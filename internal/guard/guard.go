package guard

import (
	"strings"
	"time"

	"github.com/mayankkaira/backstop/internal/detector"
	"github.com/mayankkaira/backstop/internal/pgstat"
)

type Decision struct {
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

	target, ok := snap.Backends[v.TargetPID]
	if !ok {
		return Decision{TargetPID: v.TargetPID, Reasons: []string{"target pid is no longer present"}}
	}

	if _, pending := snap.PendingStrong()[v.TargetPID]; !pending {
		allowed = false
		if v.TargetIdleInXact {
			reasons = append(reasons, "idle-in-transaction holds its lock; cancelling the current query would not release it")
		} else {
			reasons = append(reasons, "target has no pending strong lock")
		}
	}
	if g.cfg.ProtectedPIDs[v.TargetPID] {
		allowed = false
		reasons = append(reasons, "target pid is on the protected list")
	}
	if g.cfg.ProtectedUsers[strings.ToLower(target.User)] {
		allowed = false
		reasons = append(reasons, "target user is protected: "+target.User)
	}
	if g.cfg.ProtectedApps[strings.ToLower(target.ApplicationName)] {
		allowed = false
		reasons = append(reasons, "target application is protected: "+target.ApplicationName)
	}
	if target.BackendStart == nil {
		allowed = false
		reasons = append(reasons, "target has no backend_start for identity verification")
	}
	if last, seen := g.last[v.TargetPID]; seen && now.Sub(last) < g.cfg.Cooldown {
		allowed = false
		reasons = append(reasons, "cooldown active for this target")
	}

	if allowed {
		reasons = append(reasons, "all rails passed")
	}
	return Decision{Allowed: allowed, TargetPID: v.TargetPID, Reasons: reasons}
}

func (g *Guard) MarkActed(pid int32, at time.Time) {
	g.last[pid] = at
}
