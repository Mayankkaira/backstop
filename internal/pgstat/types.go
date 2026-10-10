package pgstat

import (
	"sort"
	"time"
)

const (
	ModeAccessExclusive = "AccessExclusiveLock"
)

type Backend struct {
	PID             int32
	User            string
	ApplicationName string
	State           string
	WaitEventType   string
	WaitEvent       string
	Query           string
	QueryStart      *time.Time
	XactStart       *time.Time
	BackendStart    *time.Time
	ClientAddr      string
	Blockers        []int32
}

func (b Backend) Blocked() bool {
	return len(b.Blockers) > 0
}

func (b Backend) WaitSince() *time.Time {
	if b.QueryStart != nil {
		return b.QueryStart
	}
	return b.XactStart
}

type Lock struct {
	PID          int32
	LockType     string
	Mode         string
	Relation     uint32
	RelationName string
	SchemaName   string
}

type Snapshot struct {
	At       time.Time
	Backends map[int32]Backend
	Held     []Lock
	Pending  []Lock
}

func (s Snapshot) WaitingPIDs() []int32 {
	pids := make([]int32, 0, len(s.Backends))
	for pid, b := range s.Backends {
		if b.Blocked() {
			pids = append(pids, pid)
		}
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	return pids
}

func (s Snapshot) WaitersPerBlocker() map[int32]int {
	idx := make(map[int32]int)
	for _, b := range s.Backends {
		for _, blk := range b.Blockers {
			idx[blk]++
		}
	}
	return idx
}

func (s Snapshot) downstreamIndex() map[int32][]int32 {
	idx := make(map[int32][]int32)
	for pid, b := range s.Backends {
		for _, blk := range b.Blockers {
			idx[blk] = append(idx[blk], pid)
		}
	}
	return idx
}

func (s Snapshot) Downstream(root int32) []int32 {
	idx := s.downstreamIndex()
	seen := make(map[int32]bool)
	queue := append([]int32(nil), idx[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		queue = append(queue, idx[pid]...)
	}
	out := make([]int32, 0, len(seen))
	for pid := range seen {
		out = append(out, pid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s Snapshot) PendingStrong() map[int32]Lock {
	out := make(map[int32]Lock)
	for _, l := range s.Pending {
		if l.Mode != ModeAccessExclusive {
			continue
		}
		if _, ok := out[l.PID]; !ok {
			out[l.PID] = l
		}
	}
	return out
}

func (s Snapshot) HeldExclusive() map[int32]Lock {
	out := make(map[int32]Lock)
	for _, l := range s.Held {
		if l.Mode != ModeAccessExclusive {
			continue
		}
		if _, ok := out[l.PID]; !ok {
			out[l.PID] = l
		}
	}
	return out
}

func (s Snapshot) OldestWaitSeconds(at time.Time) float64 {
	oldest := 0.0
	for _, b := range s.Backends {
		if !b.Blocked() {
			continue
		}
		since := b.WaitSince()
		if since == nil {
			continue
		}
		d := at.Sub(*since).Seconds()
		if d > oldest {
			oldest = d
		}
	}
	return oldest
}

func (s Snapshot) OldestDownstreamWaitSeconds(at time.Time, pids []int32) float64 {
	oldest := 0.0
	for _, pid := range pids {
		b, ok := s.Backends[pid]
		if !ok || !b.Blocked() {
			continue
		}
		since := b.WaitSince()
		if since == nil {
			continue
		}
		d := at.Sub(*since).Seconds()
		if d > oldest {
			oldest = d
		}
	}
	return oldest
}
