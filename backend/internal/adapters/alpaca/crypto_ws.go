package alpaca

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// CryptoWSClient handles WebSocket connections for Alpaca crypto market data.
// It uses a separate stream.CryptoClient (not the StocksClient used for equities)
// and includes reconnect, watchdog, and circuit breaker hardening.
type CryptoWSClient struct {
	cryptoDataURL string
	apiKey        string
	apiSecret     string
	feed          string // e.g. "us"
	tradeHandler  ports.TradeHandler
	log           zerolog.Logger
	tracker       *feedTracker
	closeOnce     sync.Once
	cancel        context.CancelFunc
	mu            sync.Mutex
}

// NewCryptoWSClient creates a new CryptoWSClient.
// cryptoDataURL is the base WebSocket URL (e.g. "wss://stream.data.alpaca.markets").
// feed is the crypto feed name (e.g. "us").
func NewCryptoWSClient(cryptoDataURL, apiKey, apiSecret, feed string, log zerolog.Logger) (*CryptoWSClient, error) {
	if apiKey == "" {
		return nil, ErrCryptoWSMissingCredentials
	}
	if apiSecret == "" {
		return nil, ErrCryptoWSMissingCredentials
	}
	if feed == "" {
		feed = "us"
	}
	if cryptoDataURL == "" {
		cryptoDataURL = "wss://stream.data.alpaca.markets"
	}
	return &CryptoWSClient{
		cryptoDataURL: cryptoDataURL,
		apiKey:        apiKey,
		apiSecret:     apiSecret,
		feed:          feed,
		log:           log.With().Str("component", "crypto_ws").Logger(),
		tracker:       newFeedTracker(),
	}, nil
}

// SetTradeHandler sets the callback for forwarding raw crypto trade ticks.
func (c *CryptoWSClient) SetTradeHandler(h ports.TradeHandler) { c.tradeHandler = h }

// FeedHealth returns a point-in-time snapshot of crypto WebSocket feed status.
func (c *CryptoWSClient) FeedHealth() FeedHealth { return c.tracker.Snapshot() }

// ErrCryptoWSMissingCredentials is returned when API key or secret is empty.
var ErrCryptoWSMissingCredentials = errors.New("crypto websocket requires API key and secret")

// StreamBars connects to the Alpaca crypto WebSocket and streams bars for the
// requested symbols. It reconnects with exponential backoff on failure and uses
// the shared stale feed watchdog to detect zombie connections.
func (c *CryptoWSClient) StreamBars(ctx context.Context, symbols []domain.Symbol, _ domain.Timeframe, handler ports.BarHandler) error {
	if len(symbols) == 0 {
		return nil
	}

	symStrs := make([]string, len(symbols))
	for i, s := range symbols {
		symStrs[i] = string(s)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()

	c.tracker.setState("reconnecting")
	attempt := 0
	consecutiveFails := 0

	for {
		if streamCtx.Err() != nil {
			c.tracker.setState("stopped")
			return nil
		}

		if ok, wait := c.tracker.cb.Allow(); !ok {
			c.tracker.setState("circuit_open")
			c.log.Warn().Dur("wait", wait).Msg("circuit breaker open — waiting before retry")
			select {
			case <-time.After(wait):
			case <-streamCtx.Done():
				c.tracker.setState("stopped")
				return nil
			}
		}

		if consecutiveFails >= maxConsecutiveFailsBeforeError {
			c.tracker.setState("stopped")
			return fmt.Errorf("crypto ws: %d consecutive connect failures — giving up", consecutiveFails)
		}

		attempt++
		if attempt > 1 {
			c.tracker.incReconnect()
			c.tracker.setState("reconnecting")
			c.log.Info().Int("attempt", attempt).Strs("symbols", symStrs).
				Msg("reconnecting to Alpaca crypto WebSocket")
		}

		connCtx, connCancel := context.WithCancel(streamCtx)
		var staleCancelMu sync.Mutex
		staleCancelFn := connCancel

		watchdogDone := make(chan struct{})
		go func() {
			defer close(watchdogDone)
			staleFeedWatchdog(connCtx, c.tracker, &staleCancelMu, &staleCancelFn)
		}()

		c.tracker.setConnected(true)
		c.tracker.setState("streaming")
		connectedAt := time.Now()

		barHandler := func(cb alpacastream.CryptoBar) {
			bar, err := CryptoBarToMarketBar(cb)
			if err != nil {
				return
			}
			c.tracker.recordBar()
			_ = handler(connCtx, bar)
		}

		tradeHandler := func(ct alpacastream.CryptoTrade) {
			if c.tradeHandler == nil {
				return
			}
			sym, err := domain.NewSymbol(ct.Symbol)
			if err != nil {
				return
			}
			sym = sym.ToSlashFormat()
			mt := domain.MarketTrade{
				Time:   ct.Timestamp,
				Symbol: sym,
				Price:  ct.Price,
				Size:   ct.Size,
			}
			_ = c.tradeHandler(connCtx, mt)
		}

		sc := alpacastream.NewCryptoClient(
			c.feed,
			alpacastream.WithCredentials(c.apiKey, c.apiSecret),
			alpacastream.WithCryptoBars(barHandler, symStrs...),
			alpacastream.WithCryptoTrades(tradeHandler, symStrs...),
		)

		var connErr error
		if err := sc.Connect(connCtx); err != nil {
			connErr = err
		} else {
			select {
			case err := <-sc.Terminated():
				connErr = err
			case <-connCtx.Done():
			}
		}

		connCancel()
		<-watchdogDone
		staleCancelMu.Lock()
		staleCancelFn = nil
		staleCancelMu.Unlock()

		c.tracker.setConnected(false)

		if streamCtx.Err() != nil {
			c.tracker.setState("stopped")
			return nil
		}

		c.tracker.recordError(connErr)
		errClass := classifyError(connErr)

		wasStaleReset := connCtx.Err() != nil && streamCtx.Err() == nil && connErr == nil
		if wasStaleReset {
			c.tracker.incStaleReset()
			c.log.Warn().Msg("stale feed watchdog triggered crypto reconnect")
			errClass = ErrTransient
		}

		c.tracker.cb.Record(errClass)

		if errClass == ErrFatal {
			c.log.Error().Err(connErr).Int("attempt", attempt).
				Msg("crypto stream fatal error — circuit breaker will gate retries")
			consecutiveFails++
			continue
		}

		// Reset fail counter if we streamed successfully for > 30s.
		if time.Since(connectedAt) > 30*time.Second {
			consecutiveFails = 0
		} else {
			consecutiveFails++
		}

		policy := selectPolicy()
		wait := policy.backoff(consecutiveFails)
		c.log.Warn().Err(connErr).Int("attempt", attempt).Dur("retry_in", wait).
			Msg("crypto stream disconnected, reconnecting")

		select {
		case <-time.After(wait):
		case <-streamCtx.Done():
			c.tracker.setState("stopped")
			return nil
		}
	}
}

// CryptoBarToMarketBar converts an Alpaca SDK CryptoBar to a domain.MarketBar.
func CryptoBarToMarketBar(cb alpacastream.CryptoBar) (domain.MarketBar, error) {
	sym, err := domain.NewSymbol(cb.Symbol)
	if err != nil {
		return domain.MarketBar{}, err
	}
	sym = sym.ToSlashFormat()
	tf, _ := domain.NewTimeframe("1m")
	return domain.NewMarketBar(cb.Timestamp, sym, tf, cb.Open, cb.High, cb.Low, cb.Close, cb.Volume)
}

// Close terminates the crypto WebSocket connection.
func (c *CryptoWSClient) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		cancel := c.cancel
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
	c.tracker.setState("stopped")
	return nil
}
