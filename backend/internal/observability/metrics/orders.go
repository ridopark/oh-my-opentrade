package metrics

import "github.com/prometheus/client_golang/prometheus"

type OrderMetrics struct {
	Total             *prometheus.CounterVec
	SubmitLat         *prometheus.HistogramVec
	FillsTotal        *prometheus.CounterVec
	FillLat           *prometheus.HistogramVec
	RejectsTotal      *prometheus.CounterVec
	TradeWSConnected  prometheus.Gauge
	TradeWSReconnects prometheus.Counter
}

func newOrderMetrics(reg *prometheus.Registry) OrderMetrics {
	m := OrderMetrics{
		Total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_orders_total",
			Help: "Total orders submitted to the broker.",
		}, []string{"venue", "strategy", "side", "order_type", "result"}),

		SubmitLat: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "omo_order_submit_latency_seconds",
			Help:    "Latency of order submission to the broker.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"venue", "strategy", "order_type"}),

		FillsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_order_fills_total",
			Help: "Total fills received from the broker.",
		}, []string{"venue", "strategy", "side", "result"}),

		FillLat: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "omo_order_fill_latency_seconds",
			Help:    "Time from order submission to fill confirmation.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"venue", "strategy"}),

		RejectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_order_rejects_total",
			Help: "Total order rejections by reason.",
		}, []string{"venue", "strategy", "reason"}),

		TradeWSConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "omo_trade_ws_connected",
			Help: "Whether the trade updates WebSocket is connected (1) or disconnected (0).",
		}),

		TradeWSReconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "omo_trade_ws_reconnects_total",
			Help: "Total trade WebSocket reconnection attempts.",
		}),
	}
	reg.MustRegister(m.Total, m.SubmitLat, m.FillsTotal, m.FillLat, m.RejectsTotal, m.TradeWSConnected, m.TradeWSReconnects)
	return m
}
