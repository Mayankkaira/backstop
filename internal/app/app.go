package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mayankkaira/backstop/internal/actuator"
	"github.com/mayankkaira/backstop/internal/config"
	"github.com/mayankkaira/backstop/internal/detector"
	"github.com/mayankkaira/backstop/internal/guard"
	"github.com/mayankkaira/backstop/internal/metrics"
	"github.com/mayankkaira/backstop/internal/pgstat"
)

type App struct {
	cfg       config.Config
	log       *slog.Logger
	pool      *pgxpool.Pool
	collector *pgstat.Collector
	detector  *detector.Detector
	trend     *detector.Trend
	guard     *guard.Guard
	actuator  *actuator.Actuator
	metrics   *metrics.Metrics
}

func New(cfg config.Config, log *slog.Logger, pool *pgxpool.Pool, m *metrics.Metrics) *App {
	trend := detector.NewTrend(cfg.GrowthWindow())
	det := detector.New(detector.Thresholds{
		MinWaiters:        cfg.MinWaiters,
		MinWaitSeconds:    cfg.MinWaitSeconds,
		GrowthFactor:      cfg.GrowthFactor,
		RequireHotTraffic: cfg.RequireHotTraffic,
	}, trend)
	g := guard.New(guard.Config{
		Mode:           cfg.Mode,
		ProtectedPIDs:  cfg.ProtectedPIDSet(),
		ProtectedUsers: cfg.ProtectedUserSet(),
		ProtectedApps:  cfg.ProtectedAppSet(),
		Cooldown:       cfg.Cooldown(),
		IdentityTTL:    cfg.IdentityTTL(),
	})
	return &App{
		cfg:       cfg,
		log:       log,
		pool:      pool,
		collector: pgstat.NewCollector(pool),
		detector:  det,
		trend:     trend,
		guard:     g,
		actuator:  actuator.New(pool),
		metrics:   m,
	}
}

func (a *App) Ping(ctx context.Context) error {
	return a.pool.Ping(ctx)
}

func (a *App) Run(ctx context.Context) error {
	a.log.Info("backstop started",
		"mode", a.cfg.Mode,
		"poll_interval", a.cfg.PollInterval().String(),
		"min_waiters", a.cfg.MinWaiters,
		"min_wait_seconds", a.cfg.MinWaitSeconds,
		"growth_factor", a.cfg.GrowthFactor,
	)
	ticker := time.NewTicker(a.cfg.PollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.log.Info("backstop stopping")
			return nil
		case <-ticker.C:
			a.cycle(ctx)
		}
	}
}

func (a *App) cycle(ctx context.Context) {
	now := time.Now()
	snap, err := a.collector.Snapshot(ctx)
	if err != nil {
		a.metrics.CycleError()
		a.log.Error("collection failed", "error", err)
		return
	}
	a.metrics.CycleOK()
	a.trend.Observe(snap, now)

	waiters := len(snap.WaitingPIDs())
	oldest := snap.OldestWaitSeconds(now)
	blocked := len(snap.WaitersPerBlocker())

	v, spiral := a.detector.Evaluate(snap, now)
	a.metrics.SetState(waiters, oldest, blocked, spiral)
	if !spiral {
		return
	}

	a.metrics.SpiralDetected()
	a.log.Warn("incipient lock queue spiral detected",
		"reason", v.Reason,
		"root_pid", v.RootPID,
		"root_user", v.RootUser,
		"root_app", v.RootApp,
		"root_state", v.RootState,
		"lock_mode", v.LockMode,
		"relation", v.Relation,
		"schema", v.Schema,
		"waiters", v.Waiters,
		"oldest_wait_seconds", round(v.OldestWaitSeconds),
		"growth", round(v.Growth),
		"hot_traffic", v.HotTraffic,
		"idle_in_transaction", v.RootIsIdleInXact,
		"root_query", v.RootQuery,
	)

	decision := a.guard.Inspect(snap, v, now)
	if !decision.Allowed {
		a.metrics.Action("blocked")
		a.log.Warn("action withheld by safety rails",
			"root_pid", v.RootPID,
			"reasons", decision.Reasons,
		)
		return
	}

	if a.cfg.Mode != config.ModeCancel {
		a.metrics.Action("observed")
		a.log.Info("observe mode: would cancel blocker",
			"root_pid", v.RootPID,
			"waiters", v.Waiters,
			"lock_mode", v.LockMode,
			"relation", v.Relation,
		)
		return
	}

	a.cancel(ctx, snap, v, now)
}

func (a *App) cancel(ctx context.Context, snap pgstat.Snapshot, v detector.Verdict, now time.Time) {
	expected := snap.Backends[v.RootPID].BackendStart
	if expected == nil {
		a.metrics.Action("blocked")
		a.log.Warn("action withheld: no backend_start for root", "root_pid", v.RootPID)
		return
	}
	current, err := a.collector.BackendIdentity(ctx, v.RootPID)
	if err != nil {
		a.metrics.Action("failed")
		a.log.Error("identity re-check failed, not acting", "root_pid", v.RootPID, "error", err)
		return
	}
	if current == nil || !current.Equal(*expected) {
		a.metrics.Action("blocked")
		a.log.Warn("action withheld: backend identity changed (possible pid reuse)", "root_pid", v.RootPID)
		return
	}

	actCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := a.actuator.Cancel(actCtx, v.RootPID)
	if err != nil {
		a.metrics.Action("failed")
		a.log.Error("cancel failed", "root_pid", v.RootPID, "error", err)
		return
	}
	a.guard.MarkActed(v.RootPID, now)
	a.metrics.Action("cancelled")
	a.log.Warn("cancelled blocker holding exclusive lock",
		"root_pid", res.PID,
		"lock_mode", v.LockMode,
		"relation", v.Relation,
		"waiters", v.Waiters,
	)
}

func (a *App) Verify(ctx context.Context) error {
	if err := a.pool.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}
	if _, err := a.collector.Snapshot(ctx); err != nil {
		return fmt.Errorf("snapshot probe failed: %w", err)
	}
	return nil
}

func round(f float64) float64 {
	return float64(int(f*100)) / 100
}

var errNotReady = errors.New("not ready")

func (a *App) Ready(ctx context.Context) error {
	if err := a.Verify(ctx); err != nil {
		return errors.Join(errNotReady, err)
	}
	return nil
}
