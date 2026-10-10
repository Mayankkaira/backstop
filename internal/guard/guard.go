package guard

import (
	"strings"
	"time"

	"github.com/mayankkaira/backstop/internal/detector"
	"github.com/mayankkaira/backstop/internal/pgstat"
)

type Decision struct {
	Action    bool
	Allowed   bool
	TargetPID int32
	Reasons   []string
}

type Config struct {
	Mode           string
	ProtectedPIDs  map[int32]bool
	ProtectedUsers map[string]bool
	ProtectedApps  map[string]bool
	Cooldown       time.Duration
	IdentityTTL    time.Duration
}

type Guard struct {
	cfg  Config
	last map[int32]time.Time
}

func New(cfg Config) *Guard {
	return &Guard{cfg: cfg, last: make(map[int32]time.Time)}
}

func (g *Guard) Inspect(snap pgstat.Snapshot, v detector.Verdict, now time.Time) Decision {
	var reasons []string
	allowed := true

	root, ok := snap.Backends[v.RootPID]
	if !ok {
		return Decision{Allowed: false, TargetPID: v.RootPID, Reasons: []string{"root pid no longer present"}}
	}

	if g.cfg.ProtectedPIDs[v.RootPID] {
		allowed = false
		reasons = append(reasons, "root pid is on the protected list")
	}
	if g.cfg.ProtectedUsers[strings.ToLower(root.User)] {
		allowed = false
		reasons = append(reasons, "root user is protected: "+root.User)
	}
	if g.cfg.ProtectedApps[strings.ToLower(root.ApplicationName)] {
		allowed = false
		reasons = append(reasons, "root application is protected: "+root.ApplicationName)
	}
	if _, holds := snap.HeldExclusive()[v.RootPID]; !holds {
		allowed = false
		reasons = append(reasons, "root no longer holds the exclusive lock")
	}
	if g.cfg.IdentityTTL > 0 && root.BackendStart != nil {
		if age := now.Sub(*root.BackendStart); age < 0 {
			allowed = false
			reasons = append(reasons, "root backend_start is in the future")
		}
	}
	if last, seen := g.last[v.RootPID]; seen {
		if now.Sub(last) < g.cfg.Cooldown {
			allowed = false
			reasons = append(reasons, "cooldown active for this root")
		}
	}

	if allowed {
		reasons = append(reasons, "all rails passed")
	}
	return Decision{Allowed: allowed, TargetPID: v.RootPID, Reasons: reasons}
}

func (g *Guard) MarkActed(pid int32, at time.Time) {
	g.last[pid] = at
}

func (g *Guard) IdentityFresh(snap pgstat.Snapshot, pid int32, now time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		return true
	}
	b, ok := snap.Backends[pid]
	if !ok || b.BackendStart == nil {
		return false
	}
	age := now.Sub(*b.BackendStart)
	return age >= 0 && age < ttl
}
