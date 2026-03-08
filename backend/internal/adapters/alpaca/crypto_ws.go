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
	feed          string     // e.g. "us"
	fetcher       BarFetcher // REST polling fallback; nil disables polling
	tradeHandler  ports.TradeHandler
	onDegraded    func(reason string)
	log           zerolog.Logger
	tracker       *feedTracker
	closeOnce     sync.Once
	cancel        context.CancelFunc
	mu            sync.Mutex
}

// NewCryptoWSClient creates a new CryptoWSClient.
// cryptoDataURL is the base WebSocket URL (e.g. "wss://stream.data.alpaca.markets").
// feed is the crypto feed name (e.g. "us").
func NewCryptoWSClient(cryptoDataURL, apiKey, apiSecret, feed string, fetcher BarFetcher, log zerolog.Logger) (*CryptoWSClient, error) {
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
		fetcher:       fetcher,
		log:           log.With().Str("component", "crypto_ws").Logger(),
		tracker:       newFeedTracker(),
	}, nil
}

func (c *CryptoWSClient) SetTradeHandler(h ports.TradeHandler) { c.tradeHandler = h }

func (c *CryptoWSClient) SetDegradedCallback(fn func(reason string)) { c.onDegraded = fn }

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

	const cryptoRestPollInterval = 60 * time.Second

	var dedupMu sync.Mutex
	dedup := make(map[string]struct{})

	lastBarTime := make(map[domain.Symbol]time.Time)
	var lastBarMu sync.Mutex

	callHandler := func(bCtx context.Context, bar domain.MarketBar, fromREST bool) error {
		key := barKey(bar)
		dedupMu.Lock()
		if _, seen := dedup[key]; seen {
			dedupMu.Unlock()
			return nil
		}
		dedup[key] = struct{}{}
		if len(dedup) > maxDedupEntries {
			dedup = make(map[string]struct{})
		}
		dedupMu.Unlock()

		lastBarMu.Lock()
		if bar.Time.After(lastBarTime[bar.Symbol]) {
			lastBarTime[bar.Symbol] = bar.Time
		}
		lastBarMu.Unlock()

		c.tracker.recordBar()
		return handler(bCtx, bar)
	}

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

		c.tracker.resetBarTimer()

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
			_ = callHandler(connCtx, bar, false)
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

		if time.Since(connectedAt) > 30*time.Second {
			consecutiveFails = 0
		} else {
			consecutiveFails++
		}

		var pollCancel context.CancelFunc
		if wasStaleReset && c.fetcher != nil {
			dedupMu.Lock()
			dedup = make(map[string]struct{})
			dedupMu.Unlock()

			if c.onDegraded != nil {
				c.onDegraded("crypto WebSocket stale — falling back to REST polling")
			}

			pollCtx, pCancel := context.WithCancel(streamCtx)
			pollCancel = pCancel
			go c.cryptoRestPoller(pollCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler, cryptoRestPollInterval)
		}

		policy := selectPolicy()
		wait := policy.backoff(consecutiveFails)
		c.log.Warn().Err(connErr).Int("attempt", attempt).Dur("retry_in", wait).
			Msg("crypto stream disconnected, reconnecting")

		select {
		case <-time.After(wait):
		case <-streamCtx.Done():
			if pollCancel != nil {
				pollCancel()
			}
			c.tracker.setState("stopped")
			return nil
		}

		if pollCancel != nil {
			pollCancel()
		}

		// Gap-fill: catch bars between last REST poll and WS resume.
		if c.fetcher != nil {
			c.cryptoGapFill(streamCtx, symbols, domain.Timeframe("1m"), lastBarTime, &lastBarMu, &dedupMu, dedup, callHandler)
		}
	}
}

func (c *CryptoWSClient) cryptoRestPoller(
	ctx context.Context,
	symbols []domain.Symbol,
	timeframe domain.Timeframe,
	lastBarTime map[domain.Symbol]time.Time,
	lastBarMu *sync.Mutex,
	dedupMu *sync.Mutex,
	dedup map[string]struct{},
	callHandler func(context.Context, domain.MarketBar, bool) error,
	pollInterval time.Duration,
) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	pollOnce := func() {
		now := time.Now()
		for _, sym := range symbols {
			lastBarMu.Lock()
			from := lastBarTime[sym]
			lastBarMu.Unlock()

			if from.IsZero() {
				from = now.Add(-2 * pollInterval)
			} else {
				from = from.Add(time.Second)
			}

			bars, err := c.fetcher(ctx, sym, timeframe, from, now)
			if err != nil {
				if ctx.Err() == nil {
					c.log.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto REST poller: fetch failed")
				}
				continue
			}

			for _, bar := range bars {
				if err := callHandler(ctx, bar, true); err != nil {
					if ctx.Err() == nil {
						c.log.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto REST poller: handler error")
					}
				}
			}
		}
	}

	pollOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollOnce()
		}
	}
}

func (c *CryptoWSClient) cryptoGapFill(
	ctx context.Context,
	symbols []domain.Symbol,
	timeframe domain.Timeframe,
	lastBarTime map[domain.Symbol]time.Time,
	lastBarMu *sync.Mutex,
	dedupMu *sync.Mutex,
	dedup map[string]struct{},
	callHandler func(context.Context, domain.MarketBar, bool) error,
) {
	now := time.Now()
	for _, sym := range symbols {
		lastBarMu.Lock()
		from := lastBarTime[sym]
		lastBarMu.Unlock()

		if from.IsZero() {
			continue
		}
		from = from.Add(time.Second)

		bars, err := c.fetcher(ctx, sym, timeframe, from, now)
		if err != nil {
			if ctx.Err() == nil {
				c.log.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto gap-fill: fetch failed")
			}
			continue
		}
		for _, bar := range bars {
			if err := callHandler(ctx, bar, true); err != nil {
				if ctx.Err() == nil {
					c.log.Warn().Err(err).Str("symbol", string(sym)).Msg("crypto gap-fill: handler error")
				}
			}
		}
	}
}

func CryptoBarToMarketBar(cb alpacastream.CryptoBar) (domain.MarketBar, error) {
	sym, err := domain.NewSymbol(cb.Symbol)
	if err != nil {
		return domain.MarketBar{}, err
	}
	sym = sym.ToSlashFormat()
	tf, _ := domain.NewTimeframe("1m")
	bar, err := domain.NewMarketBar(cb.Timestamp, sym, tf, cb.Open, cb.High, cb.Low, cb.Close, cb.Volume)
	if err != nil {
		return bar, err
	}
	bar.TradeCount = cb.TradeCount
	return bar, nil
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
