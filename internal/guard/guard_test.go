package guard

import (
	"testing"
	"time"

	"github.com/mayankkaira/backstop/internal/detector"
	"github.com/mayankkaira/backstop/internal/pgstat"
)

func ptr(t time.Time) *time.Time { return &t }

func baseSnap(now time.Time, user, app string, start *time.Time) pgstat.Snapshot {
	return pgstat.Snapshot{
		At: now,
		Backends: map[int32]pgstat.Backend{
			528: {PID: 528, User: user, ApplicationName: app, State: "active",
				Query: "ALTER TABLE users ADD COLUMN y int", BackendStart: start},
			600: {PID: 600, User: "web", State: "active", Query: "SELECT * FROM users",
				QueryStart: ptr(now.Add(-9 * time.Second)), Blockers: []int32{528}},
		},
		Pending: []pgstat.Lock{{PID: 528, Mode: pgstat.ModeAccessExclusive, RelationName: "users"}},
	}
}

func newGuard() *Guard {
	return New(Config{
		Mode:           "cancel",
		ProtectedPIDs:  map[int32]bool{999: true},
		ProtectedUsers: map[string]bool{"postgres": true},
		ProtectedApps:  map[string]bool{"backstop": true},
		Cooldown:       60 * time.Second,
		IdentityTTL:    5 * time.Second,
	})
}

func verdict(pending bool) detector.Verdict {
	return detector.Verdict{TargetPID: 528, Pending: pending, Waiters: 6}
}

func TestAllowsSafeTarget(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "app", "migration", ptr(now.Add(-time.Minute))), verdict(true), now)
	if !d.Allowed {
		t.Fatalf("expected allowed, got reasons %v", d.Reasons)
	}
}

func TestBlocksProtectedUser(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "postgres", "psql", ptr(now.Add(-time.Minute))), verdict(true), now)
	if d.Allowed {
		t.Fatal("expected protected user to be blocked")
	}
}

func TestBlocksProtectedApp(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "app", "backstop", ptr(now.Add(-time.Minute))), verdict(true), now)
	if d.Allowed {
		t.Fatal("expected protected application to be blocked")
	}
}

func TestBlocksProtectedPID(t *testing.T) {
	now := time.Now()
	g := newGuard()
	g.cfg.ProtectedPIDs[528] = true
	d := g.Inspect(baseSnap(now, "app", "migration", ptr(now.Add(-time.Minute))), verdict(true), now)
	if d.Allowed {
		t.Fatal("expected protected pid to be blocked")
	}
}

func TestBlocksMissingTarget(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "app", "migration", ptr(now.Add(-time.Minute))), detector.Verdict{TargetPID: 4242, Pending: true}, now)
	if d.Allowed {
		t.Fatal("expected block when the target is gone")
	}
}

func TestBlocksWhenPendingLockGone(t *testing.T) {
	now := time.Now()
	g := newGuard()
	snap := baseSnap(now, "app", "migration", ptr(now.Add(-time.Minute)))
	snap.Pending = nil
	d := g.Inspect(snap, verdict(true), now)
	if d.Allowed {
		t.Fatal("expected block when the pending strong lock disappeared")
	}
}

func TestBlocksIdleHolderWithoutTerminate(t *testing.T) {
	now := time.Now()
	g := newGuard()
	snap := baseSnap(now, "app", "psql", ptr(now.Add(-time.Minute)))
	snap.Pending = nil
	v := detector.Verdict{TargetPID: 528, Pending: false, TargetIdleInXact: true, Waiters: 6}
	d := g.Inspect(snap, v, now)
	if d.Allowed {
		t.Fatal("expected block: cancelling a query cannot release an idle-in-transaction lock")
	}
}

func TestBlocksWithoutBackendStart(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "app", "migration", nil), verdict(true), now)
	if d.Allowed {
		t.Fatal("expected block when backend_start is unavailable")
	}
}

func TestCooldownBlocksRepeat(t *testing.T) {
	now := time.Now()
	g := newGuard()
	g.MarkActed(528, now.Add(-10*time.Second))
	d := g.Inspect(baseSnap(now, "app", "migration", ptr(now.Add(-time.Minute))), verdict(true), now)
	if d.Allowed {
		t.Fatal("expected cooldown to block a repeat action")
	}
}

func TestCooldownExpires(t *testing.T) {
	now := time.Now()
	g := newGuard()
	g.MarkActed(528, now.Add(-2*time.Minute))
	d := g.Inspect(baseSnap(now, "app", "migration", ptr(now.Add(-time.Minute))), verdict(true), now)
	if !d.Allowed {
		t.Fatalf("expected cooldown to have expired, reasons %v", d.Reasons)
	}
}
