package detector

import (
	"time"

	"github.com/mayankkaira/backstop/internal/pgstat"
)

type Thresholds struct {
	MinWaiters        int
	MinWaitSeconds    int
	GrowthFactor      float64
	RequireHotTraffic bool
}

type Verdict struct {
	Spiral           bool
	TargetPID        int32
	TargetUser       string
	TargetApp        string
	TargetState      string
	TargetQuery      string
	TargetIdleInXact bool
	Pending          bool
	LockMode         string
	Relation         string
	Schema           string
	Waiters          int
	OldestWaitSecs   float64
	Growth           float64
	HotTraffic       bool
	Reason           string
}

type Detector struct {
	th    Thresholds
	trend *Trend
}

func New(th Thresholds, trend *Trend) *Detector {
	return &Detector{th: th, trend: trend}
}

func (d *Detector) Evaluate(snap pgstat.Snapshot, at time.Time) (Verdict, bool) {
	if len(snap.WaitingPIDs()) < d.th.MinWaiters {
		return Verdict{}, false
	}

	pending := snap.PendingStrong()
	held := snap.HeldExclusive()

	best := Verdict{}
	found := false

	for _, pid := range snap.CorkPIDs() {
		b, ok := snap.Backends[pid]
		if !ok {
			continue
		}
		lock, isPending := pending[pid]
		if !isPending {
			lock = held[pid]
		}
		down := snap.Downstream(pid)
		if len(down) < d.th.MinWaiters {
			continue
		}
		oldest := snap.OldestDownstreamWaitSeconds(at, down)
		if oldest < float64(d.th.MinWaitSeconds) {
			continue
		}
		hot := false
		if d.th.RequireHotTraffic {
			for _, dp := range down {
				if wb, ok := snap.Backends[dp]; ok && pgstat.IsHotQuery(wb.Query) {
					hot = true
					break
				}
			}
			if !hot {
				continue
			}
		}
		growth := 1.0
		if d.trend != nil {
			growth = d.trend.Growth(pid, len(down), at)
		}
		if d.th.GrowthFactor > 1 && growth < d.th.GrowthFactor {
			continue
		}
		v := Verdict{
			Spiral:           true,
			TargetPID:        pid,
			TargetUser:       b.User,
			TargetApp:        b.ApplicationName,
			TargetState:      b.State,
			TargetQuery:      pgstat.CompactQuery(b.Query, 300),
			TargetIdleInXact: b.State == "idle in transaction",
			Pending:          isPending,
			LockMode:         lock.Mode,
			Relation:         lock.RelationName,
			Schema:           lock.SchemaName,
			Waiters:          len(down),
			OldestWaitSecs:   oldest,
			Growth:           growth,
			HotTraffic:       hot,
		}
		if !found || v.Waiters > best.Waiters {
			best = v
			found = true
		}
	}

	if !found {
		return Verdict{}, false
	}
	best.Reason = reason(best)
	return best, true
}

func reason(v Verdict) string {
	if v.Pending {
		return "schema change waiting for a strong lock is corking the queue"
	}
	if v.TargetIdleInXact {
		return "idle-in-transaction session holding a strong lock while the queue builds"
	}
	return "session holding a strong lock is corking the queue"
}
