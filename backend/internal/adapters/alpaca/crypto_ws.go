package alpaca

import (
	"context"
	"errors"
	"sync"

	alpacastream "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// CryptoWSClient handles WebSocket connections for Alpaca crypto market data.
// It uses a separate stream.CryptoClient (not the StocksClient used for equities).
type CryptoWSClient struct {
	cryptoDataURL string
	apiKey        string
	apiSecret     string
	feed          string // e.g. "us"
	tradeHandler  ports.TradeHandler
	closeOnce     sync.Once
	cancel        context.CancelFunc
	mu            sync.Mutex
}

// NewCryptoWSClient creates a new CryptoWSClient.
// cryptoDataURL is the base WebSocket URL (e.g. "wss://stream.data.alpaca.markets").
// feed is the crypto feed name (e.g. "us").
func NewCryptoWSClient(cryptoDataURL, apiKey, apiSecret, feed string) (*CryptoWSClient, error) {
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
	}, nil
}

// SetTradeHandler sets the callback for forwarding raw crypto trade ticks.
func (c *CryptoWSClient) SetTradeHandler(h ports.TradeHandler) { c.tradeHandler = h }

// ErrCryptoWSMissingCredentials is returned when API key or secret is empty.
var ErrCryptoWSMissingCredentials = errors.New("crypto websocket requires API key and secret")

// StreamBars connects to the Alpaca crypto WebSocket and streams bars for the
// requested symbols. It blocks until ctx is cancelled or the connection terminates.
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

	barHandler := func(cb alpacastream.CryptoBar) {
		bar, err := CryptoBarToMarketBar(cb)
		if err != nil {
			return
		}
		_ = handler(streamCtx, bar)
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
		_ = c.tradeHandler(streamCtx, mt)
	}

	sc := alpacastream.NewCryptoClient(
		c.feed,
		alpacastream.WithCredentials(c.apiKey, c.apiSecret),
		alpacastream.WithCryptoBars(barHandler, symStrs...),
		alpacastream.WithCryptoTrades(tradeHandler, symStrs...),
	)

	if err := sc.Connect(streamCtx); err != nil {
		return err
	}

	// Block until stream terminates or context is cancelled.
	select {
	case err := <-sc.Terminated():
		return err
	case <-streamCtx.Done():
		return nil
	}
}

// CryptoBarToMarketBar converts an Alpaca SDK CryptoBar to a domain.MarketBar.
func CryptoBarToMarketBar(cb alpacastream.CryptoBar) (domain.MarketBar, error) {
	sym, err := domain.NewSymbol(cb.Symbol)
	if err != nil {
		return domain.MarketBar{}, err
	}
	// Normalize to slash format (SDK may return "BTCUSD" or "BTC/USD").
	sym = sym.ToSlashFormat()

	// Crypto bars are always 1-minute from the WebSocket stream.
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
	return nil
}
