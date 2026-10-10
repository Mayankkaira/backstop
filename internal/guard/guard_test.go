package guard

import (
	"testing"
	"time"

	"github.com/mayankkaira/backstop/internal/detector"
	"github.com/mayankkaira/backstop/internal/pgstat"
)

func baseSnap(now time.Time, user, app string) pgstat.Snapshot {
	start := now.Add(-1 * time.Hour)
	return pgstat.Snapshot{
		At: now,
		Backends: map[int32]pgstat.Backend{
			100: {PID: 100, User: user, ApplicationName: app, State: "idle in transaction", BackendStart: &start},
			200: {PID: 200, User: "app", State: "active", Query: "SELECT 1", Blockers: []int32{100}},
		},
		Held: []pgstat.Lock{{PID: 100, Mode: pgstat.ModeAccessExclusive, RelationName: "users"}},
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

func TestAllowsSafeTarget(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "app", "web"), detector.Verdict{RootPID: 100}, now)
	if !d.Allowed {
		t.Fatalf("expected allowed, got reasons %v", d.Reasons)
	}
}

func TestBlocksProtectedUser(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "postgres", "psql"), detector.Verdict{RootPID: 100}, now)
	if d.Allowed {
		t.Fatal("expected protected user to be blocked")
	}
}

func TestBlocksProtectedApp(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "app", "backstop"), detector.Verdict{RootPID: 100}, now)
	if d.Allowed {
		t.Fatal("expected protected application to be blocked")
	}
}

func TestBlocksWhenRootLostLock(t *testing.T) {
	now := time.Now()
	g := newGuard()
	snap := baseSnap(now, "app", "web")
	snap.Held = nil
	d := g.Inspect(snap, detector.Verdict{RootPID: 100}, now)
	if d.Allowed {
		t.Fatal("expected block when root no longer holds the lock")
	}
}

func TestBlocksMissingRoot(t *testing.T) {
	now := time.Now()
	g := newGuard()
	d := g.Inspect(baseSnap(now, "app", "web"), detector.Verdict{RootPID: 4242}, now)
	if d.Allowed {
		t.Fatal("expected block when root is gone")
	}
}

func TestCooldownBlocksRepeat(t *testing.T) {
	now := time.Now()
	g := newGuard()
	g.MarkActed(100, now.Add(-10*time.Second))
	d := g.Inspect(baseSnap(now, "app", "web"), detector.Verdict{RootPID: 100}, now)
	if d.Allowed {
		t.Fatal("expected cooldown to block a repeat action")
	}
}

func TestCooldownExpires(t *testing.T) {
	now := time.Now()
	g := newGuard()
	g.MarkActed(100, now.Add(-2*time.Minute))
	d := g.Inspect(baseSnap(now, "app", "web"), detector.Verdict{RootPID: 100}, now)
	if !d.Allowed {
		t.Fatalf("expected cooldown to have expired, reasons %v", d.Reasons)
	}
}
