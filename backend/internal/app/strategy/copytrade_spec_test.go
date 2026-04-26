package strategy_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadSpecFile_CopytradeV1_ParsesCleanly loads the shipped copytrade_v1
// TOML from disk and verifies the params the strategy needs are present in
// the parsed Params map. Guards against TOML structure regressions (e.g.
// partial_fractions accidentally nesting inside a sibling sub-table) that the
// parser would accept but the strategy's runtime parseCopytradeConfig would
// silently treat as empty.
func TestLoadSpecFile_CopytradeV1_ParsesCleanly(t *testing.T) {
	spec, err := strategy.LoadSpecFile("../../../../configs/strategies/copytrade_v1.toml")
	require.NoError(t, err)

	assert.Equal(t, "copytrade_v1", spec.ID.String())
	assert.True(t, spec.Lifecycle.PaperOnly)
	assert.Equal(t, []string{"__copytrade__"}, spec.Routing.Symbols)

	// Flat [params] keys the strategy/config/risk_sizer read directly.
	assert.Equal(t, true, spec.Params["skip_avg"])
	assert.Equal(t, true, spec.Params["trail_on_partial_enabled"])
	assert.Equal(t, 0.15, spec.Params["trail_giveback_pct"])
	assert.Equal(t, 0.33, spec.Params["default_stc_fraction"])
	assert.Equal(t, int64(500), spec.Params["risk_per_trade_bps"])
	assert.Equal(t, int64(10), spec.Params["max_positions"])

	// partial_fractions must live at top of [params] as a list of tables.
	// Nested-under-wrong-header bugs silently produce empty/missing entries
	// that parseCopytradeConfig would ignore at runtime.
	pf, ok := spec.Params["partial_fractions"]
	require.True(t, ok, "partial_fractions must be a top-level [params] key")
	list, ok := pf.([]map[string]any)
	require.True(t, ok, "partial_fractions type = %T", pf)
	require.NotEmpty(t, list)
	// Spot-check: the "half out" entry must be present with fraction 0.5.
	var foundHalf bool
	for _, m := range list {
		if m["keyword"] == "half out" && m["fraction"] == 0.5 {
			foundHalf = true
		}
	}
	assert.True(t, foundHalf, "half out keyword missing or wrong fraction")
}
