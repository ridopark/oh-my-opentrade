package monitor_test

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/monitor/monitortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/require"
)

// TestEnrichedBarPayload_FieldSchema locks the field set of
// domain.EnrichedBarPayload at compile time. Adding a field is allowed
// (the new field defaults to zero in the captured payload, which is
// acceptable for downstream consumers); removing or renaming a field
// fails the test, protecting chart streams, regime detection, and the
// dashboard from accidental schema regressions.
func TestEnrichedBarPayload_FieldSchema(t *testing.T) {
	want := []string{
		"AVWAPs",
		"Close",
		"EMA200",
		"EMA21",
		"EMA50",
		"EMA9",
		"High",
		"Low",
		"Open",
		"Symbol",
		"Time",
		"Timeframe",
		"Volume",
	}
	typ := reflect.TypeOf(domain.EnrichedBarPayload{})
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, typ.Field(i).Name)
	}
	sort.Strings(got)
	for _, name := range want {
		idx := sort.SearchStrings(got, name)
		if idx == len(got) || got[idx] != name {
			t.Fatalf("EnrichedBarPayload missing required field %q (got fields: %v)", name, got)
		}
	}
}

// TestEnrichedBar_PublishedPayloadShape drives a deterministic 1m bar
// stream through the production publish path and captures the resulting
// EnrichedBar event. Asserts every numeric/string field flows through
// correctly so the live dashboard, SSE streams, and regime consumers do
// not regress as the indicator service evolves.
func TestEnrichedBar_PublishedPayloadShape(t *testing.T) {
	bus := memory.NewBus()
	repo := &mockRepository{}
	svc, idx := monitortest.NewSvc(bus, repo, "monitor_enriched_contract")

	sym, _ := domain.NewSymbol("BTC/USD")

	idx.SetSessionOpen(time.Date(2026, 4, 29, 14, 30, 0, 0, time.UTC))

	const warmupBars = 220
	warmup := make([]domain.MarketBar, warmupBars)
	base := time.Date(2026, 4, 29, 13, 0, 0, 0, time.UTC)
	for i := 0; i < warmupBars; i++ {
		bar, err := domain.NewMarketBar(
			base.Add(time.Duration(i)*time.Minute),
			sym,
			"1m",
			100.0+float64(i)*0.1,
			100.5+float64(i)*0.1,
			99.5+float64(i)*0.1,
			100.2+float64(i)*0.1,
			10.0,
		)
		require.NoError(t, err)
		warmup[i] = bar
	}
	n := svc.WarmUp(warmup)
	require.Equal(t, warmupBars, n)

	ctx := context.Background()
	require.NoError(t, svc.Start(ctx))
	require.NoError(t, svc.StartEnrichedBarPublisher(ctx))

	var (
		mu       sync.Mutex
		captured []domain.EnrichedBarPayload
	)
	require.NoError(t, bus.Subscribe(ctx, domain.EventEnrichedBar, func(_ context.Context, ev domain.Event) error {
		payload, ok := ev.Payload.(domain.EnrichedBarPayload)
		if !ok {
			t.Errorf("EnrichedBar payload is not EnrichedBarPayload, got %T", ev.Payload)
			return nil
		}
		mu.Lock()
		captured = append(captured, payload)
		mu.Unlock()
		return nil
	}))

	live, err := domain.NewMarketBar(
		base.Add(time.Duration(warmupBars)*time.Minute),
		sym,
		"1m",
		122.0, 122.7, 121.6, 122.3, 25.0,
	)
	require.NoError(t, err)
	liveEvt, err := domain.NewEvent(domain.EventMarketBarSanitized, "tenant", domain.EnvModePaper, "enriched-test", live)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, *liveEvt))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, captured, 1, "exactly one EnrichedBar should be published per sanitized 1m bar")

	got := captured[0]
	require.Equal(t, live.Time.Unix(), got.Time, "Time must equal bar.Time.Unix()")
	require.Equal(t, sym.String(), got.Symbol, "Symbol must equal bar.Symbol.String()")
	require.Equal(t, string(live.Timeframe), got.Timeframe, "Timeframe must equal bar.Timeframe")
	require.Equal(t, live.Open, got.Open)
	require.Equal(t, live.High, got.High)
	require.Equal(t, live.Low, got.Low)
	require.Equal(t, live.Close, got.Close)
	require.Equal(t, live.Volume, got.Volume)
	require.Greater(t, got.EMA9, 0.0, "EMA9 must be populated after warmup")
	require.Greater(t, got.EMA21, 0.0, "EMA21 must be populated after warmup")
	require.Greater(t, got.EMA50, 0.0, "EMA50 must be populated after warmup")
	require.Greater(t, got.EMA200, 0.0, "EMA200 must be populated after warmup")
}
