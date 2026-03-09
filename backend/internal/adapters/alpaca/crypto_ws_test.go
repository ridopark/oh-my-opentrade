package alpaca

import (
	"context"
	"errors"
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
