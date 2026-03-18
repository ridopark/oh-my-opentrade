package strategy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSpecFile_V1_AppliesDefaultsAndConvertsVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[strategy]
id = "orb_break_retest"
version = 2
description = "x"

[parameters]
foo = 1

[regime_filter]
allowed_regimes = ["TREND"]
min_regime_strength = 0.6
`), 0o644))

	spec, err := strategy.LoadSpecFile(path)
	require.NoError(t, err)

	assert.Equal(t, 1, spec.SchemaVersion)
	assert.Equal(t, "orb_break_retest", spec.ID.String())
	assert.Equal(t, "2.0.0", spec.Version.String())
	assert.Equal(t, "x", spec.Description)
	assert.Equal(t, domstrategy.LifecycleLiveActive, spec.Lifecycle.State)
	assert.False(t, spec.Lifecycle.PaperOnly)
	assert.Empty(t, spec.Routing.Symbols)
	assert.Equal(t, []string{"1m"}, spec.Routing.Timeframes)
	assert.Equal(t, 100, spec.Routing.Priority)
	assert.Equal(t, domstrategy.ConflictPriorityWins, spec.Routing.ConflictPolicy)
	assert.False(t, spec.Routing.ExclusivePerSymbol)
	assert.Equal(t, int64(1), spec.Params["foo"])
	assert.Equal(t, []string{"TREND"}, spec.Params["regime_filter.allowed_regimes"])
	assert.Equal(t, 0.6, spec.Params["regime_filter.min_regime_strength"])
	assert.Empty(t, spec.Hooks)
}

func TestLoadSpecFile_V2_ParsesAllSectionsAndMergesRegimeFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
schema_version = 2

[strategy]
id = "orb_break_retest"
version = "1.2.3"
name = "ORB"
description = "d"
author = "system"

[lifecycle]
state = "PaperActive"
paper_only = true

[routing]
symbols = ["AAPL"]
timeframes = ["5m"]
priority = 7
conflict_policy = "merge"
exclusive_per_symbol = true

[params]
foo = "bar"

[regime_filter]
enabled = true
min_atr_pct = 0.8

[hooks]
signals = { engine = "builtin", name = "orb_v1" }
`), 0o644))

	spec, err := strategy.LoadSpecFile(path)
	require.NoError(t, err)

	assert.Equal(t, 2, spec.SchemaVersion)
	assert.Equal(t, "orb_break_retest", spec.ID.String())
	assert.Equal(t, "1.2.3", spec.Version.String())
	assert.Equal(t, "ORB", spec.Name)
	assert.Equal(t, "system", spec.Author)
	assert.Equal(t, domstrategy.LifecyclePaperActive, spec.Lifecycle.State)
	assert.True(t, spec.Lifecycle.PaperOnly)
	assert.Equal(t, []string{"AAPL"}, spec.Routing.Symbols)
	assert.Equal(t, []string{"5m"}, spec.Routing.Timeframes)
	assert.Equal(t, 7, spec.Routing.Priority)
	assert.Equal(t, domstrategy.ConflictMerge, spec.Routing.ConflictPolicy)
	assert.True(t, spec.Routing.ExclusivePerSymbol)
	assert.Equal(t, "bar", spec.Params["foo"])
	assert.Equal(t, true, spec.Params["regime_filter.enabled"])
	assert.Equal(t, 0.8, spec.Params["regime_filter.min_atr_pct"])
	require.Contains(t, spec.Hooks, "signals")
	assert.Equal(t, domstrategy.HookEngineBuiltin, spec.Hooks["signals"].Engine)
	assert.Equal(t, "orb_v1", spec.Hooks["signals"].Name)
}

func TestLoadSpecFile_ErrorsOnInvalidSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	require.NoError(t, os.WriteFile(path, []byte(`schema_version = 3`), 0o644))

	_, err := strategy.LoadSpecFile(path)
	require.Error(t, err)
}

func TestLoadSpecFile_ErrorsWhenTrailingStopGteMaxLoss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
schema_version = 2

[strategy]
id = "test_strategy"
version = "1.0.0"
name = "Test"
description = "x"
author = "system"

[lifecycle]
state = "PaperActive"
paper_only = true

[routing]
symbols = ["AAPL"]
timeframes = ["1m"]

[[exit_rules]]
type = "TRAILING_STOP"
[exit_rules.params]
pct = 0.03

[[exit_rules]]
type = "MAX_LOSS"
[exit_rules.params]
pct = 0.02
`), 0o644))

	_, err := strategy.LoadSpecFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TRAILING_STOP")
	assert.Contains(t, err.Error(), "MAX_LOSS")
}

func TestLoadSpecFile_PassesWhenTrailingStopLtMaxLoss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
schema_version = 2

[strategy]
id = "test_strategy"
version = "1.0.0"
name = "Test"
description = "x"
author = "system"

[lifecycle]
state = "PaperActive"
paper_only = true

[routing]
symbols = ["AAPL"]
timeframes = ["1m"]

[[exit_rules]]
type = "TRAILING_STOP"
[exit_rules.params]
pct = 0.01

[[exit_rules]]
type = "MAX_LOSS"
[exit_rules.params]
pct = 0.025
`), 0o644))

	_, err := strategy.LoadSpecFile(path)
	require.NoError(t, err)
}

func TestLoadSpecFile_ErrorsOnInvalidID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[strategy]
id = "BAD-ID"
version = 1
`), 0o644))

	_, err := strategy.LoadSpecFile(path)
	require.Error(t, err)
}

func TestLoadSpecFile_V2_AppliesSymbolOverridesToParamsAndExitRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
schema_version = 2

[strategy]
id = "avwap_v2"
version = "2.0.0"

[lifecycle]
state = "PaperActive"

[routing]
symbols = ["AAPL", "NVDA"]
timeframes = ["5m"]

[params]
stop_bps = 50

[[exit_rules]]
type = "VOLATILITY_STOP"
[exit_rules.params]
atr_multiplier = 3.0

[[exit_rules]]
type = "MAX_LOSS"
[exit_rules.params]
pct = 0.03

[symbol_overrides.NVDA]
atr_multiplier = 5.0
stop_bps = 30
`), 0o644))

	spec, err := strategy.LoadSpecFile(path)
	require.NoError(t, err)

	nvdaParams := spec.ParamsForSymbol("NVDA")
	aaplParams := spec.ParamsForSymbol("AAPL")
	assert.Equal(t, int64(30), nvdaParams["stop_bps"])
	assert.Equal(t, int64(50), aaplParams["stop_bps"])

	nvdaRules := spec.ExitRulesForSymbol("NVDA")
	require.Len(t, nvdaRules, 2)
	assert.Equal(t, domain.ExitRuleVolatilityStop, nvdaRules[0].Type)
	assert.Equal(t, 5.0, nvdaRules[0].Param("atr_multiplier", 0))
	assert.Equal(t, 0.03, nvdaRules[1].Param("pct", 0))

	defaults := spec.ExitRulesForSymbol("AAPL")
	assert.Equal(t, 3.0, defaults[0].Param("atr_multiplier", 0))
	assert.Equal(t, 3.0, spec.ExitRules[0].Param("atr_multiplier", 0))
}
