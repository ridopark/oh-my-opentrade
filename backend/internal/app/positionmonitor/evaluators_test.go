package positionmonitor

import (
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

	t.Run("crosses +1.0 SD sets stop to entry (breakeven)", func(t *testing.T) {
		pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)
		UpdateStepStopState(pos, 102.5, ctx, now, 0.0)
		assert.Equal(t, 1.0, pos.CustomState["highest_sd_crossed"])
		assert.Equal(t, 100.0, pos.CustomState["step_stop_level"])
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

		// Tick 1: price at 102.5 → crosses +1.0 SD → stop = entry (100)
		UpdateStepStopState(pos, 102.5, ctx, now, 0.0)
		assert.Equal(t, 1.0, pos.CustomState["highest_sd_crossed"])
		assert.Equal(t, 100.0, pos.CustomState["step_stop_level"])

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
		assert.Equal(t, 100.0, pos.CustomState["step_stop_level"])
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
