package strategy_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunner_SafeOnBar_IsolatesPanickingStrategy verifies that a panic inside
// one strategy instance does NOT crash the runner — it is recovered, logged,
// and surfaced as a returned error while subsequent bars continue processing.
//
// This is the regression test for the FAULTED component isolation pattern.
func TestRunner_SafeOnBar_IsolatesPanickingStrategy(t *testing.T) {
	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)

	m := metrics.New("test", "test", "test", false)
	runner.SetMetrics(m)

	panicStrat := newFakeStrategy("panic_strat", "1.0.0")
	panicStrat.onBarFunc = func(_ strat.Context, _ string, _ strat.Bar, _ strat.State) (strat.State, []strat.Signal, error) {
		panic("simulated nil pointer deref")
	}
	idA, _ := strat.NewInstanceID("panic_strat:1.0.0:AAPL")
	instA := strategy.NewInstance(idA, panicStrat, nil, strategy.InstanceAssignment{
		Symbols: []string{"AAPL"}, Priority: 100,
	}, strat.LifecycleLiveActive, nil)
	require.NoError(t, instA.InitSymbol(newTestCtx(), "AAPL", nil))
	router.Register(instA)

	ctx := context.Background()
	bar := strat.Bar{Time: time.Now(), Open: 100, High: 101, Low: 99, Close: 100, Volume: 10}

	// Each call must return cleanly without panicking. ProcessBar returns an
	// error because the only instance faulted, but the runner itself stays
	// alive and ready for the next bar.
	require.NotPanics(t, func() {
		_, _ = runner.ProcessBar(ctx, "AAPL", bar, strat.IndicatorData{Volume: 10})
	}, "runner must recover panics from instance.OnBar")
	require.NotPanics(t, func() {
		_, _ = runner.ProcessBar(ctx, "AAPL", bar, strat.IndicatorData{Volume: 10})
	})

	// Prometheus panic counter must have ticked twice (one per bar).
	total := gatherCounterSum(t, m, "omo_strategy_panics_total")
	assert.Equal(t, 2.0, total, "every recovered panic must increment omo_strategy_panics_total")
}

// gatherCounterSum returns the sum of all counter samples matching the
// metric name across every label set. Used to assert that a CounterVec
// incremented without depending on its internal child type.
func gatherCounterSum(t *testing.T, m *metrics.Metrics, metricName string) float64 {
	t.Helper()
	mfs, err := m.Reg.Gather()
	require.NoError(t, err)
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, c := range mf.GetMetric() {
			total += c.GetCounter().GetValue()
		}
	}
	return total
}
