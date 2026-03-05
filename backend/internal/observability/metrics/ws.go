package metrics

import "github.com/prometheus/client_golang/prometheus"

// WSMetrics holds collectors for WebSocket connection and message handling.
type WSMetrics struct {
	Connected        *prometheus.GaugeVec
	ReconnectsTotal  *prometheus.CounterVec
	MessagesTotal    *prometheus.CounterVec
	MsgProcDuration  *prometheus.HistogramVec
	LastMsgTimestamp *prometheus.GaugeVec
}

func newWSMetrics(reg *prometheus.Registry) WSMetrics {
	m := WSMetrics{
		Connected: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "omo_ws_connected",
			Help: "WebSocket connection state (1=connected, 0=disconnected).",
		}, []string{"feed"}),

		ReconnectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_ws_reconnects_total",
			Help: "Total WebSocket reconnection attempts.",
		}, []string{"feed", "reason"}),

		MessagesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_ws_messages_total",
			Help: "Total WebSocket messages received by type.",
		}, []string{"feed", "msg_type"}),

		MsgProcDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "omo_ws_message_processing_duration_seconds",
			Help:    "Duration of WebSocket message processing.",
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"feed", "msg_type", "result"}),

		LastMsgTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "omo_ws_last_message_timestamp_seconds",
			Help: "Unix timestamp of the last WebSocket message received.",
		}, []string{"feed"}),
	}
	reg.MustRegister(m.Connected, m.ReconnectsTotal, m.MessagesTotal, m.MsgProcDuration, m.LastMsgTimestamp)
	return m
}
