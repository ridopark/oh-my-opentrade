package metrics

import "github.com/prometheus/client_golang/prometheus"

// RiskMetrics holds collectors for circuit breaker and risk checks.
type RiskMetrics struct {
	CBTripsTotal *prometheus.CounterVec
	CBActive     *prometheus.GaugeVec
	ChecksTotal  *prometheus.CounterVec
	PosShares    *prometheus.GaugeVec
	ExposureUSD  *prometheus.GaugeVec
}

func newRiskMetrics(reg *prometheus.Registry) RiskMetrics {
	m := RiskMetrics{
		CBTripsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_risk_circuit_breaker_trips_total",
			Help: "Total circuit breaker trip events by reason.",
		}, []string{"reason"}),

		CBActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "omo_risk_circuit_breaker_active",
			Help: "Whether a circuit breaker is currently active (1) or not (0).",
		}, []string{"reason"}),

		ChecksTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "omo_risk_checks_total",
			Help: "Total risk check evaluations by check type and result.",
		}, []string{"check", "result"}),

		PosShares: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "omo_positions_shares",
			Help: "Current position size in shares.",
		}, []string{"symbol", "side"}),

		ExposureUSD: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "omo_exposure_usd",
			Help: "Current USD exposure.",
		}, []string{"kind"}),
	}
	reg.MustRegister(m.CBTripsTotal, m.CBActive, m.ChecksTotal, m.PosShares, m.ExposureUSD)
	return m
}
