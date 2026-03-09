// Package metrics provides Prometheus instrumentation for the omo-core trading system.
//
// A single [prometheus.Registry] owns every metric; subsystem constructors live
// in sibling files (http.go, orders.go, strategy.go, ws.go, risk.go, pnl.go, bars.go).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics holds every Prometheus collector used by omo-core, grouped by
// subsystem. Create via [New]; the zero value is not usable.
type Metrics struct {
	Reg *prometheus.Registry

	BuildInfo   prometheus.Gauge
	RuntimeInfo *prometheus.GaugeVec

	HTTP     HTTPMetrics
	Orders   OrderMetrics
	Strategy StrategyMetrics
	WS       WSMetrics
	Risk     RiskMetrics
	PnL      PnLMetrics
	Bars     BarMetrics
}

// New creates a Metrics instance with all collectors registered on a fresh
// [prometheus.Registry]. The build/runtime info gauges are set immediately.
func New(version, commit, branch string, strategyV2 bool) *Metrics {
	reg := prometheus.NewRegistry()

	// Optionally include Go runtime / process collectors.
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "omo_build_info",
		Help: "Build metadata (version, commit, branch). Always 1.",
		ConstLabels: prometheus.Labels{
			"version": version,
			"commit":  commit,
			"branch":  branch,
		},
	})
	buildInfo.Set(1)
	reg.MustRegister(buildInfo)

	v2Label := "false"
	if strategyV2 {
		v2Label = "true"
	}
	runtimeInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "omo_runtime_info",
		Help: "Runtime feature flags. Always 1.",
	}, []string{"strategy_v2"})
	runtimeInfo.WithLabelValues(v2Label).Set(1)
	reg.MustRegister(runtimeInfo)

	m := &Metrics{
		Reg:         reg,
		BuildInfo:   buildInfo,
		RuntimeInfo: runtimeInfo,
	}

	m.HTTP = newHTTPMetrics(reg)
	m.Orders = newOrderMetrics(reg)
	m.Strategy = newStrategyMetrics(reg)
	m.WS = newWSMetrics(reg)
	m.Risk = newRiskMetrics(reg)
	m.PnL = newPnLMetrics(reg)
	m.Bars = newBarMetrics(reg)

	return m
}
