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
			triggered, reason := Evaluate(tc.rule, tc.pos, tc.current, entryTime)
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
			triggered, reason := Evaluate(tc.rule, pos, tc.current, now)
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
			triggered, reason := Evaluate(tc.rule, pos, 0, tc.now)
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
			triggered, reason := Evaluate(tc.rule, pos, 0, tc.now)
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
			triggered, reason := Evaluate(tc.rule, tc.pos, 0, tc.now)
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
			triggered, reason := Evaluate(tc.rule, pos, tc.current, now)
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

func TestEvaluate_UnknownRuleType(t *testing.T) {
	etLoc := mustETLocation(t)
	now := time.Date(2026, 3, 6, 11, 30, 0, 0, etLoc)
	pos := newTestMonitoredPosition(t, 100, now.Add(-10*time.Minute), domain.AssetClassEquity)

	rule := domain.ExitRule{Type: domain.ExitRuleType("SOMETHING_ELSE"), Params: map[string]float64{"pct": 0.01}}
	triggered, reason := Evaluate(rule, pos, 101, now)
	assert.False(t, triggered)
	assert.Empty(t, reason)
}
