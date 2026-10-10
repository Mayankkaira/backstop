package detector

import (
	"time"

	"github.com/mayankkaira/backstop/internal/pgstat"
)

type Sample struct {
	At      time.Time
	Waiters int
	ByRoot  map[int32]int
}

type Trend struct {
	window  time.Duration
	samples []Sample
}

func NewTrend(window time.Duration) *Trend {
	return &Trend{window: window}
}

func (t *Trend) Observe(snap pgstat.Snapshot, at time.Time) {
	byRoot := snap.WaitersPerBlocker()
	s := Sample{At: at, Waiters: len(snap.WaitingPIDs()), ByRoot: byRoot}
	t.samples = append(t.samples, s)
	t.prune(at)
}

func (t *Trend) Growth(root int32, current int, at time.Time) float64 {
	cutoff := at.Add(-t.window)
	baseline := -1
	for _, s := range t.samples {
		if s.At.Before(cutoff) {
			continue
		}
		v := s.ByRoot[root]
		if baseline < 0 || v < baseline {
			baseline = v
		}
	}
	if baseline < 0 {
		return 1
	}
	if baseline == 0 {
		if current > 0 {
			return growthCeiling
		}
		return 1
	}
	return float64(current) / float64(baseline)
}

func (t *Trend) Samples() int {
	return len(t.samples)
}

func (t *Trend) prune(at time.Time) {
	cutoff := at.Add(-t.window)
	keep := t.samples[:0]
	for _, s := range t.samples {
		if s.At.Before(cutoff) {
			continue
		}
		keep = append(keep, s)
	}
	t.samples = keep
}

const growthCeiling = 1000.0
