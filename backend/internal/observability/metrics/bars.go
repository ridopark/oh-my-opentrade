package metrics

import "github.com/prometheus/client_golang/prometheus"

// BarMetrics holds collectors for bar ingestion and processing.
type BarMetrics struct {
	ReceivedTotal *prometheus.CounterVec
	ProcLatency   *prometheus.HistogramVec
	DroppedTotal  *prometheus.CounterVec
	QueueDepth    *prometheus.GaugeVec
}

func newBarMetrics(reg *prometheus.Registry) BarMetrics {
	m := BarMetrics{
		ReceivedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_bars_received_total",
			Help: "Total bars received by source, symbol, and timeframe.",
		}, []string{"source", "symbol", "timeframe"}),

		ProcLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "omo_bar_processing_latency_seconds",
			Help:    "Latency of bar processing pipeline.",
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}, []string{"source", "timeframe"}),

		DroppedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_bars_dropped_total",
			Help: "Total bars dropped by source and reason.",
		}, []string{"source", "reason"}),

		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "omo_bar_pipeline_queue_depth",
			Help: "Current depth of the bar processing queue.",
		}, []string{"source"}),
	}
	reg.MustRegister(m.ReceivedTotal, m.ProcLatency, m.DroppedTotal, m.QueueDepth)
	return m
}
