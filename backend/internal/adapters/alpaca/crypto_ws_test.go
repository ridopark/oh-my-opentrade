package alpaca

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestCryptoBarToMarketBar(t *testing.T) {
	ts := time.Date(2025, 3, 5, 12, 0, 0, 0, time.UTC)
	cb := alpacastream.CryptoBar{
		Symbol:    "BTC/USD",
		Open:      60000.0,
		High:      61000.0,
		Low:       59500.0,
		Close:     60500.0,
		Volume:    1.5,
		Timestamp: ts,
	}

	bar, err := CryptoBarToMarketBar(cb)
	require.NoError(t, err)

	assert.Equal(t, "BTC/USD", bar.Symbol.String())
	assert.Equal(t, "1m", bar.Timeframe.String())
	assert.Equal(t, ts, bar.Time)
	assert.Equal(t, 60000.0, bar.Open)
	assert.Equal(t, 61000.0, bar.High)
	assert.Equal(t, 59500.0, bar.Low)
	assert.Equal(t, 60500.0, bar.Close)
	assert.Equal(t, 1.5, bar.Volume)
}

func TestCryptoBarToMarketBar_NormalizesSymbol(t *testing.T) {
	cb := alpacastream.CryptoBar{
		Symbol:    "BTCUSD",
		Open:      60000.0,
		High:      61000.0,
		Low:       59500.0,
		Close:     60500.0,
		Volume:    1.5,
		Timestamp: time.Now(),
	}

	bar, err := CryptoBarToMarketBar(cb)
	require.NoError(t, err)
	assert.Equal(t, "BTC/USD", bar.Symbol.String())
}

func TestCryptoBarToMarketBar_ZeroVolume(t *testing.T) {
	cb := alpacastream.CryptoBar{
		Symbol:    "BTC/USD",
		Open:      60000.0,
		High:      61000.0,
		Low:       59500.0,
		Close:     60500.0,
		Volume:    0,
		Timestamp: time.Now(),
	}

	bar, err := CryptoBarToMarketBar(cb)
	require.NoError(t, err, "zero volume is valid for crypto idle bars")
	assert.Equal(t, 0.0, bar.Volume)
}

func TestNewCryptoWSClient_RequiresCredentials(t *testing.T) {
	_, err := NewCryptoWSClient("wss://test", "", "secret", "us", nil, zerolog.Nop())
	assert.ErrorIs(t, err, ErrCryptoWSMissingCredentials)

	_, err = NewCryptoWSClient("wss://test", "key", "", "us", nil, zerolog.Nop())
	assert.ErrorIs(t, err, ErrCryptoWSMissingCredentials)
}

func TestNewCryptoWSClient_DefaultFeed(t *testing.T) {
	client, err := NewCryptoWSClient("wss://test", "key", "secret", "", nil, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "us", client.feed)
}

func TestNewCryptoWSClient_DefaultURL(t *testing.T) {
	client, err := NewCryptoWSClient("", "key", "secret", "us", nil, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "wss://stream.data.alpaca.markets", client.cryptoDataURL)
}

func fakeCryptoConnectError(msg string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		return errors.New(msg)
	}
}

func makeCryptoWSClientWithFakeConnect(fetcher BarFetcher, connects ...func(ctx context.Context) error) *CryptoWSClient {
	client, _ := NewCryptoWSClient("wss://test", "key", "secret", "us", fetcher, zerolog.Nop())
	client.startupDelay = 0
	var mu sync.Mutex
	idx := 0
	client.connectFactory = func(symStrs []string, _ func(alpacastream.CryptoBar), _ func(alpacastream.CryptoTrade)) cryptoConnectFn {
		return func(ctx context.Context) error {
			mu.Lock()
			i := idx
			if i < len(connects) {
				idx++
			}
			mu.Unlock()
			if i < len(connects) {
				return connects[i](ctx)
			}
			<-ctx.Done()
			return nil
		}
	}
	return client
}

func TestCryptoStreamBars_GhostSessionProbe(t *testing.T) {
	origSchedule := probeSchedule
	probeSchedule = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	defer func() { probeSchedule = origSchedule }()

	handler := func(_ context.Context, _ domain.MarketBar) error { return nil }

	client := makeCryptoWSClientWithFakeConnect(nil,
		fakeCryptoConnectError("connection limit exceeded"),
		fakeCryptoConnectError("406 connection limit exceeded"),
		fakeCryptoConnectError("ghost cleared: different error"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sym, err := domain.NewSymbol("BTC/USD")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- client.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe("1m"), handler)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("StreamBars did not return after context cancel")
	}
}

func TestCryptoStreamBars_GhostSessionRESTBridge(t *testing.T) {
	origSchedule := probeSchedule
	probeSchedule = []time.Duration{50 * time.Millisecond}
	defer func() { probeSchedule = origSchedule }()

	sym, err := domain.NewSymbol("BTC/USD")
	require.NoError(t, err)
	tf, _ := domain.NewTimeframe("1m")
	barTime := time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC)

	var fetchCount atomic.Int32
	fetcher := func(ctx context.Context, s domain.Symbol, _ domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
		fetchCount.Add(1)
		bar, _ := domain.NewMarketBar(barTime, sym, tf, 60000, 61000, 59000, 60500, 1.5)
		return []domain.MarketBar{bar}, nil
	}

	var handlerCount atomic.Int32
	handler := func(_ context.Context, _ domain.MarketBar) error {
		handlerCount.Add(1)
		return nil
	}

	client := makeCryptoWSClientWithFakeConnect(fetcher,
		fakeCryptoConnectError("connection limit exceeded"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe("1m"), handler)
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StreamBars did not return after context cancel")
	}

	assert.GreaterOrEqual(t, int(fetchCount.Load()), 1, "REST poller should have fetched bars")
	assert.GreaterOrEqual(t, int(handlerCount.Load()), 1, "handler should have received at least one bar from REST poller")
}

func TestCryptoStreamBars_GhostSessionCancelDuringProbe(t *testing.T) {
	origSchedule := probeSchedule
	probeSchedule = []time.Duration{10 * time.Second}
	defer func() { probeSchedule = origSchedule }()

	client := makeCryptoWSClientWithFakeConnect(nil,
		fakeCryptoConnectError("connection limit exceeded"),
	)

	sym, err := domain.NewSymbol("BTC/USD")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	var streamErr error
	done := make(chan struct{})
	go func() {
		streamErr = client.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe("1m"),
			func(_ context.Context, _ domain.MarketBar) error { return nil })
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		assert.NoError(t, streamErr)
	case <-time.After(2 * time.Second):
		t.Fatal("StreamBars did not respect context cancellation during probe sleep")
	}
}

func TestGhostProbeConstants(t *testing.T) {
	assert.Equal(t, 3, maxGhostProbes, "ghost probes should be capped at 3")
	assert.Equal(t, 2*time.Minute, ghostColdTurkeyPeriod, "cold turkey should be 2 minutes")
	assert.Equal(t, 8*time.Second, cryptoStartupDelay, "startup delay should be 8 seconds")
	assert.Equal(t, 30*time.Second, minCryptoBackoff, "min crypto backoff should be 30 seconds")
}

// slow test (~20s): waits for staleFeedWatchdog interval
func TestCryptoStreamBars_RESTPollerRestartsOnFailedReconnect(t *testing.T) {
	sym, err := domain.NewSymbol("BTC/USD")
	require.NoError(t, err)
	tf, _ := domain.NewTimeframe("1m")
	barTime := time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC)

	var fetchCount atomic.Int32
	fetcher := func(ctx context.Context, s domain.Symbol, _ domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
		fetchCount.Add(1)
		bar, _ := domain.NewMarketBar(barTime.Add(time.Duration(fetchCount.Load())*time.Minute), sym, tf, 60000, 61000, 59000, 60500, 1.5)
		return []domain.MarketBar{bar}, nil
	}

	client, err := NewCryptoWSClient("wss://test", "key", "secret", "us", fetcher, zerolog.Nop())
	require.NoError(t, err)
	client.startupDelay = 0

	var callIdx atomic.Int32
	client.connectFactory = func(symStrs []string, barHandler func(alpacastream.CryptoBar), tradeHandler func(alpacastream.CryptoTrade)) cryptoConnectFn {
		return func(ctx context.Context) error {
			i := callIdx.Add(1) - 1
			switch i {
			case 0:
				barHandler(alpacastream.CryptoBar{
					Symbol: "BTC/USD", Open: 60000, High: 61000, Low: 59000, Close: 60500, Volume: 1.5,
					Timestamp: barTime,
				})
				<-ctx.Done()
				return nil
			case 1:
				return errors.New("connection reset by peer")
			default:
				<-ctx.Done()
				return nil
			}
		}
	}

	var handlerCount atomic.Int32
	handler := func(_ context.Context, bar domain.MarketBar) error {
		handlerCount.Add(1)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- client.StreamBars(ctx, []domain.Symbol{sym}, domain.Timeframe("1m"), handler)
	}()

	time.Sleep(100 * time.Millisecond)

	client.tracker.mu.Lock()
	client.tracker.lastBarAt = time.Now().Add(-35 * time.Minute)
	client.tracker.mu.Unlock()

	time.Sleep(20 * time.Second)

	fc := fetchCount.Load()
	assert.Greater(t, int(fc), 0, "REST poller should have fetched bars after failed reconnect")

	hc := handlerCount.Load()
	assert.Greater(t, int(hc), 1, "handler should have received bars from both WS and REST poller")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StreamBars did not return after cancel")
	}
}
