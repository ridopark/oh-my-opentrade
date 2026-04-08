package positionmonitor

import (
	"math"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustETLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	return loc
}

func newTestMonitoredPosition(t *testing.T, entryPrice float64, entryTime time.Time, assetClass domain.AssetClass) *domain.MonitoredPosition {
	t.Helper()
	pos, err := domain.NewMonitoredPosition(
		domain.Symbol("AAPL"),
		entryPrice,
		entryTime,
		"test-strategy",
		assetClass,
		nil,
		"tenant-1",
		domain.EnvModePaper,
		1,
	)
	require.NoError(t, err)
	return &pos
}

func TestEvaluate_TrailingStop(t *testing.T) {
	etLoc := mustETLocation(t)
	entryTime := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)

	triggerRule, err := domain.NewExitRule(domain.ExitRuleTrailingStop, map[string]float64{"pct": 0.02})
	require.NoError(t, err)

	noTriggerRule, err := domain.NewExitRule(domain.ExitRuleTrailingStop, map[string]float64{"pct": 0.02})
	require.NoError(t, err)

	zeroRule, err := domain.NewExitRule(domain.ExitRuleTrailingStop, map[string]float64{"pct": 0})
	require.NoError(t, err)

	tests := []struct {
		name        string
		rule        domain.ExitRule
		pos         *domain.MonitoredPosition
		current     float64
		want        bool
		wantReason  bool
		reasonMatch string
	}{
		{
			name: "triggers when drawdown 3% >= 2% threshold",
			rule: triggerRule,
			pos: func() *domain.MonitoredPosition {
				p := newTestMonitoredPosition(t, 98, entryTime, domain.AssetClassEquity)
				p.HighWaterMark = 100
				return p
			}(),
			current:     97,
			want:        true,
			wantReason:  true,
			reasonMatch: "trailing_stop",
		},
		{
			name: "does not trigger when drawdown 1% < 2% threshold",
			rule: noTriggerRule,
			pos: func() *domain.MonitoredPosition {
				p := newTestMonitoredPosition(t, 98, entryTime, domain.AssetClassEquity)
				p.HighWaterMark = 100
				return p
			}(),
			current:     99,
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
		{
			name: "returns false when pct param is zero",
			rule: zeroRule,
			pos: func() *domain.MonitoredPosition {
				p := newTestMonitoredPosition(t, 98, entryTime, domain.AssetClassEquity)
				p.HighWaterMark = 100
				return p
			}(),
			current:     97,
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, tc.pos, tc.current, entryTime, EvalContext{})
			assert.Equal(t, tc.want, triggered)
			if tc.wantReason {
				assert.NotEmpty(t, reason)
				assert.Contains(t, reason, tc.reasonMatch)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestEvaluate_ProfitTarget(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

	triggerRule, err := domain.NewExitRule(domain.ExitRuleProfitTarget, map[string]float64{"pct": 0.03})
	require.NoError(t, err)
	zeroRule, err := domain.NewExitRule(domain.ExitRuleProfitTarget, map[string]float64{"pct": 0})
	require.NoError(t, err)

	tests := []struct {
		name        string
		rule        domain.ExitRule
		current     float64
		want        bool
		wantReason  bool
		reasonMatch string
	}{
		{
			name:        "triggers when 5% profit >= 3% target",
			rule:        triggerRule,
			current:     105,
			want:        true,
			wantReason:  true,
			reasonMatch: "profit_target",
		},
		{
			name:        "does not trigger when 1% profit < 3% target",
			rule:        triggerRule,
			current:     101,
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
		{
			name:        "returns false when pct param is zero",
			rule:        zeroRule,
			current:     105,
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, pos, tc.current, now, EvalContext{})
			assert.Equal(t, tc.want, triggered)
			if tc.wantReason {
				assert.NotEmpty(t, reason)
				assert.Contains(t, reason, tc.reasonMatch)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestEvaluate_TimeExit(t *testing.T) {
	etLoc := mustETLocation(t)
	pos := newTestMonitoredPosition(t, 100, time.Date(2026, 3, 6, 9, 45, 0, 0, etLoc), domain.AssetClassEquity)

	rule, err := domain.NewExitRule(domain.ExitRuleTimeExit, map[string]float64{"hour": 15, "minute": 45})
	require.NoError(t, err)
	zeroRule, err := domain.NewExitRule(domain.ExitRuleTimeExit, map[string]float64{})
	require.NoError(t, err)

	tests := []struct {
		name        string
		rule        domain.ExitRule
		now         time.Time
		want        bool
		wantReason  bool
		reasonMatch string
	}{
		{
			name:        "triggers when current ET time is at/after threshold",
			rule:        rule,
			now:         time.Date(2026, 3, 6, 15, 45, 0, 0, etLoc),
			want:        true,
			wantReason:  true,
			reasonMatch: "time_exit",
		},
		{
			name:        "does not trigger when current ET time is before threshold",
			rule:        rule,
			now:         time.Date(2026, 3, 6, 15, 44, 0, 0, etLoc),
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
		{
			name:        "returns false when hour/minute params are missing",
			rule:        zeroRule,
			now:         time.Date(2026, 3, 6, 15, 45, 0, 0, etLoc),
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, pos, 0, tc.now, EvalContext{})
			assert.Equal(t, tc.want, triggered)
			if tc.wantReason {
				assert.NotEmpty(t, reason)
				assert.Contains(t, reason, tc.reasonMatch)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestEvaluate_EODFlatten(t *testing.T) {
	etLoc := mustETLocation(t)
	pos := newTestMonitoredPosition(t, 100, time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc), domain.AssetClassEquity)

	rule, err := domain.NewExitRule(domain.ExitRuleEODFlatten, map[string]float64{"minutes_before_close": 5})
	require.NoError(t, err)
	zeroRule, err := domain.NewExitRule(domain.ExitRuleEODFlatten, map[string]float64{"minutes_before_close": 0})
	require.NoError(t, err)

	tests := []struct {
		name        string
		rule        domain.ExitRule
		now         time.Time
		want        bool
		wantReason  bool
		reasonMatch string
	}{
		{
			name:        "triggers within 5 minutes of 4:00 PM ET close",
			rule:        rule,
			now:         time.Date(2026, 3, 6, 15, 56, 0, 0, etLoc),
			want:        true,
			wantReason:  true,
			reasonMatch: "eod_flatten",
		},
		{
			name:        "does not trigger when more than 5 minutes before close",
			rule:        rule,
			now:         time.Date(2026, 3, 6, 15, 50, 0, 0, etLoc),
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
		{
			name:        "returns false when minutes_before_close is zero",
			rule:        zeroRule,
			now:         time.Date(2026, 3, 6, 15, 56, 0, 0, etLoc),
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, pos, 0, tc.now, EvalContext{})
			assert.Equal(t, tc.want, triggered)
			if tc.wantReason {
				assert.NotEmpty(t, reason)
				assert.Contains(t, reason, tc.reasonMatch)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestEvaluate_MaxHoldingTime(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, etLoc)

	rule, err := domain.NewExitRule(domain.ExitRuleMaxHoldingTime, map[string]float64{"minutes": 60})
	require.NoError(t, err)
	zeroRule, err := domain.NewExitRule(domain.ExitRuleMaxHoldingTime, map[string]float64{"minutes": 0})
	require.NoError(t, err)

	tests := []struct {
		name        string
		rule        domain.ExitRule
		pos         *domain.MonitoredPosition
		now         time.Time
		want        bool
		wantReason  bool
		reasonMatch string
	}{
		{
			name:        "triggers when held 120 min with 60 min limit",
			rule:        rule,
			pos:         newTestMonitoredPosition(t, 100, now.Add(-120*time.Minute), domain.AssetClassEquity),
			now:         now,
			want:        true,
			wantReason:  true,
			reasonMatch: "max_holding_time",
		},
		{
			name:        "does not trigger when held 30 min with 60 min limit",
			rule:        rule,
			pos:         newTestMonitoredPosition(t, 100, now.Add(-30*time.Minute), domain.AssetClassEquity),
			now:         now,
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
		{
			name:        "returns false when minutes param is zero",
			rule:        zeroRule,
			pos:         newTestMonitoredPosition(t, 100, now.Add(-120*time.Minute), domain.AssetClassEquity),
			now:         now,
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, tc.pos, 0, tc.now, EvalContext{})
			assert.Equal(t, tc.want, triggered)
			if tc.wantReason {
				assert.NotEmpty(t, reason)
				assert.Contains(t, reason, tc.reasonMatch)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestEvaluate_MaxLoss(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

	rule, err := domain.NewExitRule(domain.ExitRuleMaxLoss, map[string]float64{"pct": 0.02})
	require.NoError(t, err)
	zeroRule, err := domain.NewExitRule(domain.ExitRuleMaxLoss, map[string]float64{"pct": 0})
	require.NoError(t, err)

	tests := []struct {
		name        string
		rule        domain.ExitRule
		current     float64
		want        bool
		wantReason  bool
		reasonMatch string
	}{
		{
			name:        "triggers when -3% loss exceeds 2% limit",
			rule:        rule,
			current:     97,
			want:        true,
			wantReason:  true,
			reasonMatch: "max_loss",
		},
		{
			name:        "does not trigger when -1% loss is within 2% limit",
			rule:        rule,
			current:     99,
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
		{
			name:        "returns false when pct param is zero",
			rule:        zeroRule,
			current:     97,
			want:        false,
			wantReason:  false,
			reasonMatch: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, pos, tc.current, now, EvalContext{})
			assert.Equal(t, tc.want, triggered)
			if tc.wantReason {
				assert.NotEmpty(t, reason)
				assert.Contains(t, reason, tc.reasonMatch)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestEvaluate_VolatilityStop(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

	rule, err := domain.NewExitRule(domain.ExitRuleVolatilityStop, map[string]float64{"atr_multiplier": 1.5})
	require.NoError(t, err)
	zeroRule, err := domain.NewExitRule(domain.ExitRuleVolatilityStop, map[string]float64{"atr_multiplier": 0})
	require.NoError(t, err)

	tests := []struct {
		name        string
		rule        domain.ExitRule
		current     float64
		ctx         EvalContext
		want        bool
		reasonMatch string
	}{
		{
			name:        "triggers when price below entry minus 1.5*ATR",
			rule:        rule,
			current:     95.0,
			ctx:         EvalContext{ATR: 3.0},
			want:        true,
			reasonMatch: "volatility_stop",
		},
		{
			name:    "does not trigger when price above stop level",
			rule:    rule,
			current: 99.0,
			ctx:     EvalContext{ATR: 1.0},
			want:    false,
		},
		{
			name:    "does not trigger when ATR is zero (warmup)",
			rule:    rule,
			current: 90.0,
			ctx:     EvalContext{ATR: 0},
			want:    false,
		},
		{
			name:    "does not trigger when atr_multiplier is zero",
			rule:    zeroRule,
			current: 90.0,
			ctx:     EvalContext{ATR: 5.0},
			want:    false,
		},
		{
			name:        "triggers at exact stop price boundary",
			rule:        rule,
			current:     95.5,
			ctx:         EvalContext{ATR: 3.0},
			want:        true,
			reasonMatch: "volatility_stop",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, pos, tc.current, now, tc.ctx)
			assert.Equal(t, tc.want, triggered)
			if tc.reasonMatch != "" {
				assert.Contains(t, reason, tc.reasonMatch)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestEvaluate_SDTarget(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

	rule := domain.ExitRule{
		Type:   domain.ExitRuleSDTarget,
		Params: map[string]float64{"sd_level": 2.0},
	}
	zeroRule := domain.ExitRule{
		Type:   domain.ExitRuleSDTarget,
		Params: map[string]float64{"sd_level": 0},
	}

	// VWAP=150, SD=1.2 → +2.0 SD band = 150 + 2*1.2 = 152.4
	sdBands := map[float64]float64{
		1.0: 151.2,
		2.0: 152.4,
		2.5: 153.0,
	}

	tests := []struct {
		name        string
		rule        domain.ExitRule
		current     float64
		ctx         EvalContext
		want        bool
		reasonMatch string
	}{
		{
			name:        "triggers when price reaches +2.0 SD band",
			rule:        rule,
			current:     152.5,
			ctx:         EvalContext{VWAPValue: 150.0, SDBands: sdBands},
			want:        true,
			reasonMatch: "sd_target",
		},
		{
			name:    "does not trigger when price below SD band",
			rule:    rule,
			current: 151.0,
			ctx:     EvalContext{VWAPValue: 150.0, SDBands: sdBands},
			want:    false,
		},
		{
			name:        "triggers at exact band price",
			rule:        rule,
			current:     152.4,
			ctx:         EvalContext{VWAPValue: 150.0, SDBands: sdBands},
			want:        true,
			reasonMatch: "sd_target",
		},
		{
			name:    "does not trigger when SDBands is nil (warmup)",
			rule:    rule,
			current: 200.0,
			ctx:     EvalContext{VWAPValue: 150.0},
			want:    false,
		},
		{
			name:    "does not trigger when sd_level is zero",
			rule:    zeroRule,
			current: 200.0,
			ctx:     EvalContext{VWAPValue: 150.0, SDBands: sdBands},
			want:    false,
		},
		{
			name:    "does not trigger when requested level not in SDBands",
			rule:    domain.ExitRule{Type: domain.ExitRuleSDTarget, Params: map[string]float64{"sd_level": 3.0}},
			current: 200.0,
			ctx:     EvalContext{VWAPValue: 150.0, SDBands: sdBands},
			want:    false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, pos, tc.current, now, tc.ctx)
			assert.Equal(t, tc.want, triggered)
			if tc.reasonMatch != "" {
				assert.Contains(t, reason, tc.reasonMatch)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestUpdateStepStopState(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)

	// VWAP=100, SD=2 → bands: +1.0=102, +2.0=104, +3.0=106
	sdBands := map[float64]float64{
		1.0: 102.0,
		2.0: 104.0,
		3.0: 106.0,
	}
	ctx := EvalContext{VWAPValue: 100.0, SDBands: sdBands}

	t.Run("crosses +1.0 SD sets stop to entry with buffer (breakeven)", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		UpdateStepStopState(pos, 102.5, ctx, now, 0.0)
		assert.Equal(t, 1.0, pos.CustomState["highest_sd_crossed"])
		assert.InEpsilon(t, 99.85, pos.CustomState["step_stop_level"], 0.001) // entry * 0.9985
	})

	t.Run("crosses +2.0 SD sets stop to +1.0 SD band", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		UpdateStepStopState(pos, 104.5, ctx, now, 0.0)
		assert.Equal(t, 2.0, pos.CustomState["highest_sd_crossed"])
		assert.Equal(t, 102.0, pos.CustomState["step_stop_level"])
	})

	t.Run("crosses +3.0 SD sets stop to +2.0 SD band", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		UpdateStepStopState(pos, 106.5, ctx, now, 0.0)
		assert.Equal(t, 3.0, pos.CustomState["highest_sd_crossed"])
		assert.Equal(t, 104.0, pos.CustomState["step_stop_level"])
	})

	t.Run("stop only ratchets up, never down", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		// First: cross +2.0 SD → stop at +1.0 SD (102)
		UpdateStepStopState(pos, 104.5, ctx, now, 0.0)
		assert.Equal(t, 102.0, pos.CustomState["step_stop_level"])

		// Price drops back below +2.0 SD — stop must NOT decrease
		UpdateStepStopState(pos, 101.0, ctx, now, 0.0)
		assert.Equal(t, 102.0, pos.CustomState["step_stop_level"])
		assert.Equal(t, 2.0, pos.CustomState["highest_sd_crossed"])
	})

	t.Run("no-op when SDBands is nil", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		UpdateStepStopState(pos, 200.0, EvalContext{}, now, 0.0)
		assert.Equal(t, 0.0, pos.CustomState["step_stop_level"])
	})

	t.Run("progressive ratcheting across ticks", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

		// Tick 1: price at 102.5 → crosses +1.0 SD → stop = entry with buffer (99.85)
		UpdateStepStopState(pos, 102.5, ctx, now, 0.0)
		assert.Equal(t, 1.0, pos.CustomState["highest_sd_crossed"])
		assert.InEpsilon(t, 99.85, pos.CustomState["step_stop_level"], 0.001)

		// Tick 2: price at 104.5 → crosses +2.0 SD → stop = +1.0 SD (102)
		UpdateStepStopState(pos, 104.5, ctx, now, 0.0)
		assert.Equal(t, 2.0, pos.CustomState["highest_sd_crossed"])
		assert.Equal(t, 102.0, pos.CustomState["step_stop_level"])

		// Tick 3: price at 106.5 → crosses +3.0 SD → stop = +2.0 SD (104)
		UpdateStepStopState(pos, 106.5, ctx, now, 0.0)
		assert.Equal(t, 3.0, pos.CustomState["highest_sd_crossed"])
		assert.Equal(t, 104.0, pos.CustomState["step_stop_level"])
	})

	t.Run("min_hold_bars suppresses ratchet while within hold period", func(t *testing.T) {
		entryTime := now.Add(-2 * time.Minute)
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		UpdateStepStopState(pos, 102.5, ctx, now, 3.0)
		assert.Equal(t, 0.0, pos.CustomState["highest_sd_crossed"], "should not ratchet within hold period")
		assert.Equal(t, 0.0, pos.CustomState["step_stop_level"])
	})

	t.Run("min_hold_bars allows ratchet once hold period elapsed", func(t *testing.T) {
		entryTime := now.Add(-4 * time.Minute)
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		UpdateStepStopState(pos, 102.5, ctx, now, 3.0)
		assert.Equal(t, 1.0, pos.CustomState["highest_sd_crossed"], "should ratchet after hold period")
		assert.InEpsilon(t, 99.85, pos.CustomState["step_stop_level"], 0.001)
	})
}

func TestEvaluate_StepStop(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)

	rule := domain.ExitRule{Type: domain.ExitRuleStepStop, Params: map[string]float64{}}
	ctx := EvalContext{VWAPValue: 100.0}

	t.Run("triggers when price below step stop level", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		pos.CustomState["step_stop_level"] = 102.0
		pos.CustomState["highest_sd_crossed"] = 2.0

		triggered, reason := Evaluate(rule, pos, 101.5, now, ctx)
		assert.True(t, triggered)
		assert.Contains(t, reason, "step_stop")
	})

	t.Run("does not trigger when price above step stop level", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		pos.CustomState["step_stop_level"] = 102.0
		pos.CustomState["highest_sd_crossed"] = 2.0

		triggered, reason := Evaluate(rule, pos, 103.0, now, ctx)
		assert.False(t, triggered)
		assert.Empty(t, reason)
	})

	t.Run("does not trigger when step stop level is zero (not yet activated)", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

		triggered, reason := Evaluate(rule, pos, 50.0, now, ctx)
		assert.False(t, triggered)
		assert.Empty(t, reason)
	})

	t.Run("triggers at exact stop level boundary", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		pos.CustomState["step_stop_level"] = 102.0
		pos.CustomState["highest_sd_crossed"] = 2.0

		triggered, reason := Evaluate(rule, pos, 102.0, now, ctx)
		assert.True(t, triggered)
		assert.Contains(t, reason, "step_stop")
	})
}

func TestEvaluate_StagnationExit(t *testing.T) {
	etLoc := mustETLocation(t)
	rule := domain.ExitRule{
		Type:   domain.ExitRuleStagnationExit,
		Params: map[string]float64{"minutes": 30, "sd_threshold": 1.0},
	}

	sdBands := map[float64]float64{1.0: 102.0, 2.0: 104.0}
	ctx := EvalContext{VWAPValue: 100.0, SDBands: sdBands}

	t.Run("triggers after stagnation period without reaching SD band", func(t *testing.T) {
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-35*time.Minute), domain.AssetClassEquity)

		triggered, reason := Evaluate(rule, pos, 101.0, now, ctx)
		assert.True(t, triggered)
		assert.Contains(t, reason, "stagnation_exit")
	})

	t.Run("does not trigger before stagnation period expires", func(t *testing.T) {
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-20*time.Minute), domain.AssetClassEquity)

		triggered, _ := Evaluate(rule, pos, 101.0, now, ctx)
		assert.False(t, triggered)
	})

	t.Run("does not trigger if price reached SD band", func(t *testing.T) {
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-35*time.Minute), domain.AssetClassEquity)

		triggered, _ := Evaluate(rule, pos, 102.5, now, ctx)
		assert.False(t, triggered)
	})

	t.Run("disabled when step-stop has activated", func(t *testing.T) {
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-35*time.Minute), domain.AssetClassEquity)
		pos.CustomState["highest_sd_crossed"] = 1.0

		triggered, _ := Evaluate(rule, pos, 101.0, now, ctx)
		assert.False(t, triggered)
	})

	t.Run("does not trigger when minutes param is zero", func(t *testing.T) {
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-60*time.Minute), domain.AssetClassEquity)
		zeroRule := domain.ExitRule{Type: domain.ExitRuleStagnationExit, Params: map[string]float64{"minutes": 0}}

		triggered, _ := Evaluate(zeroRule, pos, 101.0, now, ctx)
		assert.False(t, triggered)
	})

	t.Run("works without SDBands (always triggers after timeout)", func(t *testing.T) {
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-35*time.Minute), domain.AssetClassEquity)

		triggered, reason := Evaluate(rule, pos, 101.0, now, EvalContext{VWAPValue: 100.0})
		assert.True(t, triggered)
		assert.Contains(t, reason, "stagnation_exit")
	})

	t.Run("profit gate skips exit when position is profitable", func(t *testing.T) {
		gatedRule := domain.ExitRule{
			Type:   domain.ExitRuleStagnationExit,
			Params: map[string]float64{"minutes": 30, "sd_threshold": 1.0, "profit_gate_pct": 0.005},
		}
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-35*time.Minute), domain.AssetClassEquity)

		// +1.34% P&L exceeds 0.5% gate — should NOT trigger
		triggered, _ := Evaluate(gatedRule, pos, 101.34, now, ctx)
		assert.False(t, triggered)
	})

	t.Run("profit gate still exits when position is losing", func(t *testing.T) {
		gatedRule := domain.ExitRule{
			Type:   domain.ExitRuleStagnationExit,
			Params: map[string]float64{"minutes": 30, "sd_threshold": 1.0, "profit_gate_pct": 0.005},
		}
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-35*time.Minute), domain.AssetClassEquity)

		// -0.5% P&L below gate — should trigger stagnation exit
		triggered, reason := Evaluate(gatedRule, pos, 99.5, now, ctx)
		assert.True(t, triggered)
		assert.Contains(t, reason, "stagnation_exit")
	})

	t.Run("profit gate still exits when profit below threshold", func(t *testing.T) {
		gatedRule := domain.ExitRule{
			Type:   domain.ExitRuleStagnationExit,
			Params: map[string]float64{"minutes": 30, "sd_threshold": 1.0, "profit_gate_pct": 0.005},
		}
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-35*time.Minute), domain.AssetClassEquity)

		// +0.3% P&L below 0.5% gate — should still trigger
		triggered, reason := Evaluate(gatedRule, pos, 100.3, now, ctx)
		assert.True(t, triggered)
		assert.Contains(t, reason, "stagnation_exit")
	})

	t.Run("profit gate disabled when param is zero (backward compat)", func(t *testing.T) {
		now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, now.Add(-35*time.Minute), domain.AssetClassEquity)

		// Original rule has no profit_gate_pct — +1.34% should still trigger
		triggered, reason := Evaluate(rule, pos, 101.34, now, ctx)
		assert.True(t, triggered)
		assert.Contains(t, reason, "stagnation_exit")
	})
}

func TestUpdateBreakevenStopState(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)

	t.Run("activates when P&L crosses activation threshold", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		// Price at 100.40 → +0.4% P&L, activation at 0.3%
		UpdateBreakevenStopState(pos, 100.40, 0.003, 0.0005)
		assert.Equal(t, 1.0, pos.CustomState["breakeven_activated"])
		// Stop level = entry * (1 + buffer) = 100 * 1.0005 = 100.05
		assert.InDelta(t, 100.05, pos.CustomState["breakeven_stop_level"], 0.001)
	})

	t.Run("does not activate when P&L below threshold", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		// Price at 100.20 → +0.2% P&L, activation at 0.3%
		UpdateBreakevenStopState(pos, 100.20, 0.003, 0.0005)
		assert.Equal(t, 0.0, pos.CustomState["breakeven_activated"])
		assert.Equal(t, 0.0, pos.CustomState["breakeven_stop_level"])
	})

	t.Run("stop level locks after activation", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		// First tick: activate at +0.4%
		UpdateBreakevenStopState(pos, 100.40, 0.003, 0.0005)
		assert.Equal(t, 1.0, pos.CustomState["breakeven_activated"])
		stopLevel := pos.CustomState["breakeven_stop_level"]

		// Second tick: price drops — stop must NOT change
		UpdateBreakevenStopState(pos, 99.50, 0.003, 0.0005)
		assert.Equal(t, stopLevel, pos.CustomState["breakeven_stop_level"])
		assert.Equal(t, 1.0, pos.CustomState["breakeven_activated"])
	})

	t.Run("no-op when activation_pct is zero", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		UpdateBreakevenStopState(pos, 105.0, 0, 0.0005)
		assert.Equal(t, 0.0, pos.CustomState["breakeven_activated"])
	})

	t.Run("no-op when CustomState is nil", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		pos.CustomState = nil
		UpdateBreakevenStopState(pos, 105.0, 0.003, 0.0005)
		assert.Nil(t, pos.CustomState)
	})

	t.Run("activates at exact threshold boundary", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 1000, now.Add(-10*time.Minute), domain.AssetClassEquity)
		// entry=1000, price=1003 → P&L = 3/1000 = 0.003 exactly (no float rounding)
		UpdateBreakevenStopState(pos, 1003, 0.003, 0.0005)
		assert.Equal(t, 1.0, pos.CustomState["breakeven_activated"])
	})
}

func TestEvaluate_BreakevenStop(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)

	rule := domain.ExitRule{Type: domain.ExitRuleBreakevenStop, Params: map[string]float64{}}

	t.Run("triggers when price below breakeven stop level", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		pos.CustomState["breakeven_activated"] = 1
		pos.CustomState["breakeven_stop_level"] = 100.05

		triggered, reason := Evaluate(rule, pos, 100.00, now, EvalContext{})
		assert.True(t, triggered)
		assert.Contains(t, reason, "breakeven_stop")
	})

	t.Run("does not trigger when price above stop level", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		pos.CustomState["breakeven_activated"] = 1
		pos.CustomState["breakeven_stop_level"] = 100.05

		triggered, reason := Evaluate(rule, pos, 100.10, now, EvalContext{})
		assert.False(t, triggered)
		assert.Empty(t, reason)
	})

	t.Run("does not trigger when not activated", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

		triggered, reason := Evaluate(rule, pos, 50.0, now, EvalContext{})
		assert.False(t, triggered)
		assert.Empty(t, reason)
	})

	t.Run("triggers at exact stop level boundary", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		pos.CustomState["breakeven_activated"] = 1
		pos.CustomState["breakeven_stop_level"] = 100.05

		triggered, reason := Evaluate(rule, pos, 100.05, now, EvalContext{})
		assert.True(t, triggered)
		assert.Contains(t, reason, "breakeven_stop")
	})

	t.Run("does not trigger when CustomState is nil", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		pos.CustomState = nil

		triggered, reason := Evaluate(rule, pos, 50.0, now, EvalContext{})
		assert.False(t, triggered)
		assert.Empty(t, reason)
	})
}

func TestEvaluate_UnknownRuleType(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

	rule := domain.ExitRule{Type: domain.ExitRuleType("SOMETHING_ELSE"), Params: map[string]float64{"pct": 0.01}}
	triggered, reason := Evaluate(rule, pos, 101, now, EvalContext{})
	assert.False(t, triggered)
	assert.Empty(t, reason)
}

// --- helpers for premium-based exit tests ---

func newOptionPosition(t *testing.T, entryUnderlying float64, entryTime time.Time, premium, delta float64) *domain.MonitoredPosition {
	t.Helper()
	pos := newTestMonitoredPosition(t, entryUnderlying, entryTime, domain.AssetClassEquity)
	pos.InstrumentType = domain.InstrumentTypeOption
	pos.OptionRight = "CALL"
	pos.CustomState["option_premium"] = premium
	pos.CustomState["delta_at_entry"] = delta
	return pos
}

func TestEvaluate_PremiumStop(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)

	tests := []struct {
		name       string
		rule       domain.ExitRule
		pos        *domain.MonitoredPosition
		price      float64
		wantFired  bool
		wantSubstr string
	}{
		{
			name: "triggers when premium drops 50% (threshold 40%)",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumStop, Params: map[string]float64{"threshold": 0.40}},
			pos: func() *domain.MonitoredPosition {
				// entry premium = 5.00, delta = 0.50, entry underlying = 150
				// at underlying 140: est = 5.00 + 0.50*(140-150) - 5.00*0.005 = 5 - 5 - 0.025 = -0.025 -> 0
				return newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50)
			}(),
			price:      140,
			wantFired:  true,
			wantSubstr: "premium_stop",
		},
		{
			name: "does not trigger when premium drop is below threshold",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumStop, Params: map[string]float64{"threshold": 0.40}},
			pos: func() *domain.MonitoredPosition {
				// entry premium = 5.00, delta = 0.50, entry underlying = 150
				// at 149: est = 5.00 + 0.50*(149-150) - 0.025 = 5 - 0.5 - 0.025 = 4.475
				// loss = (5.00 - 4.475) / 5.00 = 0.105 = 10.5% < 40%
				return newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50)
			}(),
			price:     149,
			wantFired: false,
		},
		{
			name: "does not trigger for non-option position",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumStop, Params: map[string]float64{"threshold": 0.40}},
			pos: func() *domain.MonitoredPosition {
				return newTestMonitoredPosition(t, 150, now.Add(-30*time.Minute), domain.AssetClassEquity)
			}(),
			price:     140,
			wantFired: false,
		},
		{
			name: "does not trigger with missing CustomState",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumStop, Params: map[string]float64{"threshold": 0.40}},
			pos: func() *domain.MonitoredPosition {
				p := newTestMonitoredPosition(t, 150, now.Add(-30*time.Minute), domain.AssetClassEquity)
				p.InstrumentType = domain.InstrumentTypeOption
				// no option_premium or delta_at_entry in CustomState
				return p
			}(),
			price:     140,
			wantFired: false,
		},
		{
			name:      "does not trigger with zero threshold",
			rule:      domain.ExitRule{Type: domain.ExitRulePremiumStop, Params: map[string]float64{"threshold": 0}},
			pos:       newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50),
			price:     140,
			wantFired: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, tc.pos, tc.price, now, EvalContext{})
			assert.Equal(t, tc.wantFired, triggered, "triggered mismatch")
			if tc.wantSubstr != "" {
				assert.Contains(t, reason, tc.wantSubstr)
			}
		})
	}
}

func TestEvaluate_PremiumTrail(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)

	tests := []struct {
		name       string
		rule       domain.ExitRule
		pos        *domain.MonitoredPosition
		price      float64
		wantFired  bool
		wantSubstr string
	}{
		{
			name: "triggers when premium drops 35% from HWM after activation",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTrail, Params: map[string]float64{
				"trail_pct": 0.30, "min_activation": 0.20,
			}},
			pos: func() *domain.MonitoredPosition {
				// entry premium = 5.00, delta = 0.50, entry underlying = 150
				// HWM was at 7.00 (premium rose 40% from entry — above 20% activation)
				// current underlying = 148 -> est = 5.00 + 0.50*(148-150) - 0.025 = 3.975
				// drawdown from HWM = (7.00 - 3.975) / 7.00 = 0.432 = 43.2% >= 30%
				p := newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50)
				p.CustomState["premium_hwm"] = 7.00
				return p
			}(),
			price:      148,
			wantFired:  true,
			wantSubstr: "premium_trail",
		},
		{
			name: "does not trigger before activation threshold",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTrail, Params: map[string]float64{
				"trail_pct": 0.30, "min_activation": 0.20,
			}},
			pos: func() *domain.MonitoredPosition {
				// HWM = 5.50 -> gain = (5.50-5.00)/5.00 = 10% < 20% activation
				p := newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50)
				p.CustomState["premium_hwm"] = 5.50
				return p
			}(),
			price:     148,
			wantFired: false,
		},
		{
			name: "does not trigger when drawdown is below trail_pct",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTrail, Params: map[string]float64{
				"trail_pct": 0.30, "min_activation": 0.20,
			}},
			pos: func() *domain.MonitoredPosition {
				// HWM = 7.00, underlying at 152 -> est = 5.00 + 0.50*(152-150) - 0.025 = 5.975
				// drawdown = (7.00-5.975)/7.00 = 14.6% < 30%
				p := newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50)
				p.CustomState["premium_hwm"] = 7.00
				return p
			}(),
			price:     152,
			wantFired: false,
		},
		{
			name: "does not trigger for non-option position",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTrail, Params: map[string]float64{
				"trail_pct": 0.30, "min_activation": 0.20,
			}},
			pos: func() *domain.MonitoredPosition {
				return newTestMonitoredPosition(t, 150, now.Add(-30*time.Minute), domain.AssetClassEquity)
			}(),
			price:     140,
			wantFired: false,
		},
		{
			name: "does not trigger with no premium_hwm",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTrail, Params: map[string]float64{
				"trail_pct": 0.30, "min_activation": 0.20,
			}},
			pos:       newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50),
			price:     148,
			wantFired: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, tc.pos, tc.price, now, EvalContext{})
			assert.Equal(t, tc.wantFired, triggered, "triggered mismatch")
			if tc.wantSubstr != "" {
				assert.Contains(t, reason, tc.wantSubstr)
			}
		})
	}
}

func TestEvaluate_PremiumTarget(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)

	tests := []struct {
		name       string
		rule       domain.ExitRule
		pos        *domain.MonitoredPosition
		price      float64
		wantFired  bool
		wantSubstr string
	}{
		{
			name: "triggers when premium rises 75% from entry (target 70%)",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTarget, Params: map[string]float64{"target_pct": 0.70}},
			pos: func() *domain.MonitoredPosition {
				// entry premium = 5.00, delta = 0.50, entry underlying = 150
				// at 168: est = 5.00 + 0.50*(168-150) - 0.025 = 5 + 9 - 0.025 = 13.975
				// gain = (13.975 - 5.00) / 5.00 = 1.795 = 179.5% >= 70%
				return newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50)
			}(),
			price:      168,
			wantFired:  true,
			wantSubstr: "premium_target",
		},
		{
			name: "does not trigger when premium gain is below target",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTarget, Params: map[string]float64{"target_pct": 0.70}},
			pos: func() *domain.MonitoredPosition {
				// at 152: est = 5.00 + 0.50*(152-150) - 0.025 = 5 + 1 - 0.025 = 5.975
				// gain = (5.975-5.00)/5.00 = 19.5% < 70%
				return newOptionPosition(t, 150, now.Add(-30*time.Minute), 5.00, 0.50)
			}(),
			price:     152,
			wantFired: false,
		},
		{
			name: "does not trigger for non-option position",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTarget, Params: map[string]float64{"target_pct": 0.70}},
			pos: func() *domain.MonitoredPosition {
				return newTestMonitoredPosition(t, 150, now.Add(-30*time.Minute), domain.AssetClassEquity)
			}(),
			price:     200,
			wantFired: false,
		},
		{
			name: "does not trigger with missing CustomState",
			rule: domain.ExitRule{Type: domain.ExitRulePremiumTarget, Params: map[string]float64{"target_pct": 0.70}},
			pos: func() *domain.MonitoredPosition {
				p := newTestMonitoredPosition(t, 150, now.Add(-30*time.Minute), domain.AssetClassEquity)
				p.InstrumentType = domain.InstrumentTypeOption
				return p
			}(),
			price:     200,
			wantFired: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			triggered, reason := Evaluate(tc.rule, tc.pos, tc.price, now, EvalContext{})
			assert.Equal(t, tc.wantFired, triggered, "triggered mismatch")
			if tc.wantSubstr != "" {
				assert.Contains(t, reason, tc.wantSubstr)
			}
		})
	}
}

func TestEstimatedPremium(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)

	t.Run("delta-linear fallback when BSM fields missing", func(t *testing.T) {
		pos := newOptionPosition(t, 150, now, 5.00, 0.50)
		// No strike/expiry/iv/is_call in CustomState — falls back to delta-linear
		// at underlying 152: est = 5.00 + 0.50*(152-150) - 5.00*0.005 = 5 + 1 - 0.025 = 5.975
		est := pos.EstimatedPremium(152, now)
		assert.InDelta(t, 5.975, est, 0.001)
	})

	t.Run("non-option returns zero", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 150, now, domain.AssetClassEquity)
		est := pos.EstimatedPremium(152, now)
		assert.Equal(t, 0.0, est)
	})

	t.Run("missing CustomState returns zero", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 150, now, domain.AssetClassEquity)
		pos.InstrumentType = domain.InstrumentTypeOption
		est := pos.EstimatedPremium(152, now)
		assert.Equal(t, 0.0, est)
	})

	t.Run("negative premium clamped to zero", func(t *testing.T) {
		pos := newOptionPosition(t, 150, now, 2.00, 0.50)
		// at underlying 140: est = 2.00 + 0.50*(140-150) - 2.00*0.008 = 2 - 5 - 0.016 = -3.016 -> 0
		est := pos.EstimatedPremium(140, now)
		assert.Equal(t, 0.0, est)
	})

	t.Run("spread tiers delta-linear", func(t *testing.T) {
		// >= $10 -> 0.3%
		pos10 := newOptionPosition(t, 150, now, 12.00, 0.50)
		est10 := pos10.EstimatedPremium(150, now)
		// est = 12.00 + 0 - 12*0.003 = 12 - 0.036 = 11.964
		assert.InDelta(t, 11.964, est10, 0.001)

		// < $2 -> 1.5%
		pos1 := newOptionPosition(t, 150, now, 1.50, 0.30)
		est1 := pos1.EstimatedPremium(150, now)
		// est = 1.50 + 0 - 1.50*0.015 = 1.50 - 0.0225 = 1.4775
		assert.InDelta(t, 1.4775, est1, 0.001)
	})

	t.Run("BSM call — ATM with 7 DTE", func(t *testing.T) {
		// Set up an ATM call with BSM fields populated
		expiry := time.Date(2026, 3, 13, 16, 0, 0, 0, etLoc)
		pos := newOptionPosition(t, 150, now, 4.00, 0.50)
		pos.CustomState["strike"] = 150.0
		pos.CustomState["expiry_unix"] = float64(expiry.Unix())
		pos.CustomState["iv_at_entry"] = 0.30
		pos.CustomState["is_call"] = 1.0

		// BSM(s=152, k=150, t≈7d/365.25, r=0.045, sigma=0.30, call=true)
		// With underlying at 152 (2 points ITM), BSM should give ~3.5-4.5
		est := pos.EstimatedPremium(152, now)
		assert.Greater(t, est, 2.0, "BSM premium should be positive for ITM call")
		assert.Less(t, est, 8.0, "BSM premium should be reasonable")

		// Compare with delta-linear: 5.00 + 0.50*2 - spread = 5.975
		// BSM captures gamma curvature, so result differs from delta-linear
		deltaLinear := 5.00 + 0.50*(152.0-150.0) - 5.00*0.005
		assert.True(t, math.Abs(est-deltaLinear) > 0.01, "BSM should differ from delta-linear, got est=%.4f deltaLinear=%.4f", est, deltaLinear)
	})

	t.Run("BSM put — ATM with 7 DTE", func(t *testing.T) {
		expiry := time.Date(2026, 3, 13, 16, 0, 0, 0, etLoc)
		pos := newOptionPosition(t, 150, now, 4.00, -0.50)
		pos.OptionRight = "PUT"
		pos.CustomState["strike"] = 150.0
		pos.CustomState["expiry_unix"] = float64(expiry.Unix())
		pos.CustomState["iv_at_entry"] = 0.30
		pos.CustomState["is_call"] = 0.0

		// Underlying drops to 148 — put should gain value
		est := pos.EstimatedPremium(148, now)
		assert.Greater(t, est, 2.0, "ITM put should have positive premium")

		// Underlying rises to 155 — put should lose value
		estOTM := pos.EstimatedPremium(155, now)
		assert.Less(t, estOTM, est, "OTM put premium should be less than ITM")
	})

	t.Run("BSM near-expiry graceful handling", func(t *testing.T) {
		// Expiry is today — DTE near zero
		expiry := time.Date(2026, 3, 6, 16, 0, 0, 0, etLoc)
		pos := newOptionPosition(t, 150, now, 4.00, 0.50)
		pos.CustomState["strike"] = 150.0
		pos.CustomState["expiry_unix"] = float64(expiry.Unix())
		pos.CustomState["iv_at_entry"] = 0.30
		pos.CustomState["is_call"] = 1.0

		// ITM: underlying at 153, should return mostly intrinsic value (3.0) minus spread
		est := pos.EstimatedPremium(153, now)
		assert.Greater(t, est, 2.0, "near-expiry ITM should have positive premium")
		assert.Less(t, est, 5.0, "near-expiry premium bounded by intrinsic + small time value")

		// OTM: underlying at 148, should be near zero
		estOTM := pos.EstimatedPremium(148, now)
		assert.Less(t, estOTM, 1.0, "near-expiry OTM call should have minimal premium")
	})

	t.Run("BSM expired option returns intrinsic", func(t *testing.T) {
		// Expiry already passed
		expiry := time.Date(2026, 3, 5, 16, 0, 0, 0, etLoc)
		pos := newOptionPosition(t, 150, now, 4.00, 0.50)
		pos.CustomState["strike"] = 150.0
		pos.CustomState["expiry_unix"] = float64(expiry.Unix())
		pos.CustomState["iv_at_entry"] = 0.30
		pos.CustomState["is_call"] = 1.0

		// ITM call: intrinsic = 155 - 150 = 5.0, minus spread
		est := pos.EstimatedPremium(155, now)
		spreadCost := 4.00 * 0.008 // entry premium 4.00 is in [2, 5) tier
		assert.InDelta(t, 5.0-spreadCost, est, 0.01)

		// OTM: underlying at 148, intrinsic = 0
		estOTM := pos.EstimatedPremium(148, now)
		assert.Equal(t, 0.0, estOTM)
	})
}

// ---------------------------------------------------------------------------
// SwingStop — uses BarLow/BarHigh from EvalContext (Shannon methodology)
// ---------------------------------------------------------------------------

func TestEvaluate_SwingStop_LongUsesBarLow(t *testing.T) {
	etLoc := mustETLocation(t)
	entryTime := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)

	// Use buffer_bps=50 so building bars don't trigger the stop.
	rule := domain.ExitRule{
		Type:   domain.ExitRuleSwingStop,
		Params: map[string]float64{"lookback": 3, "buffer_bps": 50, "min_bars": 1},
	}

	pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)

	// Feed 3 bars with ascending bar lows (higher lows).
	// Close prices are high so they never trigger. Bar lows: 94, 95, 96.
	// Stop ratchets UP as swing lows rise: bar1→94-0.47=93.53, bar2→94, bar3→94
	// (min of ring is still 94, so stop stays at 93.53).
	// After all 3: ring has 94,95,96. Min=94. Buffer=94*50/10000=0.47. Stop=93.53.
	bars := []struct{ close, low float64 }{
		{99, 94}, {99, 95}, {99, 96},
	}
	for i, b := range bars {
		now := entryTime.Add(time.Duration(i+1) * time.Minute)
		ctx := EvalContext{BarDuration: time.Minute, BarLow: b.low}
		Evaluate(rule, pos, b.close, now, ctx)
	}

	// Verify stop is based on bar lows. If it had used close (99), stop = 99-0.495=98.505.
	// But with bar lows, stop ≈ 93.53. Price 95 is above 93.53 → no trigger.
	// (If close-based, 95 < 98.505 would trigger — this proves bar lows are used.)
	ctx := EvalContext{BarDuration: time.Minute, BarLow: 95}
	now := entryTime.Add(4 * time.Minute)
	triggered, _ := Evaluate(rule, pos, 95.0, now, ctx)
	assert.False(t, triggered, "price 95 > stop ~93.53 (bar-low-based) → no trigger")

	// Price at 93 < stop 93.53 → trigger
	ctx2 := EvalContext{BarDuration: time.Minute, BarLow: 93}
	triggered, reason := Evaluate(rule, pos, 93.0, now, ctx2)
	assert.True(t, triggered)
	assert.Contains(t, reason, "swing_stop")
}

func TestEvaluate_SwingStop_ShortUsesBarHigh(t *testing.T) {
	etLoc := mustETLocation(t)
	entryTime := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)

	rule := domain.ExitRule{
		Type:   domain.ExitRuleSwingStop,
		Params: map[string]float64{"lookback": 3, "buffer_bps": 0, "min_bars": 1},
	}

	pos := newTestMonitoredPosition(t, 110, entryTime, domain.AssetClassEquity)
	pos.Side = "SELL"

	// Feed 3 bars. Close prices are 101,102,103 but bar highs are 102,104,105.
	// Ring buffer should store bar highs: 102, 104, 105. Swing high = 105.
	bars := []struct{ close, high float64 }{
		{101, 102}, {102, 104}, {103, 105},
	}
	for i, b := range bars {
		now := entryTime.Add(time.Duration(i+1) * time.Minute)
		ctx := EvalContext{BarDuration: time.Minute, BarHigh: b.high}
		Evaluate(rule, pos, b.close, now, ctx)
	}

	// Stop should be at swing high=105 (not 103).
	// Price at 105.5 >= stop=105 → trigger
	ctx := EvalContext{BarDuration: time.Minute, BarHigh: 106}
	now := entryTime.Add(4 * time.Minute)
	triggered, reason := Evaluate(rule, pos, 105.5, now, ctx)
	assert.True(t, triggered)
	assert.Contains(t, reason, "swing_stop")
}

func TestEvaluate_SwingStop_FallsBackToCloseWhenBarLowZero(t *testing.T) {
	etLoc := mustETLocation(t)
	entryTime := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)

	rule := domain.ExitRule{
		Type:   domain.ExitRuleSwingStop,
		Params: map[string]float64{"lookback": 3, "buffer_bps": 50, "min_bars": 1},
	}

	pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)

	// BarLow=0 → falls back to close price
	for i := 1; i <= 3; i++ {
		now := entryTime.Add(time.Duration(i) * time.Minute)
		ctx := EvalContext{BarDuration: time.Minute, BarLow: 0}
		Evaluate(rule, pos, 97.0+float64(i), now, ctx)
	}

	// Should have used close prices (98, 99, 100). Min=98. Buffer=98*50/10000=0.49.
	// Stop = 98 - 0.49 = 97.51
	now := entryTime.Add(4 * time.Minute)
	ctx := EvalContext{BarDuration: time.Minute, BarLow: 0}
	triggered, _ := Evaluate(rule, pos, 97.6, now, ctx)
	assert.False(t, triggered, "97.6 > stop~97.51 → no trigger")
}

func TestEvaluate_SwingStop_MinBarsGuard(t *testing.T) {
	etLoc := mustETLocation(t)
	entryTime := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)

	rule := domain.ExitRule{
		Type:   domain.ExitRuleSwingStop,
		Params: map[string]float64{"lookback": 3, "buffer_bps": 0, "min_bars": 10},
	}

	pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
	ctx := EvalContext{BarDuration: time.Minute, BarLow: 50}

	// Only 2 minutes elapsed < min_bars=10
	now := entryTime.Add(2 * time.Minute)
	triggered, _ := Evaluate(rule, pos, 50.0, now, ctx)
	assert.False(t, triggered, "should not trigger before min_bars elapsed")
}

func TestEvaluate_SwingStop_ATRBuffer(t *testing.T) {
	etLoc := mustETLocation(t)
	entryTime := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)

	rule := domain.ExitRule{
		Type:   domain.ExitRuleSwingStop,
		Params: map[string]float64{"lookback": 3, "atr_buffer_mult": 0.5, "min_bars": 1},
	}

	// Build ascending bar lows: 96, 97, 98. Min=96. Buffer=ATR*0.5=1. Stop=95.
	for i := 1; i <= 3; i++ {
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		_ = pos
	}
	pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
	for i := 1; i <= 3; i++ {
		ctx := EvalContext{BarDuration: time.Minute, ATR: 2.0, BarLow: 95.0 + float64(i)}
		Evaluate(rule, pos, 98.0+float64(i), entryTime.Add(time.Duration(i)*time.Minute), ctx)
	}

	// Price at 94.5 < stop=95 → trigger
	ctx := EvalContext{BarDuration: time.Minute, ATR: 2.0, BarLow: 94}
	triggered, reason := Evaluate(rule, pos, 94.5, entryTime.Add(4*time.Minute), ctx)
	assert.True(t, triggered)
	assert.Contains(t, reason, "swing_stop")

	// Price at 95.5 > stop=95 → no trigger
	pos2 := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
	for i := 1; i <= 3; i++ {
		ctx2 := EvalContext{BarDuration: time.Minute, ATR: 2.0, BarLow: 95.0 + float64(i)}
		Evaluate(rule, pos2, 98.0+float64(i), entryTime.Add(time.Duration(i)*time.Minute), ctx2)
	}
	ctx3 := EvalContext{BarDuration: time.Minute, ATR: 2.0, BarLow: 95.5}
	triggered2, _ := Evaluate(rule, pos2, 95.5, entryTime.Add(4*time.Minute), ctx3)
	assert.False(t, triggered2)
}

// ---------------------------------------------------------------------------
// DTEFloor
// ---------------------------------------------------------------------------

func TestEvaluate_DTEFloor(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)

	rule := domain.ExitRule{Type: domain.ExitRuleDTEFloor, Params: map[string]float64{"dte": 7}}

	t.Run("triggers when DTE below floor", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-24*time.Hour), domain.AssetClassEquity)
		pos.InstrumentType = domain.InstrumentTypeOption
		pos.OptionExpiry = now.Add(5 * 24 * time.Hour)
		triggered, reason := Evaluate(rule, pos, 100, now, EvalContext{})
		assert.True(t, triggered)
		assert.Contains(t, reason, "dte_floor")
	})

	t.Run("does not trigger above floor", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-24*time.Hour), domain.AssetClassEquity)
		pos.InstrumentType = domain.InstrumentTypeOption
		pos.OptionExpiry = now.Add(14 * 24 * time.Hour) // 14 calendar days = ~10 trading days > floor 7
		triggered, _ := Evaluate(rule, pos, 100, now, EvalContext{})
		assert.False(t, triggered)
	})

	t.Run("ignores equity positions", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-24*time.Hour), domain.AssetClassEquity)
		triggered, _ := Evaluate(rule, pos, 100, now, EvalContext{})
		assert.False(t, triggered)
	})

	t.Run("zero dte param returns false", func(t *testing.T) {
		zeroRule := domain.ExitRule{Type: domain.ExitRuleDTEFloor, Params: map[string]float64{"dte": 0}}
		pos := newTestMonitoredPosition(t, 100, now.Add(-24*time.Hour), domain.AssetClassEquity)
		pos.InstrumentType = domain.InstrumentTypeOption
		pos.OptionExpiry = now.Add(1 * time.Hour)
		triggered, _ := Evaluate(zeroRule, pos, 100, now, EvalContext{})
		assert.False(t, triggered)
	})
}

// ---------------------------------------------------------------------------
// ExpiryWatch — uses trading days (excludes weekends/holidays)
// ---------------------------------------------------------------------------

func TestEvaluate_ExpiryWatch_TradingDays(t *testing.T) {
	etLoc := mustETLocation(t)

	rule := domain.ExitRule{
		Type:   domain.ExitRuleExpiryWatch,
		Params: map[string]float64{"pct_elapsed": 0.5},
	}

	t.Run("triggers when enough trading days elapsed", func(t *testing.T) {
		// Entry Monday March 2, Expiry Friday March 13 = 10 trading days
		entryTime := time.Date(2026, 3, 2, 10, 0, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		pos.InstrumentType = domain.InstrumentTypeOption
		pos.OptionExpiry = time.Date(2026, 3, 13, 16, 0, 0, 0, etLoc)

		// Monday March 9 = 5 trading days elapsed = 50% → triggers at threshold
		now := time.Date(2026, 3, 9, 12, 0, 0, 0, etLoc)
		triggered, reason := Evaluate(rule, pos, 100, now, EvalContext{})
		assert.True(t, triggered)
		assert.Contains(t, reason, "expiry_watch")
	})

	t.Run("does not trigger before threshold", func(t *testing.T) {
		entryTime := time.Date(2026, 3, 2, 10, 0, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		pos.InstrumentType = domain.InstrumentTypeOption
		pos.OptionExpiry = time.Date(2026, 3, 13, 16, 0, 0, 0, etLoc)

		// Thursday March 5 = 3 trading days elapsed = 30% < 50%
		now := time.Date(2026, 3, 5, 12, 0, 0, 0, etLoc)
		triggered, _ := Evaluate(rule, pos, 100, now, EvalContext{})
		assert.False(t, triggered)
	})

	t.Run("weekend does not inflate ratio", func(t *testing.T) {
		// Entry Friday March 6, Expiry Friday March 20 = 10 trading days
		entryTime := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		pos.InstrumentType = domain.InstrumentTypeOption
		pos.OptionExpiry = time.Date(2026, 3, 20, 16, 0, 0, 0, etLoc)

		// Monday March 9 = 1 trading day elapsed (Friday doesn't count, weekend skipped)
		now := time.Date(2026, 3, 9, 12, 0, 0, 0, etLoc)
		triggered, _ := Evaluate(rule, pos, 100, now, EvalContext{})
		// 1/10 = 10% < 50% → should NOT trigger
		// Old clock-based code would say 3 calendar days / 14 total ≈ 21%
		assert.False(t, triggered, "weekend should not inflate the elapsed ratio")
	})

	t.Run("ignores equity", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, time.Date(2026, 3, 2, 10, 0, 0, 0, etLoc), domain.AssetClassEquity)
		now := time.Date(2026, 3, 9, 12, 0, 0, 0, etLoc)
		triggered, _ := Evaluate(rule, pos, 100, now, EvalContext{})
		assert.False(t, triggered)
	})
}

// ---------------------------------------------------------------------------
// TieredTP
// ---------------------------------------------------------------------------

func TestEvaluate_TieredTP_Tier1(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)

	rule := domain.ExitRule{
		Type: domain.ExitRuleTieredTP,
		Params: map[string]float64{
			"first_tier_pct": 0.5, "first_tier_rr": 1.5,
			"trail_pct": 0.02, "initial_risk_pct": 0.01,
		},
	}

	t.Run("triggers at RR target", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		triggered, reason := Evaluate(rule, pos, 102.0, now, EvalContext{})
		assert.True(t, triggered)
		assert.Contains(t, reason, "tiered_tp_tier1")
		assert.Equal(t, 0.5, pos.CustomState["tiered_tp_exit_qty_frac"])
		assert.Equal(t, 1.0, pos.CustomState["breakeven_activated"])
	})

	t.Run("does not trigger below target", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		triggered, _ := Evaluate(rule, pos, 101.0, now, EvalContext{})
		assert.False(t, triggered)
	})
}

func TestEvaluate_TieredTP_Tier2Trail(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)

	rule := domain.ExitRule{
		Type: domain.ExitRuleTieredTP,
		Params: map[string]float64{
			"first_tier_pct": 0.5, "first_tier_rr": 1.5,
			"trail_pct": 0.02, "initial_risk_pct": 0.01,
		},
	}

	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
	Evaluate(rule, pos, 102.0, now, EvalContext{}) // tier 1
	Evaluate(rule, pos, 105.0, now, EvalContext{}) // push HWM

	// Trail = 105*(1-0.02) = 102.9. Price 102 < 102.9 → tier 2
	triggered, reason := Evaluate(rule, pos, 102.0, now, EvalContext{})
	assert.True(t, triggered)
	assert.Contains(t, reason, "tiered_tp_tier2_trail")
}

// ---------------------------------------------------------------------------
// TimePartial
// ---------------------------------------------------------------------------

func TestEvaluate_TimePartial(t *testing.T) {
	etLoc := mustETLocation(t)
	entryTime := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)

	rule := domain.ExitRule{
		Type: domain.ExitRuleTimePartial,
		Params: map[string]float64{"minutes": 60, "partial_pct": 0.5, "min_profit_pct": 0.001},
	}

	t.Run("triggers after hold with profit", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		now := entryTime.Add(90 * time.Minute)
		triggered, reason := Evaluate(rule, pos, 100.5, now, EvalContext{})
		assert.True(t, triggered)
		assert.Contains(t, reason, "time_partial")
		assert.Equal(t, 0.5, pos.CustomState["time_partial_exit_qty_frac"])
	})

	t.Run("does not trigger before minutes", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		triggered, _ := Evaluate(rule, pos, 105.0, entryTime.Add(30*time.Minute), EvalContext{})
		assert.False(t, triggered)
	})

	t.Run("does not trigger without profit", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		triggered, _ := Evaluate(rule, pos, 99.0, entryTime.Add(90*time.Minute), EvalContext{})
		assert.False(t, triggered)
	})

	t.Run("does not fire twice", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, entryTime, domain.AssetClassEquity)
		now := entryTime.Add(90 * time.Minute)
		Evaluate(rule, pos, 100.5, now, EvalContext{})
		triggered, _ := Evaluate(rule, pos, 101.0, now.Add(time.Minute), EvalContext{})
		assert.False(t, triggered)
	})
}

// ---------------------------------------------------------------------------
// Short-side coverage for existing evaluators
// ---------------------------------------------------------------------------

func TestEvaluate_VolatilityStop_Short(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)

	rule, err := domain.NewExitRule(domain.ExitRuleVolatilityStop, map[string]float64{"atr_multiplier": 1.5})
	require.NoError(t, err)

	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
	pos.Side = "SELL"
	pos.LowWaterMark = 95.0

	triggered, reason := Evaluate(rule, pos, 99.0, now, EvalContext{ATR: 2.0})
	assert.True(t, triggered)
	assert.Contains(t, reason, "volatility_stop")

	triggered2, _ := Evaluate(rule, pos, 97.0, now, EvalContext{ATR: 2.0})
	assert.False(t, triggered2)
}

func TestEvaluate_SDTarget_Short(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)

	rule := domain.ExitRule{Type: domain.ExitRuleSDTarget, Params: map[string]float64{"sd_level": 2.0}}
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
	pos.Side = "SELL"

	ctx := EvalContext{VWAPValue: 150.0, SDBands: map[float64]float64{2.0: 154.0}}

	triggered, reason := Evaluate(rule, pos, 145.0, now, ctx)
	assert.True(t, triggered)
	assert.Contains(t, reason, "sd_target")

	triggered2, _ := Evaluate(rule, pos, 148.0, now, ctx)
	assert.False(t, triggered2)
}

func TestEvaluate_MaxLoss_Short(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)

	rule, _ := domain.NewExitRule(domain.ExitRuleMaxLoss, map[string]float64{"pct": 0.02})
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
	pos.Side = "SELL"

	triggered, _ := Evaluate(rule, pos, 103.0, now, EvalContext{})
	assert.True(t, triggered)

	triggered2, _ := Evaluate(rule, pos, 99.0, now, EvalContext{})
	assert.False(t, triggered2)
}

func TestEvaluate_ProfitTarget_Short(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 0, 0, 0, etLoc)

	rule, _ := domain.NewExitRule(domain.ExitRuleProfitTarget, map[string]float64{"pct": 0.03})
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
	pos.Side = "SELL"

	triggered, _ := Evaluate(rule, pos, 96.0, now, EvalContext{})
	assert.True(t, triggered)

	triggered2, _ := Evaluate(rule, pos, 99.0, now, EvalContext{})
	assert.False(t, triggered2)
}

// ---------------------------------------------------------------------------
// TradingDaysBetween (used by ExpiryWatch)
// ---------------------------------------------------------------------------

func TestTradingDaysBetween(t *testing.T) {
	etLoc := mustETLocation(t)

	t.Run("weekdays only", func(t *testing.T) {
		// Mon March 2 to Fri March 6 = 4 trading days
		from := time.Date(2026, 3, 2, 10, 0, 0, 0, etLoc)
		to := time.Date(2026, 3, 6, 16, 0, 0, 0, etLoc)
		assert.Equal(t, 4, domain.TradingDaysBetween(from, to))
	})

	t.Run("spans weekend", func(t *testing.T) {
		// Fri March 6 to Mon March 9 = 1 trading day (just Friday)
		from := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)
		to := time.Date(2026, 3, 9, 10, 0, 0, 0, etLoc)
		assert.Equal(t, 1, domain.TradingDaysBetween(from, to))
	})

	t.Run("same day returns zero", func(t *testing.T) {
		d := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)
		assert.Equal(t, 0, domain.TradingDaysBetween(d, d))
	})

	t.Run("to before from returns zero", func(t *testing.T) {
		from := time.Date(2026, 3, 9, 10, 0, 0, 0, etLoc)
		to := time.Date(2026, 3, 6, 10, 0, 0, 0, etLoc)
		assert.Equal(t, 0, domain.TradingDaysBetween(from, to))
	})
}

// ---------------------------------------------------------------------------
// MFE/MAE Premium Tracking (computed in exit_eval.go eval loop)
// ---------------------------------------------------------------------------

// updatePremiumMFEMAE simulates the MFE/MAE tracking logic from exit_eval.go
// for unit testing. This mirrors the code in the EvalExitRules loop.
func updatePremiumMFEMAE(pos *domain.MonitoredPosition, estPremium float64) {
	if pos.CustomState == nil {
		pos.CustomState = make(map[string]float64)
	}
	if estPremium <= 0 {
		return
	}
	if hwm, ok := pos.CustomState["premium_hwm"]; !ok || estPremium > hwm {
		pos.CustomState["premium_hwm"] = estPremium
	}
	if lwm, ok := pos.CustomState["premium_lwm"]; !ok || estPremium < lwm {
		pos.CustomState["premium_lwm"] = estPremium
	}
	entryPremium := pos.CustomState["option_premium"]
	if entryPremium > 0 {
		pctChange := (estPremium - entryPremium) / entryPremium
		if mfe, ok := pos.CustomState["premium_mfe_pct"]; !ok || pctChange > mfe {
			pos.CustomState["premium_mfe_pct"] = pctChange
		}
		if mae, ok := pos.CustomState["premium_mae_pct"]; !ok || pctChange < mae {
			pos.CustomState["premium_mae_pct"] = pctChange
		}
		// Use a simple counter for test purposes (real code uses elapsed time)
		pos.CustomState["minutes_since_entry"] += 1.0
		if _, set := pos.CustomState["minutes_to_first_profit"]; !set {
			if pctChange > 0 {
				pos.CustomState["minutes_to_first_profit"] = pos.CustomState["minutes_since_entry"]
			}
		}
	}
}

func TestPremiumMFEMAE_BasicTracking(t *testing.T) {
	pos := newTestMonitoredPosition(t, 150, time.Now(), domain.AssetClassEquity)
	pos.InstrumentType = domain.InstrumentTypeOption
	pos.CustomState["option_premium"] = 5.00

	// Bar 1: premium drops to 4.50 (-10%)
	updatePremiumMFEMAE(pos, 4.50)
	assert.InDelta(t, -0.10, pos.CustomState["premium_mae_pct"], 0.001)
	assert.InDelta(t, -0.10, pos.CustomState["premium_mfe_pct"], 0.001)
	assert.Equal(t, 1.0, pos.CustomState["minutes_since_entry"])

	// Bar 2: premium recovers to 5.50 (+10%)
	updatePremiumMFEMAE(pos, 5.50)
	assert.InDelta(t, 0.10, pos.CustomState["premium_mfe_pct"], 0.001)
	assert.InDelta(t, -0.10, pos.CustomState["premium_mae_pct"], 0.001) // MAE unchanged
	assert.Equal(t, 2.0, pos.CustomState["minutes_since_entry"])

	// Bar 3: premium rises to 6.00 (+20%)
	updatePremiumMFEMAE(pos, 6.00)
	assert.InDelta(t, 0.20, pos.CustomState["premium_mfe_pct"], 0.001)
	assert.InDelta(t, -0.10, pos.CustomState["premium_mae_pct"], 0.001) // MAE still -10%

	// Bar 4: premium crashes to 3.00 (-40%)
	updatePremiumMFEMAE(pos, 3.00)
	assert.InDelta(t, 0.20, pos.CustomState["premium_mfe_pct"], 0.001) // MFE still +20%
	assert.InDelta(t, -0.40, pos.CustomState["premium_mae_pct"], 0.001)
}

func TestPremiumMFEMAE_LowWaterMark(t *testing.T) {
	pos := newTestMonitoredPosition(t, 150, time.Now(), domain.AssetClassEquity)
	pos.InstrumentType = domain.InstrumentTypeOption
	pos.CustomState["option_premium"] = 5.00

	updatePremiumMFEMAE(pos, 4.00) // LWM = 4.00
	updatePremiumMFEMAE(pos, 6.00) // LWM stays 4.00
	updatePremiumMFEMAE(pos, 3.50) // LWM = 3.50

	assert.InDelta(t, 3.50, pos.CustomState["premium_lwm"], 0.001)
	assert.InDelta(t, 6.00, pos.CustomState["premium_hwm"], 0.001)
}

func TestPremiumMFEMAE_MinutesToFirstProfit(t *testing.T) {
	pos := newTestMonitoredPosition(t, 150, time.Now(), domain.AssetClassEquity)
	pos.InstrumentType = domain.InstrumentTypeOption
	pos.CustomState["option_premium"] = 5.00

	// Bars 1-3: losing
	updatePremiumMFEMAE(pos, 4.80)
	updatePremiumMFEMAE(pos, 4.50)
	updatePremiumMFEMAE(pos, 4.90)
	_, hasProfit := pos.CustomState["minutes_to_first_profit"]
	assert.False(t, hasProfit, "should not be set while never profitable")

	// Bar 4: first profit
	updatePremiumMFEMAE(pos, 5.10)
	assert.Equal(t, 4.0, pos.CustomState["minutes_to_first_profit"])

	// Bar 5: loses again — minutes_to_first_profit should NOT change
	updatePremiumMFEMAE(pos, 4.00)
	assert.Equal(t, 4.0, pos.CustomState["minutes_to_first_profit"], "should be set once and never change")
}

func TestPremiumMFEMAE_NeverProfitable(t *testing.T) {
	pos := newTestMonitoredPosition(t, 150, time.Now(), domain.AssetClassEquity)
	pos.InstrumentType = domain.InstrumentTypeOption
	pos.CustomState["option_premium"] = 5.00

	updatePremiumMFEMAE(pos, 4.90)
	updatePremiumMFEMAE(pos, 4.50)
	updatePremiumMFEMAE(pos, 4.00)

	_, hasProfit := pos.CustomState["minutes_to_first_profit"]
	assert.False(t, hasProfit, "should not be set if never profitable")
	assert.Equal(t, 3.0, pos.CustomState["minutes_since_entry"])
}

func TestPremiumMFEMAE_ZeroEntryPremium(t *testing.T) {
	pos := newTestMonitoredPosition(t, 150, time.Now(), domain.AssetClassEquity)
	pos.InstrumentType = domain.InstrumentTypeOption
	pos.CustomState["option_premium"] = 0 // edge case

	updatePremiumMFEMAE(pos, 5.00)

	// With zero entry premium, MFE/MAE should not be computed
	_, hasMFE := pos.CustomState["premium_mfe_pct"]
	assert.False(t, hasMFE, "should not compute MFE with zero entry premium")
	// But HWM/LWM should still be tracked
	assert.InDelta(t, 5.00, pos.CustomState["premium_hwm"], 0.001)
	assert.InDelta(t, 5.00, pos.CustomState["premium_lwm"], 0.001)
}

func TestPremiumMFEMAE_ImmediateProfitable(t *testing.T) {
	pos := newTestMonitoredPosition(t, 150, time.Now(), domain.AssetClassEquity)
	pos.InstrumentType = domain.InstrumentTypeOption
	pos.CustomState["option_premium"] = 5.00

	// First bar is immediately profitable
	updatePremiumMFEMAE(pos, 5.50)
	assert.Equal(t, 1.0, pos.CustomState["minutes_to_first_profit"])
	assert.InDelta(t, 0.10, pos.CustomState["premium_mfe_pct"], 0.001)
}
