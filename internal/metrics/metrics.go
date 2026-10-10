package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type Metrics struct {
	Registry *prometheus.Registry

	cycles       *prometheus.CounterVec
	spirals      prometheus.Counter
	actions      *prometheus.CounterVec
	waiters      prometheus.Gauge
	oldestWait   prometheus.Gauge
	blocked      prometheus.Gauge
	spiralActive prometheus.Gauge
	build        *prometheus.GaugeVec
}

func New(version, mode string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		cycles: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "backstop_cycles_total",
			Help: "Collection cycles by result.",
		}, []string{"result"}),
		spirals: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "backstop_spirals_detected_total",
			Help: "Incipient lock queue spirals detected.",
		}),
		actions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "backstop_actions_total",
			Help: "Actions attempted by outcome.",
		}, []string{"outcome"}),
		waiters: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "backstop_lock_waiters",
			Help: "Sessions currently waiting on a lock.",
		}),
		oldestWait: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "backstop_lock_oldest_wait_seconds",
			Help: "Age in seconds of the oldest lock wait.",
		}),
		blocked: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "backstop_blocked_backends",
			Help: "Backends blocked by another backend.",
		}),
		spiralActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "backstop_spiral_active",
			Help: "1 when the last cycle detected an incipient spiral, else 0.",
		}),
		build: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "backstop_build_info",
			Help: "Build metadata.",
		}, []string{"version", "mode"}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.cycles, m.spirals, m.actions, m.waiters, m.oldestWait, m.blocked, m.spiralActive, m.build,
	)
	m.build.WithLabelValues(version, mode).Set(1)
	return m
}

func (m *Metrics) CycleOK()        { m.cycles.WithLabelValues("ok").Inc() }
func (m *Metrics) CycleError()     { m.cycles.WithLabelValues("error").Inc() }
func (m *Metrics) SpiralDetected() { m.spirals.Inc() }

func (m *Metrics) Action(outcome string) { m.actions.WithLabelValues(outcome).Inc() }

func (m *Metrics) SetState(waiters int, oldestWait float64, blocked int, spiralActive bool) {
	m.waiters.Set(float64(waiters))
	m.oldestWait.Set(oldestWait)
	m.blocked.Set(float64(blocked))
	if spiralActive {
		m.spiralActive.Set(1)
	} else {
		m.spiralActive.Set(0)
	}
}
