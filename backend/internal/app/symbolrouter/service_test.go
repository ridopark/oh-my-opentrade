package symbolrouter_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/symbolrouter"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_PublishesEffectiveSymbolsUpdated_SingleStrategy(t *testing.T) {
	ctx := context.Background()
	bus := memory.NewBus()

	svc := symbolrouter.NewService(
		bus,
		[]symbolrouter.StrategySpec{{
			Key:           "orb_break_retest",
			BaseSymbols:   []string{"AAPL", "MSFT", "GOOGL"},
			WatchlistMode: "intersection",
		}},
		"tenant123",
		domain.EnvModePaper,
		zerolog.Nop(),
	)

	var got domain.Event
	err := bus.Subscribe(ctx, domain.EventEffectiveSymbolsUpdated, func(_ context.Context, ev domain.Event) error {
		got = ev
		return nil
	})
	require.NoError(t, err)

	err = svc.Start(ctx)
	require.NoError(t, err)

	completed := screener.CompletedPayload{
		RunID: "run-1",
		AsOf:  time.Now(),
		Ranked: []screener.RankedSymbol{
			{Symbol: "AAPL", TotalScore: 1},
			{Symbol: "TSLA", TotalScore: 0.9},
			{Symbol: "MSFT", TotalScore: 0.8},
		},
	}
	inEvt, err := domain.NewEvent(domain.EventScreenerCompleted, "tenant123", domain.EnvModePaper, "screener-1", completed)
	require.NoError(t, err)

	err = bus.Publish(ctx, *inEvt)
	require.NoError(t, err)

	assert.Equal(t, domain.EventEffectiveSymbolsUpdated, got.Type)
	p, ok := got.Payload.(screener.EffectiveSymbolsUpdatedPayload)
	require.True(t, ok)
	assert.Equal(t, "orb_break_retest", p.StrategyKey)
	assert.Equal(t, completed.RunID, p.RunID)
	assert.Equal(t, "intersection", p.Mode)
	assert.Equal(t, "intersection", p.Source)
	assert.Equal(t, []string{"AAPL", "MSFT"}, p.Symbols)
}

func TestService_PublishesEffectiveSymbolsUpdated_MultipleStrategies(t *testing.T) {
	ctx := context.Background()
	bus := memory.NewBus()

	svc := symbolrouter.NewService(
		bus,
		[]symbolrouter.StrategySpec{
			{Key: "s1", BaseSymbols: []string{"AAPL", "MSFT"}, WatchlistMode: "intersection"},
			{Key: "s2", BaseSymbols: []string{"TSLA"}, WatchlistMode: "replace"},
		},
		"tenant123",
		domain.EnvModePaper,
		zerolog.Nop(),
	)

	var got []domain.Event
	err := bus.Subscribe(ctx, domain.EventEffectiveSymbolsUpdated, func(_ context.Context, ev domain.Event) error {
		got = append(got, ev)
		return nil
	})
	require.NoError(t, err)

	err = svc.Start(ctx)
	require.NoError(t, err)

	completed := screener.CompletedPayload{
		RunID: "run-2",
		AsOf:  time.Now(),
		Ranked: []screener.RankedSymbol{
			{Symbol: "AAPL", TotalScore: 1},
			{Symbol: "TSLA", TotalScore: 0.9},
			{Symbol: "MSFT", TotalScore: 0.8},
		},
	}
	inEvt, err := domain.NewEvent(domain.EventScreenerCompleted, "tenant123", domain.EnvModePaper, "screener-2", completed)
	require.NoError(t, err)

	err = bus.Publish(ctx, *inEvt)
	require.NoError(t, err)

	require.Len(t, got, 2)
	var p1, p2 screener.EffectiveSymbolsUpdatedPayload
	for _, ev := range got {
		p, ok := ev.Payload.(screener.EffectiveSymbolsUpdatedPayload)
		require.True(t, ok)
		if p.StrategyKey == "s1" {
			p1 = p
		}
		if p.StrategyKey == "s2" {
			p2 = p
		}
	}
	assert.Equal(t, []string{"AAPL", "MSFT"}, p1.Symbols)
	assert.Equal(t, "intersection", p1.Source)
	assert.Equal(t, []string{"AAPL", "TSLA", "MSFT"}, p2.Symbols)
	assert.Equal(t, "screener", p2.Source)
}

func TestService_InvalidPayloadType_ReturnsError(t *testing.T) {
	ctx := context.Background()
	bus := memory.NewBus()

	svc := symbolrouter.NewService(
		bus,
		[]symbolrouter.StrategySpec{{Key: "s1", BaseSymbols: []string{"AAPL"}, WatchlistMode: "static"}},
		"tenant123",
		domain.EnvModePaper,
		zerolog.Nop(),
	)

	err := svc.Start(ctx)
	require.NoError(t, err)

	badEvt, err := domain.NewEvent(domain.EventScreenerCompleted, "tenant123", domain.EnvModePaper, "screener-bad", "nope")
	require.NoError(t, err)

	err = bus.Publish(ctx, *badEvt)
	assert.Error(t, err)
}
