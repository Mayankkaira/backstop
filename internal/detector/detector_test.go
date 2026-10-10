package detector

import (
	"testing"
	"time"

	"github.com/mayankkaira/backstop/internal/pgstat"
)

func ts(now time.Time, secAgo float64) *time.Time {
	v := now.Add(-time.Duration(secAgo * float64(time.Second)))
	return &v
}

func snapWithRoot(now time.Time, rootWaiters int, waitSeconds float64, rootIdle bool, waiterQuery string) pgstat.Snapshot {
	backends := map[int32]pgstat.Backend{
		100: {
			PID:          100,
			User:         "app",
			State:        "active",
			Query:        "ALTER TABLE users ADD COLUMN x int",
			BackendStart: ts(now, 3600),
		},
	}
	if rootIdle {
		backends[100] = pgstat.Backend{
			PID:          100,
			User:         "app",
			State:        "idle in transaction",
			Query:        "BEGIN; SELECT 1",
			XactStart:    ts(now, 600),
			BackendStart: ts(now, 3600),
		}
	}
	held := []pgstat.Lock{{PID: 100, Mode: pgstat.ModeAccessExclusive, RelationName: "users", SchemaName: "public"}}
	pending := []pgstat.Lock{{PID: 100, Mode: pgstat.ModeAccessExclusive, RelationName: "users", SchemaName: "public"}}
	for i := 0; i < rootWaiters; i++ {
		pid := int32(200 + i)
		backends[pid] = pgstat.Backend{
			PID:          pid,
			User:         "app",
			State:        "active",
			Query:        waiterQuery,
			QueryStart:   ts(now, waitSeconds),
			BackendStart: ts(now, 3600),
			Blockers:     []int32{100},
		}
		pending = append(pending, pgstat.Lock{PID: pid, Mode: "AccessShareLock", RelationName: "users"})
	}
	return pgstat.Snapshot{At: now, Backends: backends, Held: held, Pending: pending}
}

func TestDetectsSpiralBehindRoot(t *testing.T) {
	now := time.Now()
	trend := NewTrend(10 * time.Second)
	trend.Observe(snapWithRoot(now.Add(-6*time.Second), 1, 8, false, "SELECT * FROM users"), now.Add(-6*time.Second))
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 2, RequireHotTraffic: true}, trend)
	snap := snapWithRoot(now, 8, 9, false, "SELECT * FROM users")
	trend.Observe(snap, now)

	v, ok := d.Evaluate(snap, now)
	if !ok {
		t.Fatal("expected spiral to be detected")
	}
	if v.RootPID != 100 {
		t.Fatalf("root pid = %d, want 100", v.RootPID)
	}
	if v.Waiters != 8 {
		t.Fatalf("waiters = %d, want 8", v.Waiters)
	}
	if !v.RootHoldsExclusive {
		t.Fatal("expected root to hold an exclusive lock")
	}
	if v.LockMode != pgstat.ModeAccessExclusive {
		t.Fatalf("lock mode = %q", v.LockMode)
	}
}

func TestIgnoresWaitersBlockedByWaiter(t *testing.T) {
	now := time.Now()
	trend := NewTrend(10 * time.Second)
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1, RequireHotTraffic: true}, trend)

	backends := map[int32]pgstat.Backend{}
	for i := 0; i < 6; i++ {
		pid := int32(300 + i)
		blockers := []int32{300}
		if i > 0 {
			blockers = []int32{int32(300 + i - 1)}
		}
		backends[pid] = pgstat.Backend{
			PID: pid, State: "active", Query: "SELECT 1",
			QueryStart: ts(now, 10), BackendStart: ts(now, 3600), Blockers: blockers,
		}
	}
	snap := pgstat.Snapshot{At: now, Backends: backends}
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("no root holds an exclusive lock, detection must not fire")
	}
}

func TestRequiresMinimumWaiters(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1}, NewTrend(10*time.Second))
	snap := snapWithRoot(now, 2, 30, false, "SELECT 1")
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("two waiters must not trigger detection")
	}
}

func TestRequiresMinimumWaitAge(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1}, NewTrend(10*time.Second))
	snap := snapWithRoot(now, 8, 1, false, "SELECT 1")
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("young waits must not trigger detection")
	}
}

func TestRequiresHotTrafficWhenConfigured(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1, RequireHotTraffic: true}, NewTrend(10*time.Second))
	snap := snapWithRoot(now, 8, 10, false, "ALTER TABLE t ADD COLUMN y int")
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("DDL waiters are not hot traffic; must not trigger")
	}
	d2 := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1, RequireHotTraffic: false}, NewTrend(10*time.Second))
	if _, ok := d2.Evaluate(snap, now); !ok {
		t.Fatal("with the gate off, detection should fire")
	}
}

func TestRequiresGrowth(t *testing.T) {
	now := time.Now()
	trend := NewTrend(10 * time.Second)
	steady := snapWithRoot(now.Add(-8*time.Second), 8, 30, false, "SELECT 1")
	trend.Observe(steady, now.Add(-8*time.Second))
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 2, RequireHotTraffic: true}, trend)
	snap := snapWithRoot(now, 8, 31, false, "SELECT 1")
	trend.Observe(snap, now)
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("flat queue must not trigger when growth is required")
	}
}

func TestGrowthComputation(t *testing.T) {
	now := time.Now()
	trend := NewTrend(10 * time.Second)
	trend.Observe(snapWithRoot(now.Add(-6*time.Second), 4, 1, false, "SELECT 1"), now.Add(-6*time.Second))
	trend.Observe(snapWithRoot(now.Add(-3*time.Second), 10, 1, false, "SELECT 1"), now.Add(-3*time.Second))
	snap := snapWithRoot(now, 20, 1, false, "SELECT 1")
	trend.Observe(snap, now)
	g := trend.Growth(100, 20, now)
	if g < 4.9 || g > 5.1 {
		t.Fatalf("growth = %v, want about 5", g)
	}
}

func TestIdleInTransactionIsFlagged(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1, RequireHotTraffic: true}, NewTrend(10*time.Second))
	snap := snapWithRoot(now, 8, 10, true, "SELECT * FROM users")
	v, ok := d.Evaluate(snap, now)
	if !ok {
		t.Fatal("expected detection")
	}
	if !v.RootIsIdleInXact {
		t.Fatal("expected idle-in-transaction to be flagged")
	}
}
