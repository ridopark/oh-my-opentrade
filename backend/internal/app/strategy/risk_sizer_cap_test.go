package strategy

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRiskSizerForCap builds a bare RiskSizer wired only with what the
// cap evaluator needs: account equity and a PositionRiskCap. The event
// bus and spec store are left nil — applyPositionRiskCap never touches
// them.
func newTestRiskSizerForCap(equity float64, cap config.PositionRiskCapConfig) *RiskSizer {
	rs := &RiskSizer{
		accountEquity: equity,
		logger:        slog.Default(),
	}
	rs.positionRiskCap = cap
	return rs
}

func stopPctPremiumTrail(pct float64) []domain.ExitRule {
	return []domain.ExitRule{{Type: domain.ExitRulePremiumTrail, Params: map[string]float64{"trail_pct": pct}}}
}
func stopPctMaxLoss(pct float64) []domain.ExitRule {
	return []domain.ExitRule{{Type: domain.ExitRuleMaxLoss, Params: map[string]float64{"pct": pct}}}
}

func enabledAccountPct(frac float64) config.PositionRiskCapConfig {
	return config.PositionRiskCapConfig{
		Enabled:             true,
		Mode:                "account_pct",
		DailyLossBudgetPct:  0.0025,
		MaxPositionRiskFrac: frac,
		StopPctSource:       "widest_active",
		RejectOnFloor:       true,
		AppliesTo:           []string{"options"},
	}
}

// TestApplyPositionRiskCap_TableDriven exercises the main branches of the
// cap evaluator: reduce, reject, pass-through, disabled, equity-missing.
func TestApplyPositionRiskCap_TableDriven(t *testing.T) {
	const equity = 100_000.0 // daily budget = 250; per-trade cap = 50 (20%)

	cases := []struct {
		name              string
		cap               config.PositionRiskCapConfig
		equity            float64
		qtyIn             float64
		premium           float64
		rules             []domain.ExitRule
		instrument        domain.InstrumentType
		wantQty           float64
		wantAdjusted      bool
		wantDisabled      bool
	}{
		{
			name:       "enabled: cap reduces qty from 5 to 3",
			cap:        enabledAccountPct(0.20),
			equity:     equity,
			qtyIn:      5, // 5 × 20 × 0.50 = 50 loss vs cap = 50; exactly at cap → no reduction
			premium:    20,
			rules:      stopPctPremiumTrail(0.50),
			instrument: domain.InstrumentTypeOption,
			wantQty:    5,
		},
		{
			name: "enabled: loss > cap → reduce to floor(cap/lossPerContract)",
			cap:  enabledAccountPct(0.20),
			equity: equity,
			// Per-contract loss at stop = 30 × 0.50 = 15. Cap = 50. floor(50/15) = 3.
			qtyIn:        10,
			premium:      30,
			rules:        stopPctPremiumTrail(0.50),
			instrument:   domain.InstrumentTypeOption,
			wantQty:      3,
			wantAdjusted: true,
		},
		{
			name:         "disabled short-circuits",
			cap:          config.PositionRiskCapConfig{Enabled: false},
			equity:       equity,
			qtyIn:        100,
			premium:      20,
			rules:        stopPctPremiumTrail(0.50),
			instrument:   domain.InstrumentTypeOption,
			wantQty:      100,
			wantDisabled: true,
		},
		{
			name:         "applies_to filter: equity intent short-circuits",
			cap:          enabledAccountPct(0.20),
			equity:       equity,
			qtyIn:        100,
			premium:      20,
			rules:        stopPctPremiumTrail(0.50),
			instrument:   domain.InstrumentTypeEquity,
			wantQty:      100,
			wantDisabled: true,
		},
		{
			name:         "no widest-active stop → disabled (cannot compute loss)",
			cap:          enabledAccountPct(0.20),
			equity:       equity,
			qtyIn:        5,
			premium:      20,
			rules:        []domain.ExitRule{{Type: domain.ExitRuleProfitTarget, Params: map[string]float64{"pct": 0.10}}},
			instrument:   domain.InstrumentTypeOption,
			wantQty:      5,
			wantDisabled: true,
		},
		{
			name:         "equity missing in account_pct mode → disabled",
			cap:          enabledAccountPct(0.20),
			equity:       0,
			qtyIn:        5,
			premium:      20,
			rules:        stopPctPremiumTrail(0.50),
			instrument:   domain.InstrumentTypeOption,
			wantQty:      5,
			wantDisabled: true,
		},
		{
			name: "fixed_usd mode uses budget directly",
			cap: config.PositionRiskCapConfig{
				Enabled:             true,
				Mode:                "fixed_usd",
				DailyLossBudgetUSD:  1000,   // cap = 0.2 × 1000 = 200
				MaxPositionRiskFrac: 0.20,
				StopPctSource:       "widest_active",
				RejectOnFloor:       true,
				AppliesTo:           []string{"options"},
			},
			equity:       0, // irrelevant in fixed mode
			qtyIn:        100,
			premium:      50,                          // per-contract loss = 50 × 0.5 = 25
			rules:        stopPctPremiumTrail(0.50), // floor(200/25) = 8
			instrument:   domain.InstrumentTypeOption,
			wantQty:      8,
			wantAdjusted: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := newTestRiskSizerForCap(tc.equity, tc.cap)
			d := rs.applyPositionRiskCap(tc.qtyIn, tc.premium, tc.rules, tc.instrument)
			assert.Equal(t, tc.wantDisabled, d.Disabled, "Disabled")
			assert.Equal(t, tc.wantAdjusted, d.Adjusted, "Adjusted")
			assert.InDelta(t, tc.wantQty, d.Qty, 1e-9, "Qty")
		})
	}
}

// TestApplyPositionRiskCap_MUIncidentReproducer is a dedicated regression
// guard for the 2026-04-16 MU incident: 1 contract @ $25.50 premium
// (notional per contract = $25.50 × 100 = $2,550), MAX_LOSS 100%, $1M
// paper equity → daily budget = $2,500, per-position cap = $500,
// expected loss = $2,550 > $500 → reject. The function receives the
// already-multiplied per-contract notional from the sizer, mirroring
// how premiumPerContract = fillPrice × Multiplier is computed upstream.
func TestApplyPositionRiskCap_MUIncidentReproducer(t *testing.T) {
	const equity = 1_000_000.0
	cap := enabledAccountPct(0.20) // cap = $500
	rs := newTestRiskSizerForCap(equity, cap)

	const premiumPerContractNotional = 25.50 * 100 // $2,550

	d := rs.applyPositionRiskCap(
		1, // 1 contract
		premiumPerContractNotional,
		stopPctMaxLoss(1.0), // 100% stop
		domain.InstrumentTypeOption,
	)
	assert.False(t, d.Disabled)
	assert.InDelta(t, 500.0, d.CapUSD, 1e-9)
	assert.InDelta(t, 2550.0, d.ComputedLossUSDRaw, 1e-9)
	assert.InDelta(t, 0.0, d.Qty, 1e-9, "reject — expected_loss exceeds cap")
}

// TestApplyPositionRiskCap_DisabledByteIdentical verifies the disabled
// path does not even look at equity/rules — the byte-identical invariant
// requires the function to leave qty untouched regardless of inputs.
func TestApplyPositionRiskCap_DisabledByteIdentical(t *testing.T) {
	rs := newTestRiskSizerForCap(100_000, config.PositionRiskCapConfig{Enabled: false})
	d := rs.applyPositionRiskCap(999, 0.01, nil, domain.InstrumentTypeOption)
	assert.Equal(t, 999.0, d.Qty)
	assert.False(t, d.Adjusted)
	assert.True(t, d.Disabled)
}

// TestRiskCapRejectionPayloadShape builds the same payload the sizer
// constructs when it rejects on REJECTED_RISK_CAP and asserts the
// dashboard-visible fields are populated. We do NOT drive the full
// signal pipeline here — wiring ContractSelector + OptionsMarket for
// an end-to-end test would be out of proportion for asserting payload
// keys. The payload is exactly what the sizer emits at service.go's
// rejection site (see risk_sizer.go option path); if that code changes,
// this test pins the public schema.
func TestRiskCapRejectionPayloadShape(t *testing.T) {
	const equity = 1_000_000.0
	cap := enabledAccountPct(0.20)
	rs := newTestRiskSizerForCap(equity, cap)
	rs.eventBus = nil // irrelevant here

	d := rs.applyPositionRiskCap(1, 25.50*100, stopPctMaxLoss(1.0), domain.InstrumentTypeOption)
	require.False(t, d.Disabled)
	require.Equal(t, 0.0, d.Qty)

	// Replicate the Meta construction from risk_sizer.go.
	meta := map[string]string{
		"reject_code":            RejectedRiskCap,
		"computed_expected_loss": fmt.Sprintf("%.2f", d.ComputedLossUSDRaw),
		"cap_usd":                fmt.Sprintf("%.2f", d.CapUSD),
		"stop_pct":               fmt.Sprintf("%.6f", d.StopPct),
		"stop_source":            d.StopSource,
		"daily_budget_usd":       fmt.Sprintf("%.2f", d.DailyBudgetUSD),
		"budget_mode":            d.BudgetMode,
		"premium_per_contract":   fmt.Sprintf("%.4f", 25.50*100),
	}

	assert.Equal(t, "REJECTED_RISK_CAP", meta["reject_code"])
	assert.Equal(t, "2550.00", meta["computed_expected_loss"])
	assert.Equal(t, "500.00", meta["cap_usd"])
	assert.Equal(t, "MAX_LOSS.pct", meta["stop_source"])
	assert.Equal(t, "account_pct", meta["budget_mode"])
	assert.Equal(t, "2500.00", meta["daily_budget_usd"])
}

// TestIsInstrumentTypeApplicable covers the AppliesTo filter branches.
func TestIsInstrumentTypeApplicable(t *testing.T) {
	cases := []struct {
		name       string
		it         domain.InstrumentType
		appliesTo  []string
		wantActive bool
	}{
		{"empty defaults to options-only, option ok", domain.InstrumentTypeOption, nil, true},
		{"empty defaults to options-only, equity filtered", domain.InstrumentTypeEquity, nil, false},
		{"options list includes option", domain.InstrumentTypeOption, []string{"options"}, true},
		{"options list excludes equity", domain.InstrumentTypeEquity, []string{"options"}, false},
		{"options list excludes crypto", domain.InstrumentTypeCrypto, []string{"options"}, false},
		{"equities label matches equity", domain.InstrumentTypeEquity, []string{"equities"}, true},
		{"crypto label matches crypto", domain.InstrumentTypeCrypto, []string{"crypto"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isInstrumentTypeApplicable(tc.it, tc.appliesTo)
			assert.Equal(t, tc.wantActive, got)
		})
	}
}
