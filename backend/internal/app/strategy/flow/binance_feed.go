package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

const (
	binanceWSURL        = "wss://stream.binance.com:9443/ws"
	binanceMaxBackoff   = 60 * time.Second
	binanceInitBackoff  = 1 * time.Second
)

// binanceAggTrade is the JSON shape Binance sends for aggTrade events.
type binanceAggTrade struct {
	EventType    string `json:"e"` // "aggTrade"
	Symbol       string `json:"s"` // "BTCUSDT"
	Price        string `json:"p"`
	Quantity     string `json:"q"`
	IsBuyerMaker bool   `json:"m"` // true = buyer is maker, taker is SELLER
	TradeTime    int64  `json:"T"` // ms epoch
}

// ParseBinanceAggTrade converts raw JSON into a VenueTrade.
// Binance convention: m=true means buyer is maker, so taker is seller.
func ParseBinanceAggTrade(data []byte) (VenueTrade, error) {
	var msg binanceAggTrade
	if err := json.Unmarshal(data, &msg); err != nil {
		return VenueTrade{}, fmt.Errorf("flow: parse binance aggTrade: %w", err)
	}
	price, err := strconv.ParseFloat(msg.Price, 64)
	if err != nil {
		return VenueTrade{}, fmt.Errorf("flow: parse binance price: %w", err)
	}
	qty, err := strconv.ParseFloat(msg.Quantity, 64)
	if err != nil {
		return VenueTrade{}, fmt.Errorf("flow: parse binance qty: %w", err)
	}

	side := "buy"
	if msg.IsBuyerMaker {
		side = "sell" // taker is seller when buyer is maker
	}

	return VenueTrade{
		Venue:     "binance",
		Symbol:    NormalizeBinanceSymbol(msg.Symbol),
		Price:     price,
		Size:      qty,
		TakerSide: side,
		Timestamp: time.UnixMilli(msg.TradeTime),
	}, nil
}

// BinanceDialFn abstracts the WebSocket dial so tests can inject a fake server.
type BinanceDialFn func(ctx context.Context, url string) (*websocket.Conn, error)

// BinanceFeed connects to the Binance aggTrade WebSocket stream and pumps
// trades into an Aggregator.
type BinanceFeed struct {
	url     string
	symbols []string // lowercase binance symbols, e.g. "btcusdt"
	agg     *Aggregator
	log     zerolog.Logger
	dialFn  BinanceDialFn

	mu      sync.Mutex
	backoff time.Duration
}

// NewBinanceFeed creates a feed for the given Binance-style symbol list.
func NewBinanceFeed(symbols []string, agg *Aggregator, log zerolog.Logger) *BinanceFeed {
	lower := make([]string, len(symbols))
	for i, s := range symbols {
		lower[i] = strings.ToLower(s)
	}
	return &BinanceFeed{
		url:     binanceWSURL,
		symbols: lower,
		agg:     agg,
		log:     log.With().Str("component", "binance_feed").Logger(),
		dialFn:  defaultBinanceDial,
	}
}

func defaultBinanceDial(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose
	return conn, err
}

// Run connects, subscribes, and streams trades until ctx is canceled.
// Reconnects with exponential backoff on failure.
func (f *BinanceFeed) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := f.connectAndStream(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := f.nextBackoff()
		f.log.Warn().Err(err).Dur("retry_in", wait).Msg("binance feed disconnected, reconnecting")
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (f *BinanceFeed) connectAndStream(ctx context.Context) error {
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()

	conn, err := f.dialFn(dialCtx, f.url)
	if err != nil {
		return fmt.Errorf("flow: binance dial: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(256 * 1024)

	// Subscribe to aggTrade streams.
	params := make([]string, len(f.symbols))
	for i, s := range f.symbols {
		params[i] = s + "@aggTrade"
	}
	sub := struct {
		Method string   `json:"method"`
		Params []string `json:"params"`
		ID     int      `json:"id"`
	}{
		Method: "SUBSCRIBE",
		Params: params,
		ID:     1,
	}
	subBytes, _ := json.Marshal(sub)
	if err := conn.Write(ctx, websocket.MessageText, subBytes); err != nil {
		return fmt.Errorf("flow: binance subscribe: %w", err)
	}

	f.resetBackoff()
	f.log.Info().Strs("symbols", f.symbols).Msg("binance feed connected")

	for {
		if ctx.Err() != nil {
			conn.Close(websocket.StatusNormalClosure, "shutdown")
			return nil
		}

		_, msg, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("flow: binance read: %w", err)
		}

		trade, err := ParseBinanceAggTrade(msg)
		if err != nil {
			// Subscription ack or other non-trade messages are expected; skip silently.
			continue
		}
		if trade.Symbol == "" {
			continue
		}

		f.agg.Ingest(trade)
	}
}

func (f *BinanceFeed) nextBackoff() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.backoff
	if b == 0 {
		b = binanceInitBackoff
	}
	f.backoff = time.Duration(math.Min(float64(b)*2, float64(binanceMaxBackoff)))
	return b
}

func (f *BinanceFeed) resetBackoff() {
	f.mu.Lock()
	f.backoff = binanceInitBackoff
	f.mu.Unlock()
}
