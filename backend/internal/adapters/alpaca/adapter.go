package alpaca

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const defaultRateLimit = 200

// Adapter implements both market data and broker interfaces for Alpaca.
type Adapter struct {
	rest        *RESTClient
	ws          *WSClient
	cryptoWs    *CryptoWSClient
	tradeStream *TradeStreamClient
	dataURL     string
	posC        *positionCache
	log         zerolog.Logger
}

var _ ports.SnapshotPort = (*Adapter)(nil)

// NewAdapter creates a new Alpaca Adapter.
func NewAdapter(cfg config.AlpacaConfig, log zerolog.Logger) (*Adapter, error) {
	if cfg.APIKeyID == "" {
		return nil, errors.New("APIKeyID is required")
	}
	if cfg.APISecretKey == "" {
		return nil, errors.New("APISecretKey is required")
	}

	limiter := NewPriorityRateLimiter(defaultRateLimit, 120)
	restLog := log.With().Str("client", "rest").Logger()
	rest := NewRESTClient(cfg.BaseURL, cfg.APIKeyID, cfg.APISecretKey, limiter, restLog)
	rest.feed = cfg.Feed

	dataURL := cfg.DataURL
	if dataURL == "" {
		dataURL = "https://data.alpaca.markets"
	}

	dataREST := NewRESTClient(cfg.BaseURL, cfg.APIKeyID, cfg.APISecretKey, limiter, restLog)
	dataREST.feed = cfg.Feed
	fetcher := func(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
		if symbol.IsCryptoSymbol() {
			return dataREST.GetCryptoHistoricalBars(ctx, dataURL, symbol, timeframe, from, to)
		}
		return dataREST.GetHistoricalBars(ctx, dataURL, symbol, timeframe, from, to)
	}
	ws := NewWSClient(cfg.DataURL, cfg.APIKeyID, cfg.APISecretKey, cfg.Feed, fetcher)

	cryptoWs, err := NewCryptoWSClient(cfg.CryptoDataURL, cfg.APIKeyID, cfg.APISecretKey, cfg.CryptoFeed, fetcher, log)
	if err != nil {
		return nil, fmt.Errorf("create crypto WS client: %w", err)
	}

	tradeStream := NewTradeStreamClient(cfg.BaseURL, cfg.APIKeyID, cfg.APISecretKey, cfg.PaperMode, log)

	return &Adapter{
		rest:        rest,
		ws:          ws,
		cryptoWs:    cryptoWs,
		tradeStream: tradeStream,
		dataURL:     dataURL,
		posC:        newPositionCache(2 * time.Second),
		log:         log,
	}, nil
}

// StreamBars starts streaming market data bars for the requested symbols.
// Crypto symbols are dispatched to the CryptoWSClient; equity symbols to the StocksClient.
func (a *Adapter) StreamBars(ctx context.Context, symbols []domain.Symbol, timeframe domain.Timeframe, handler ports.BarHandler) error {
	var equitySyms, cryptoSyms []domain.Symbol
	for _, s := range symbols {
		if s.IsCryptoSymbol() {
			cryptoSyms = append(cryptoSyms, s)
		} else {
			equitySyms = append(equitySyms, s)
		}
	}

	errCh := make(chan error, 2)

	if len(cryptoSyms) > 0 {
		go func() {
			errCh <- a.cryptoWs.StreamBars(ctx, cryptoSyms, timeframe, handler)
		}()
	} else {
		errCh <- nil
	}

	if len(equitySyms) > 0 {
		go func() {
			errCh <- a.ws.StreamBars(ctx, equitySyms, timeframe, handler)
		}()
	} else {
		errCh <- nil
	}

	// Wait for both streams to finish; return the first non-nil error.
	var firstErr error
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// GetHistoricalBars fetches historical OHLCV bars from the Alpaca data API.
// Crypto symbols are routed to the crypto endpoint; equity symbols to the stocks endpoint.
func (a *Adapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	if symbol.IsCryptoSymbol() {
		return a.rest.GetCryptoHistoricalBars(ctx, a.dataURL, symbol, timeframe, from, to)
	}
	return a.rest.GetHistoricalBars(ctx, a.dataURL, symbol, timeframe, from, to)
}

func (a *Adapter) GetSnapshots(ctx context.Context, symbols []string, asOf time.Time) (map[string]ports.Snapshot, error) {
	var equitySyms, cryptoSyms []string
	for _, s := range symbols {
		if strings.Contains(s, "/") {
			cryptoSyms = append(cryptoSyms, s)
		} else {
			equitySyms = append(equitySyms, s)
		}
	}

	out := make(map[string]ports.Snapshot, len(symbols))

	if len(equitySyms) > 0 {
		snaps, err := a.rest.GetSnapshots(ctx, a.dataURL, equitySyms)
		if err != nil {
			a.log.Warn().Err(err).Int("count", len(equitySyms)).Msg("equity snapshots failed")
		} else {
			for k, v := range snaps {
				out[k] = v
			}
		}
	}

	if len(cryptoSyms) > 0 {
		snaps, err := a.rest.GetCryptoSnapshot(ctx, a.dataURL, cryptoSyms)
		if err != nil {
			a.log.Warn().Err(err).Int("count", len(cryptoSyms)).Msg("crypto snapshots failed")
		} else {
			for k, v := range snaps {
				out[k] = v
			}
		}
	}

	return out, nil
}

func (a *Adapter) ListTradeable(ctx context.Context, assetClass domain.AssetClass) ([]ports.Asset, error) {
	return a.rest.ListTradeable(ctx, assetClass)
}

// Close safely closes the adapter and underlying connections.
func (a *Adapter) Close() error {
	var errs []error
	if err := a.ws.Close(); err != nil {
		errs = append(errs, err)
	}
	if a.cryptoWs != nil {
		if err := a.cryptoWs.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SubscribeOrderUpdates returns a channel that streams real-time order
// updates from Alpaca's trading WebSocket. Satisfies ports.OrderStreamPort.
func (a *Adapter) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
	ch := make(chan ports.OrderUpdate, 64)
	go a.tradeStream.Run(ctx, ch)
	return ch, nil
}

// TradeStream returns the underlying TradeStreamClient for metrics wiring.
func (a *Adapter) TradeStream() *TradeStreamClient { return a.tradeStream }

// SubmitOrder places an order through the Alpaca REST API.
// Crypto orders are validated (long-only) before submission.
// Options orders are dispatched to SubmitOptionOrder.
func (a *Adapter) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	// Crypto guard: reject short selling for crypto assets.
	if intent.AssetClass == domain.AssetClassCrypto {
		if intent.Direction == domain.DirectionShort {
			return "", errors.New("crypto does not support short selling")
		}
		// Ensure symbol is in slash format for the Alpaca API.
		intent.Symbol = intent.Symbol.ToSlashFormat()
	}

	if intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption {
		id, err := a.rest.SubmitOptionOrder(ctx, intent)
		if err == nil {
			if a.posC != nil {
				a.posC.Invalidate()
			}
		}
		return id, err
	}
	id, err := a.rest.SubmitOrder(ctx, intent)
	if err == nil {
		if a.posC != nil {
			a.posC.Invalidate()
		}
	}
	return id, err
}

// CancelOrder requests cancellation of an existing Alpaca order.
func (a *Adapter) CancelOrder(ctx context.Context, orderID string) error {
	return a.rest.CancelOrder(ctx, orderID)
}

// CancelOpenOrders cancels all open orders for a symbol and side on Alpaca.
func (a *Adapter) CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error) {
	return a.rest.CancelOpenOrders(ctx, symbol, side)
}

// GetOrderStatus fetches the current status of an Alpaca order.
func (a *Adapter) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	return a.rest.GetOrderStatus(ctx, orderID)
}

// GetPositions retrieves currently open positions from Alpaca.
func (a *Adapter) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	if a.posC != nil {
		if pos, ok := a.posC.Get(tenantID, envMode); ok {
			return pos, nil
		}
	}

	pos, err := a.rest.GetPositions(ctx, tenantID, envMode)
	if err != nil {
		return nil, err
	}
	if a.posC != nil {
		a.posC.Set(tenantID, envMode, pos)
	}
	return pos, nil
}

func (a *Adapter) GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error) {
	return a.rest.GetPosition(ctx, symbol)
}

func (a *Adapter) ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error) {
	return a.rest.ClosePosition(ctx, symbol)
}

func (a *Adapter) GetOrderDetails(ctx context.Context, orderID string) (ports.OrderDetails, error) {
	return a.rest.GetOrderDetails(ctx, orderID)
}

func (a *Adapter) GetQuote(ctx context.Context, symbol domain.Symbol) (float64, float64, error) {
	if symbol.IsCryptoSymbol() {
		return a.rest.GetCryptoQuote(ctx, a.dataURL, symbol)
	}
	return a.rest.GetQuote(ctx, a.dataURL, symbol)
}

// GetOptionChain fetches option contract snapshots for the given underlying, expiry, and right.
func (a *Adapter) GetOptionChain(ctx context.Context, underlying domain.Symbol, expiry time.Time, right domain.OptionRight) ([]domain.OptionContractSnapshot, error) {
	return a.rest.GetOptionChain(ctx, underlying, expiry, right)
}

// GetAccountEquity fetches the current paper account equity from Alpaca.
func (a *Adapter) GetAccountEquity(ctx context.Context) (float64, error) {
	return a.rest.GetAccountEquity(ctx)
}

// GetAccountBuyingPower fetches DTBP, effective buying power, and PDT flag from Alpaca.
// Satisfies ports.AccountPort.
func (a *Adapter) GetAccountBuyingPower(ctx context.Context) (ports.BuyingPower, error) {
	bp, err := a.rest.GetAccountBuyingPower(ctx)
	if err != nil {
		return ports.BuyingPower{}, err
	}
	return ports.BuyingPower{
		DayTradingBuyingPower:    bp.DayTradingBuyingPower,
		EffectiveBuyingPower:     bp.EffectiveBuyingPower,
		NonMarginableBuyingPower: bp.NonMarginableBuyingPower,
		PatternDayTrader:         bp.PatternDayTrader,
	}, nil
}

// SetTradeHandler sets the trade tick callback on both equity and crypto WS clients.
func (a *Adapter) SetTradeHandler(h ports.TradeHandler) {
	a.ws.SetTradeHandler(h)
	if a.cryptoWs != nil {
		a.cryptoWs.SetTradeHandler(h)
	}
}

// SubscribeSymbols dynamically adds symbols to the active WebSocket stream.
// Equity symbols route to the StocksClient; crypto symbols are no-ops (crypto
// streams already cover all symbols in the feed).
func (a *Adapter) SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error {
	var equitySyms []domain.Symbol
	for _, s := range symbols {
		if !s.IsCryptoSymbol() {
			equitySyms = append(equitySyms, s)
		}
	}
	if len(equitySyms) == 0 {
		return nil
	}
	return a.ws.SubscribeSymbols(ctx, equitySyms)
}

func (a *Adapter) WSClient() *WSClient { return a.ws }

func (a *Adapter) CryptoWSClient() *CryptoWSClient { return a.cryptoWs }

// GetClosedOrders fetches all closed orders from Alpaca within the given time range.
func (a *Adapter) GetClosedOrders(ctx context.Context, after, until time.Time) ([]ClosedOrder, error) {
	return a.rest.GetClosedOrders(ctx, after, until)
}
