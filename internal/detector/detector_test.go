package detector

import (
	"testing"
	"time"

	"github.com/mayankkaira/backstop/internal/pgstat"
)

func at(now time.Time, secAgo float64) *time.Time {
	v := now.Add(-time.Duration(secAgo * float64(time.Second)))
	return &v
}

type scenario struct {
	now         time.Time
	waiters     int
	waitSeconds float64
	waiterQuery string
	pendingDDL  bool
	idleHolder  bool
	weakCork    bool
	blockedBy   int32
}

func build(s scenario) pgstat.Snapshot {
	const corkA, corkB = int32(528), int32(521)
	cork := corkA
	if s.idleHolder {
		cork = corkB
	}
	if s.blockedBy != 0 {
		cork = s.blockedBy
	}
	backends := map[int32]pgstat.Backend{
		cork: {PID: cork, User: "app", ApplicationName: "migration", State: "active",
			Query: "ALTER TABLE users ADD COLUMN last_seen timestamptz", BackendStart: at(s.now, 60)},
	}
	var held, pending []pgstat.Lock
	switch {
	case s.idleHolder:
		backends[cork] = pgstat.Backend{PID: cork, User: "app", ApplicationName: "psql", State: "idle in transaction",
			Query: "BEGIN; SELECT 1", XactStart: at(s.now, 120), BackendStart: at(s.now, 3600)}
		held = append(held, pgstat.Lock{PID: cork, Mode: pgstat.ModeAccessExclusive, RelationName: "users", SchemaName: "public"})
	case s.weakCork:
		pending = append(pending, pgstat.Lock{PID: cork, Mode: "AccessShareLock", RelationName: "users", SchemaName: "public"})
	default:
		pending = append(pending, pgstat.Lock{PID: cork, Mode: pgstat.ModeAccessExclusive, RelationName: "users", SchemaName: "public"})
	}
	for i := 0; i < s.waiters; i++ {
		pid := int32(600 + i)
		backends[pid] = pgstat.Backend{
			PID: pid, User: "app", ApplicationName: "web", State: "active",
			Query: s.waiterQuery, QueryStart: at(s.now, s.waitSeconds), BackendStart: at(s.now, 3600),
			Blockers: []int32{cork},
		}
		pending = append(pending, pgstat.Lock{PID: pid, Mode: "AccessShareLock", RelationName: "users"})
	}
	return pgstat.Snapshot{At: s.now, Backends: backends, Held: held, Pending: pending}
}

func TestDetectsSpiralBehindPendingDDL(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1, RequireHotTraffic: true}, NewTrend(10*time.Second))
	snap := build(scenario{now: now, waiters: 6, waitSeconds: 8, waiterQuery: "SELECT * FROM users", pendingDDL: true})

	v, ok := d.Evaluate(snap, now)
	if !ok {
		t.Fatal("expected the spiral to be detected")
	}
	if v.TargetPID != 528 {
		t.Fatalf("target pid = %d, want 528", v.TargetPID)
	}
	if !v.Pending {
		t.Fatal("expected the cork to be a pending strong lock")
	}
	if v.Waiters != 6 {
		t.Fatalf("waiters = %d, want 6", v.Waiters)
	}
	if v.Relation != "users" {
		t.Fatalf("relation = %q, want users", v.Relation)
	}
}

func TestDetectsIdleInTransactionHolder(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1, RequireHotTraffic: true}, NewTrend(10*time.Second))
	snap := build(scenario{now: now, waiters: 6, waitSeconds: 8, waiterQuery: "SELECT * FROM users", idleHolder: true})

	v, ok := d.Evaluate(snap, now)
	if !ok {
		t.Fatal("expected the spiral to be detected")
	}
	if v.Pending {
		t.Fatal("idle holder holds the lock, it is not pending")
	}
	if !v.TargetIdleInXact {
		t.Fatal("expected idle-in-transaction to be flagged")
	}
}

func TestIgnoresWhenNoStrongLockInvolved(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1}, NewTrend(10*time.Second))
	snap := build(scenario{now: now, waiters: 6, waitSeconds: 10, waiterQuery: "SELECT 1", weakCork: true})
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("a pending lock that is not strong must not trigger detection")
	}
}

func TestRequiresMinimumWaiters(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1}, NewTrend(10*time.Second))
	snap := build(scenario{now: now, waiters: 2, waitSeconds: 30, waiterQuery: "SELECT 1", pendingDDL: true})
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("two waiters must not trigger detection")
	}
}

func TestRequiresMinimumWaitAge(t *testing.T) {
	now := time.Now()
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1}, NewTrend(10*time.Second))
	snap := build(scenario{now: now, waiters: 8, waitSeconds: 1, waiterQuery: "SELECT 1", pendingDDL: true})
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("young waits must not trigger detection")
	}
}

func TestRequiresHotTrafficWhenConfigured(t *testing.T) {
	now := time.Now()
	ddlWaiters := build(scenario{now: now, waiters: 8, waitSeconds: 10, waiterQuery: "ALTER TABLE t ADD COLUMN y int", pendingDDL: true})

	gated := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1, RequireHotTraffic: true}, NewTrend(10*time.Second))
	if _, ok := gated.Evaluate(ddlWaiters, now); ok {
		t.Fatal("DDL waiters are not hot traffic; gated detector must not fire")
	}

	open := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 1, RequireHotTraffic: false}, NewTrend(10*time.Second))
	if _, ok := open.Evaluate(ddlWaiters, now); !ok {
		t.Fatal("with the gate off, detection should fire")
	}
}

func TestRequiresGrowth(t *testing.T) {
	now := time.Now()
	trend := NewTrend(10 * time.Second)
	flat := build(scenario{now: now.Add(-8 * time.Second), waiters: 8, waitSeconds: 30, waiterQuery: "SELECT 1", pendingDDL: true})
	trend.Observe(flat, now.Add(-8*time.Second))
	d := New(Thresholds{MinWaiters: 5, MinWaitSeconds: 5, GrowthFactor: 2, RequireHotTraffic: true}, trend)
	snap := build(scenario{now: now, waiters: 8, waitSeconds: 31, waiterQuery: "SELECT 1", pendingDDL: true})
	trend.Observe(snap, now)
	if _, ok := d.Evaluate(snap, now); ok {
		t.Fatal("flat queue must not trigger when growth is required")
	}
}

func TestGrowthComputation(t *testing.T) {
	const cork = int32(528)
	now := time.Now()
	trend := NewTrend(10 * time.Second)
	for i, n := range []int{2, 4, 8} {
		tm := now.Add(-time.Duration(6-i*3) * time.Second)
		trend.Observe(build(scenario{now: tm, waiters: n, waitSeconds: 1, waiterQuery: "SELECT 1", pendingDDL: true}), tm)
	}
	g := trend.Growth(cork, 8, now)
	if g < 3.9 || g > 4.1 {
		t.Fatalf("growth = %v, want about 4", g)
	}
}
