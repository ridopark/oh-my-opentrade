package metrics

import "github.com/prometheus/client_golang/prometheus"

// PnLMetrics holds gauges for real-time P&L tracking.
type PnLMetrics struct {
	RealizedUSD   prometheus.Gauge
	UnrealizedUSD prometheus.Gauge
	DayUSD        prometheus.Gauge
	DayDDUSD      prometheus.Gauge
	EquityUSD     prometheus.Gauge
}

func newPnLMetrics(reg *prometheus.Registry) PnLMetrics {
	m := PnLMetrics{
		RealizedUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "omo_pnl_realized_usd",
			Help: "Cumulative realized P&L in USD for the current day.",
		}),
		UnrealizedUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "omo_pnl_unrealized_usd",
			Help: "Current unrealized P&L in USD.",
		}),
		DayUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "omo_pnl_day_usd",
			Help: "Total P&L (realized + unrealized) for the current day.",
		}),
		DayDDUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "omo_drawdown_day_usd",
			Help: "Maximum intraday drawdown in USD.",
		}),
		EquityUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "omo_equity_usd",
			Help: "Current account equity in USD.",
		}),
	}
	reg.MustRegister(m.RealizedUSD, m.UnrealizedUSD, m.DayUSD, m.DayDDUSD, m.EquityUSD)
	return m
}
