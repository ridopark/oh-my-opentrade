package strategy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	stratports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSpecStore struct {
	spec *stratports.Spec
	err  error

	getLatestCalls int
	lastID         strat.StrategyID
}

func (f *fakeSpecStore) List(context.Context, *stratports.SpecFilter) ([]stratports.Spec, error) {
	return nil, nil
}

func (f *fakeSpecStore) Get(context.Context, strat.StrategyID, strat.Version) (*stratports.Spec, error) {
	return nil, nil
}

func (f *fakeSpecStore) GetLatest(_ context.Context, id strat.StrategyID) (*stratports.Spec, error) {
	f.getLatestCalls++
	f.lastID = id
	if f.err != nil {
		return nil, f.err
	}
	return f.spec, nil
}

func (f *fakeSpecStore) Save(context.Context, stratports.Spec) error { return nil }

func (f *fakeSpecStore) Watch(context.Context) (<-chan strat.StrategyID, error) {
	ch := make(chan strat.StrategyID)
	close(ch)
	return ch, nil
}

func mustEnvMode(t *testing.T) domain.EnvMode {
	t.Helper()
	mode, err := domain.NewEnvMode("Paper")
	require.NoError(t, err)
	return mode
}

func waitForEvents(t *testing.T, ch <-chan domain.Event, n int) []domain.Event {
	t.Helper()
	deadline := time.After(500 * time.Millisecond)
	var out []domain.Event
	for len(out) < n {
		select {
		case ev := <-ch:
			out = append(out, ev)
		case <-deadline:
			require.FailNow(t, "timed out waiting for events", "got %d want %d", len(out), n)
		}
	}
	return out
}

func subscribeOrderIntentCreated(t *testing.T, bus *memory.Bus) <-chan domain.Event {
	t.Helper()
	ch := make(chan domain.Event, 10)
	ctx := context.Background()
	require.NoError(t, bus.Subscribe(ctx, domain.EventOrderIntentCreated, func(_ context.Context, ev domain.Event) error {
		ch <- ev
		return nil
	}))
	return ch
}

func publishSignalCreated(t *testing.T, bus *memory.Bus, sig strat.Signal) {
	t.Helper()
	ctx := context.Background()
	envMode := mustEnvMode(t)
	ev, err := domain.NewEvent(domain.EventSignalCreated, "t1", envMode, "sig-1", sig)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *ev))
}

func TestRiskSizer_HandleSignal_Entry_Buy(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":     int64(5),
		"stop_bps":             int64(25),
		"risk_per_trade_bps":   int64(10),
		"some_other_parameter": "ignored",
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)

	ctx := context.Background()
	require.NoError(t, rs.Start(ctx))

	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("orb_break:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	require.Equal(t, domain.EventOrderIntentCreated, evs[0].Type)
	intent, ok := evs[0].Payload.(domain.OrderIntent)
	require.True(t, ok)

	assert.Equal(t, domain.DirectionLong, intent.Direction)
	assert.Equal(t, domain.Symbol("AAPL"), intent.Symbol)
	assert.Equal(t, "orb_break", intent.Strategy)
	assert.InDelta(t, 100*(1+0.0005), intent.LimitPrice, 0.0000001)
	assert.InDelta(t, 100*(1-0.0025), intent.StopLoss, 0.0000001)
	assert.Equal(t, 10, intent.MaxSlippageBPS)
}

func TestRiskSizer_HandleSignal_Entry_Sell(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(5),
		"stop_bps":           int64(25),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	require.NoError(t, rs.Start(context.Background()))

	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("orb_break:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideSell, 0.9, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	intent := evs[0].Payload.(domain.OrderIntent)
	assert.Equal(t, domain.DirectionShort, intent.Direction)
	assert.InDelta(t, 100*(1-0.0005), intent.LimitPrice, 0.0000001)
	assert.InDelta(t, 100*(1+0.0025), intent.StopLoss, 0.0000001)
}

func TestRiskSizer_HandleSignal_FlatIgnored(t *testing.T) {
	bus := memory.NewBus()
	rs := strategy.NewRiskSizer(bus, &fakeSpecStore{}, 100000, nil)
	require.NoError(t, rs.Start(context.Background()))
	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("orb_break:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalFlat, strat.SideBuy, 0.5, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	select {
	case <-received:
		require.FailNow(t, "unexpected event")
	case <-time.After(50 * time.Millisecond):
		assert.True(t, true)
	}
}

func TestRiskSizer_HandleSignal_NoRefPrice(t *testing.T) {
	bus := memory.NewBus()
	rs := strategy.NewRiskSizer(bus, &fakeSpecStore{}, 100000, nil)
	require.NoError(t, rs.Start(context.Background()))
	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("orb_break:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{})
	publishSignalCreated(t, bus, sig)

	select {
	case <-received:
		require.FailNow(t, "unexpected event")
	case <-time.After(50 * time.Millisecond):
		assert.True(t, true)
	}
}

func TestRiskSizer_HandleSignal_SpecNotFound(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{err: errors.New("not found")}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	require.NoError(t, rs.Start(context.Background()))
	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("orb_break:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.7, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	intent := evs[0].Payload.(domain.OrderIntent)
	assert.InDelta(t, 100*(1+0.0005), intent.LimitPrice, 0.0000001)
	assert.InDelta(t, 100*(1-0.0025), intent.StopLoss, 0.0000001)
}

func TestRiskSizer_HandleSignal_PositionSizing(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(0),
		"stop_bps":           int64(250),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	require.NoError(t, rs.Start(context.Background()))
	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("orb_break:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	evs := waitForEvents(t, received, 1)
	intent := evs[0].Payload.(domain.OrderIntent)
	assert.Equal(t, 40.0, intent.Quantity)
}

func TestRiskSizer_SetAccountEquity(t *testing.T) {
	bus := memory.NewBus()
	store := &fakeSpecStore{spec: &stratports.Spec{Params: map[string]any{
		"limit_offset_bps":   int64(0),
		"stop_bps":           int64(250),
		"risk_per_trade_bps": int64(10),
	}}}
	rs := strategy.NewRiskSizer(bus, store, 100000, nil)
	require.NoError(t, rs.Start(context.Background()))
	received := subscribeOrderIntentCreated(t, bus)

	rs.SetAccountEquity(50000)

	iid, _ := strat.NewInstanceID("orb_break:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	intent := waitForEvents(t, received, 1)[0].Payload.(domain.OrderIntent)
	assert.Equal(t, 20.0, intent.Quantity)
}

func TestRiskSizer_Start_SubscribesCorrectly(t *testing.T) {
	bus := memory.NewBus()
	rs := strategy.NewRiskSizer(bus, &fakeSpecStore{}, 100000, nil)
	require.NoError(t, rs.Start(context.Background()))

	received := subscribeOrderIntentCreated(t, bus)

	iid, _ := strat.NewInstanceID("orb_break:1.0.0:AAPL")
	sig, _ := strat.NewSignal(iid, "AAPL", strat.SignalEntry, strat.SideBuy, 0.8, map[string]string{"ref_price": "100"})
	publishSignalCreated(t, bus, sig)

	_ = waitForEvents(t, received, 1)
}
