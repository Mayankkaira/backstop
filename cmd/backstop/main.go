package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mayankkaira/backstop/internal/app"
	"github.com/mayankkaira/backstop/internal/config"
	"github.com/mayankkaira/backstop/internal/metrics"
	"github.com/mayankkaira/backstop/internal/server"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "backstop:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "", "path to a JSON config file")
		mode        = flag.String("mode", "", "observe or cancel (overrides config)")
		databaseURL = flag.String("database-url", "", "Postgres connection string (overrides config and env)")
		minWaiters  = flag.Int("min-waiters", 0, "sessions queued behind one cork before acting (0 keeps config)")
		minWaitSecs = flag.Int("min-wait-seconds", 0, "oldest wait age in seconds before acting (0 keeps config)")
		growth      = flag.Float64("growth-factor", 0, "queue growth multiple required to act (0 keeps config)")
		showVersion = flag.Bool("version", false, "print version and exit")
		check       = flag.Bool("check", false, "verify configuration and connectivity, then exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("backstop", version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		if *configPath == "" && *databaseURL == "" && os.Getenv("BACKSTOP_DATABASE_URL") == "" {
			return fmt.Errorf("%w\n\nset BACKSTOP_DATABASE_URL or pass -database-url", err)
		}
		return err
	}
	if *mode != "" {
		cfg.Mode = *mode
	}
	if *databaseURL != "" {
		cfg.DatabaseURL = *databaseURL
	}
	if *minWaiters > 0 {
		cfg.MinWaiters = *minWaiters
	}
	if *minWaitSecs > 0 {
		cfg.MinWaitSeconds = *minWaitSecs
	}
	if *growth > 0 {
		cfg.GrowthFactor = *growth
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	log := newLogger(cfg.LogFormat)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	poolCfg.MaxConns = 4
	poolCfg.MinConns = 1
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.ConnConfig.RuntimeParams["application_name"] = "backstop"

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}
	log.Info("connected to database", "server_version", serverVersion(ctx, pool))

	m := metrics.New(version, cfg.Mode)
	application := app.New(cfg, log, pool, m)

	if *check {
		if err := application.Verify(ctx); err != nil {
			return err
		}
		log.Info("configuration and connectivity verified", "mode", cfg.Mode)
		return nil
	}

	srv := server.New(cfg.MetricsAddr, m.Registry, log, application.Ready)
	srvErr := make(chan error, 1)
	go func() {
		log.Info("metrics listening", "addr", cfg.MetricsAddr)
		srvErr <- srv.ListenAndServe()
	}()

	runErr := make(chan error, 1)
	go func() { runErr <- application.Run(ctx) }()

	select {
	case err := <-runErr:
		if err != nil {
			return err
		}
	case err := <-srvErr:
		if err != nil {
			return fmt.Errorf("metrics server: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Warn("metrics shutdown", "error", err)
	}
	return nil
}

func newLogger(format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if strings.EqualFold(format, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func serverVersion(ctx context.Context, pool *pgxpool.Pool) string {
	var v string
	if err := pool.QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "unknown"
	}
	return v
}
