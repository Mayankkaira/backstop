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
	Spiral             bool
	RootPID            int32
	RootUser           string
	RootApp            string
	RootState          string
	RootQuery          string
	LockMode           string
	Relation           string
	Schema             string
	Waiters            int
	OldestWaitSeconds  float64
	Growth             float64
	HotTraffic         bool
	RootHoldsExclusive bool
	RootIsIdleInXact   bool
	Reason             string
}

type Detector struct {
	th    Thresholds
	trend *Trend
}

func New(th Thresholds, trend *Trend) *Detector {
	return &Detector{th: th, trend: trend}
}

func (d *Detector) Evaluate(snap pgstat.Snapshot, at time.Time) (Verdict, bool) {
	waiters := snap.WaitingPIDs()
	if len(waiters) < d.th.MinWaiters {
		return Verdict{}, false
	}

	waiting := make(map[int32]bool, len(waiters))
	for _, pid := range waiters {
		waiting[pid] = true
	}

	roots := snap.WaitersPerBlocker()
	best := Verdict{}
	found := false

	for pid := range roots {
		if waiting[pid] {
			continue
		}
		b, ok := snap.Backends[pid]
		if !ok {
			continue
		}
		lock, holds := snap.HeldExclusive()[pid]
		if !holds {
			continue
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
			Spiral:             true,
			RootPID:            pid,
			RootUser:           b.User,
			RootApp:            b.ApplicationName,
			RootState:          b.State,
			RootQuery:          pgstat.CompactQuery(b.Query, 300),
			LockMode:           lock.Mode,
			Relation:           lock.RelationName,
			Schema:             lock.SchemaName,
			Waiters:            len(down),
			OldestWaitSeconds:  oldest,
			Growth:             growth,
			HotTraffic:         hot,
			RootHoldsExclusive: true,
			RootIsIdleInXact:   b.State == "idle in transaction",
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
	head := "lock queue building behind one blocker"
	if v.RootIsIdleInXact {
		head = "idle-in-transaction session holding an exclusive lock while the queue builds"
	}
	return head
}
