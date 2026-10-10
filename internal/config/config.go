package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	ModeObserve = "observe"
	ModeCancel  = "cancel"
)

type Config struct {
	DatabaseURL       string   `json:"database_url"`
	Mode              string   `json:"mode"`
	PollIntervalSec   int      `json:"poll_interval_seconds"`
	MinWaiters        int      `json:"min_waiters"`
	MinWaitSeconds    int      `json:"min_wait_seconds"`
	GrowthFactor      float64  `json:"growth_factor"`
	GrowthWindowSec   int      `json:"growth_window_seconds"`
	IdentityTTLSec    int      `json:"identity_ttl_seconds"`
	CooldownSec       int      `json:"cooldown_seconds"`
	MaxRowsPerAction  int      `json:"max_rows_per_action"`
	RequireHotTraffic bool     `json:"require_hot_traffic"`
	ProtectedPIDs     []int32  `json:"protected_pids"`
	ProtectedUsers    []string `json:"protected_users"`
	ProtectedApps     []string `json:"protected_apps"`
	MetricsAddr       string   `json:"metrics_addr"`
	LogFormat         string   `json:"log_format"`
}

func Default() Config {
	return Config{
		Mode:              ModeObserve,
		PollIntervalSec:   1,
		MinWaiters:        5,
		MinWaitSeconds:    5,
		GrowthFactor:      2.0,
		GrowthWindowSec:   10,
		IdentityTTLSec:    5,
		CooldownSec:       60,
		MaxRowsPerAction:  1,
		RequireHotTraffic: true,
		ProtectedUsers:    []string{"postgres", "replication"},
		ProtectedApps:     []string{"backstop", "pgbouncer", "pgbouncers"},
		MetricsAddr:       ":9104",
		LogFormat:         "json",
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if v := strings.TrimSpace(os.Getenv("BACKSTOP_DATABASE_URL")); v != "" {
		cfg.DatabaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("BACKSTOP_MODE")); v != "" {
		cfg.Mode = v
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("database_url is required (or set BACKSTOP_DATABASE_URL)")
	}
	switch c.Mode {
	case ModeObserve, ModeCancel:
	default:
		return fmt.Errorf("mode must be %q or %q, got %q", ModeObserve, ModeCancel, c.Mode)
	}
	if c.PollIntervalSec < 1 {
		return fmt.Errorf("poll_interval_seconds must be >= 1, got %d", c.PollIntervalSec)
	}
	if c.MinWaiters < 2 {
		return fmt.Errorf("min_waiters must be >= 2, got %d", c.MinWaiters)
	}
	if c.MinWaitSeconds < 1 {
		return fmt.Errorf("min_wait_seconds must be >= 1, got %d", c.MinWaitSeconds)
	}
	if c.GrowthFactor < 1 {
		return fmt.Errorf("growth_factor must be >= 1, got %v", c.GrowthFactor)
	}
	if c.GrowthWindowSec < c.PollIntervalSec {
		return fmt.Errorf("growth_window_seconds must be >= poll_interval_seconds, got %d", c.GrowthWindowSec)
	}
	if c.IdentityTTLSec < 1 {
		return fmt.Errorf("identity_ttl_seconds must be >= 1, got %d", c.IdentityTTLSec)
	}
	if c.CooldownSec < 1 {
		return fmt.Errorf("cooldown_seconds must be >= 1, got %d", c.CooldownSec)
	}
	if c.MaxRowsPerAction < 1 {
		return fmt.Errorf("max_rows_per_action must be >= 1, got %d", c.MaxRowsPerAction)
	}
	if strings.TrimSpace(c.MetricsAddr) == "" {
		return fmt.Errorf("metrics_addr is required")
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("log_format must be %q or %q, got %q", "json", "text", c.LogFormat)
	}
	return nil
}

func (c Config) PollInterval() time.Duration {
	return time.Duration(c.PollIntervalSec) * time.Second
}

func (c Config) GrowthWindow() time.Duration {
	return time.Duration(c.GrowthWindowSec) * time.Second
}

func (c Config) IdentityTTL() time.Duration {
	return time.Duration(c.IdentityTTLSec) * time.Second
}

func (c Config) Cooldown() time.Duration {
	return time.Duration(c.CooldownSec) * time.Second
}

func (c Config) ProtectedPIDSet() map[int32]bool {
	out := make(map[int32]bool, len(c.ProtectedPIDs))
	for _, pid := range c.ProtectedPIDs {
		out[pid] = true
	}
	return out
}

func (c Config) ProtectedUserSet() map[string]bool {
	out := make(map[string]bool, len(c.ProtectedUsers))
	for _, u := range c.ProtectedUsers {
		out[strings.ToLower(strings.TrimSpace(u))] = true
	}
	return out
}

func (c Config) ProtectedAppSet() map[string]bool {
	out := make(map[string]bool, len(c.ProtectedApps))
	for _, a := range c.ProtectedApps {
		out[strings.ToLower(strings.TrimSpace(a))] = true
	}
	return out
}
