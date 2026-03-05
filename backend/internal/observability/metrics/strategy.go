package metrics

import "github.com/prometheus/client_golang/prometheus"

// StrategyMetrics holds collectors for the strategy pipeline.
type StrategyMetrics struct {
	SignalsTotal *prometheus.CounterVec
	TradesTotal  *prometheus.CounterVec
	LoopDuration *prometheus.HistogramVec
	State        *prometheus.GaugeVec
}

func newStrategyMetrics(reg *prometheus.Registry) StrategyMetrics {
	m := StrategyMetrics{
		SignalsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_strategy_signals_total",
			Help: "Total strategy signals emitted.",
		}, []string{"strategy", "signal", "direction"}),

		TradesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_strategy_trades_total",
			Help: "Total strategy trade outcomes.",
		}, []string{"strategy", "outcome"}),

		LoopDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "omo_strategy_loop_duration_seconds",
			Help:    "Duration of strategy processing phases.",
			Buckets: prometheus.DefBuckets,
		}, []string{"strategy", "phase"}),

		State: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "omo_strategy_state",
			Help: "Current strategy state (0=idle, 1=armed, 2=in_position, 3=halted).",
		}, []string{"strategy"}),
	}
	reg.MustRegister(m.SignalsTotal, m.TradesTotal, m.LoopDuration, m.State)
	return m
}
