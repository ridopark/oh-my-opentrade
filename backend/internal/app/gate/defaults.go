package gate

// DefaultMonitorGateConfigs returns the gate chain matching the current
// hardcoded behavior in service.go. This is used as the fallback when
// no [gate_chain.monitor] section exists in the TOML config.
func DefaultMonitorGateConfigs() []GateConfig {
	return []GateConfig{
		{Name: "dna_approval"},
		{Name: "vix"},
		{Name: "regime"},
		{Name: "htf_bias"},
		{Name: "min_atr_pct"},
	}
}

// DefaultExecutionGateConfigs returns the gate chain matching the current
// hardcoded guard order in execution/service.go handleIntent(). This is
// used as the fallback when no [gate_chain.execution] section exists.
func DefaultExecutionGateConfigs() []GateConfig {
	return []GateConfig{
		{Name: "short_direction"},
		{Name: "exposure_guard"},
		{Name: "portfolio_guard"},
		{Name: "risk_engine"},
		{Name: "slippage_guard"},
		{Name: "trading_window"},
		{Name: "spread_guard"},
		{Name: "buying_power_guard"},
	}
}
