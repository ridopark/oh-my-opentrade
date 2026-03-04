package strategy_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validTOML is a minimal valid strategy DNA TOML string for testing.
const validTOML = `
[strategy]
id = "orb_break_retest"
version = 1
description = "Opening Range Breakout — Break & Retest"

[parameters]
orb_window_minutes = 30
min_rvol = 1.5
max_risk_bps = 200
limit_offset_bps = 5
stop_bps_below_low = 10
min_confidence = 0.65

[regime_filter]
allowed_regimes = ["TREND"]
min_regime_strength = 0.6
`

func writeTempTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test_strategy.toml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// ─── DNAManager.Load ────────────────────────────────────────────────────────

func TestDNAManager_Load_ParsesAllFields(t *testing.T) {
	path := writeTempTOML(t, validTOML)
	mgr := strategy.NewDNAManager()

	dna, err := mgr.Load(path)

	require.NoError(t, err)
	assert.Equal(t, "orb_break_retest", dna.ID)
	assert.Equal(t, 1, dna.Version)
	assert.Equal(t, "Opening Range Breakout — Break & Retest", dna.Description)
	assert.Equal(t, []string{"TREND"}, dna.RegimeFilter.AllowedRegimes)
	assert.InDelta(t, 0.6, dna.RegimeFilter.MinRegimeStrength, 1e-9)
}

func TestDNAManager_Load_ParsesParameters(t *testing.T) {
	path := writeTempTOML(t, validTOML)
	mgr := strategy.NewDNAManager()

	dna, err := mgr.Load(path)

	require.NoError(t, err)
	require.NotNil(t, dna.Parameters)
	// TOML integers are decoded as int64
	assert.EqualValues(t, 30, dna.Parameters["orb_window_minutes"])
	assert.InDelta(t, 1.5, dna.Parameters["min_rvol"], 1e-9)
}

func TestDNAManager_Load_ReturnsErrorForMissingFile(t *testing.T) {
	mgr := strategy.NewDNAManager()

	_, err := mgr.Load("/nonexistent/path/strategy.toml")

	assert.Error(t, err)
}

func TestDNAManager_Load_ReturnsErrorForInvalidTOML(t *testing.T) {
	path := writeTempTOML(t, "this is not valid toml ::::")
	mgr := strategy.NewDNAManager()

	_, err := mgr.Load(path)

	assert.Error(t, err)
}

func TestDNAManager_Load_ReturnsErrorForMissingID(t *testing.T) {
	noID := `
[strategy]
version = 1
description = "No ID strategy"
`
	path := writeTempTOML(t, noID)
	mgr := strategy.NewDNAManager()

	_, err := mgr.Load(path)

	assert.Error(t, err)
}

// ─── DNAManager.Get ──────────────────────────────────────────────────────────

func TestDNAManager_Get_ReturnsDNAAfterLoad(t *testing.T) {
	path := writeTempTOML(t, validTOML)
	mgr := strategy.NewDNAManager()

	loaded, err := mgr.Load(path)
	require.NoError(t, err)

	got, ok := mgr.Get("orb_break_retest")

	require.True(t, ok)
	assert.Equal(t, loaded.ID, got.ID)
	assert.Equal(t, loaded.Version, got.Version)
}

func TestDNAManager_Get_ReturnsFalseForUnknownID(t *testing.T) {
	mgr := strategy.NewDNAManager()

	_, ok := mgr.Get("unknown_strategy")

	assert.False(t, ok)
}

func TestDNAManager_Get_LatestVersionAfterReload(t *testing.T) {
	v1 := `
[strategy]
id = "my_strat"
version = 1
description = "v1"
`
	v2 := `
[strategy]
id = "my_strat"
version = 2
description = "v2"
`
	path := writeTempTOML(t, v1)
	mgr := strategy.NewDNAManager()

	_, err := mgr.Load(path)
	require.NoError(t, err)

	// Overwrite with v2 and reload
	require.NoError(t, os.WriteFile(path, []byte(v2), 0o644))
	_, err = mgr.Load(path)
	require.NoError(t, err)

	got, ok := mgr.Get("my_strat")
	require.True(t, ok)
	assert.Equal(t, 2, got.Version)
}

// ─── DNAManager.Watch ───────────────────────────────────────────────────────

func TestDNAManager_Watch_CallsOnChangeWhenFileModified(t *testing.T) {
	v1 := `
[strategy]
id = "watch_strat"
version = 1
description = "v1"
`
	v2 := `
[strategy]
id = "watch_strat"
version = 2
description = "v2"
`
	path := writeTempTOML(t, v1)
	mgr := strategy.NewDNAManager()

	// Pre-load so Watch knows the initial mtime
	_, err := mgr.Load(path)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	changed := make(chan *strategy.StrategyDNA, 1)
	go mgr.Watch(ctx, path, func(dna *strategy.StrategyDNA) {
		changed <- dna
	})

	// Give the watcher a moment to register, then modify the file
	time.Sleep(200 * time.Millisecond)
	// Use a slightly later mtime to guarantee detection
	time.Sleep(1 * time.Second)
	require.NoError(t, os.WriteFile(path, []byte(v2), 0o644))

	select {
	case dna := <-changed:
		assert.Equal(t, 2, dna.Version)
	case <-ctx.Done():
		t.Fatal("Watch did not call onChange within timeout")
	}
}

func TestDNAManager_Watch_StopsWhenContextCancelled(t *testing.T) {
	path := writeTempTOML(t, validTOML)
	mgr := strategy.NewDNAManager()
	_, err := mgr.Load(path)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Watch(ctx, path, func(_ *strategy.StrategyDNA) {})
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not stop after context cancellation")
	}
}
