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
		// Cheapest / most critical first — atomic int32 load blocks everything in HALTED.
		{Name: "kill_switch"},
		// Risk gates — read positions, cheap math.
		{Name: "portfolio_heat_guard"},
		{Name: "sector_exposure_guard"},
		{Name: "directional_bias_guard"},
		// Original gates in their existing order.
		{Name: "short_direction"},
		{Name: "exposure_guard"},
		{Name: "portfolio_guard"},
		{Name: "risk_engine"},
		{Name: "slippage_guard"},
		{Name: "trading_window"},
		{Name: "spread_guard"},
		{Name: "buying_power_guard"},
		// Compliance / event gates — hit DB or external state, run last.
		{Name: "pdt_guard"},
		{Name: "reg_t_guard"},
		{Name: "earnings_blackout_gate"},
		{Name: "macro_event_gate"},
	}
}
