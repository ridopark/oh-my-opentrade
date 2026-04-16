package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

const (
	coinbaseWSURL        = "wss://ws-feed.exchange.coinbase.com"
	coinbaseMaxBackoff   = 60 * time.Second
	coinbaseInitBackoff  = 1 * time.Second
)

// coinbaseMatch is the JSON shape Coinbase sends for match events.
type coinbaseMatch struct {
	Type      string `json:"type"`       // "match" or "last_match"
	ProductID string `json:"product_id"` // "BTC-USD"
	Price     string `json:"price"`
	Size      string `json:"size"`
	Side      string `json:"side"` // maker side: "buy" or "sell"
	Time      string `json:"time"` // RFC3339
}

// ParseCoinbaseMatch converts raw JSON into a VenueTrade.
// Coinbase convention: `side` is the MAKER side.
// If side="buy", the taker is a SELLER. If side="sell", the taker is a BUYER.
func ParseCoinbaseMatch(data []byte) (VenueTrade, error) {
	var msg coinbaseMatch
	if err := json.Unmarshal(data, &msg); err != nil {
		return VenueTrade{}, fmt.Errorf("flow: parse coinbase match: %w", err)
	}
	if msg.Type != "match" && msg.Type != "last_match" {
		return VenueTrade{}, fmt.Errorf("flow: coinbase not a match message: type=%s", msg.Type)
	}

	price, err := strconv.ParseFloat(msg.Price, 64)
	if err != nil {
		return VenueTrade{}, fmt.Errorf("flow: parse coinbase price: %w", err)
	}
	size, err := strconv.ParseFloat(msg.Size, 64)
	if err != nil {
		return VenueTrade{}, fmt.Errorf("flow: parse coinbase size: %w", err)
	}

	// Invert maker side to get taker side. Unknown/empty side → "" so
	// the aggregator ignores unclassified trades rather than mis-signing.
	var takerSide string
	switch msg.Side {
	case "sell":
		takerSide = "buy" // maker is seller → taker is buyer
	case "buy":
		takerSide = "sell" // maker is buyer → taker is seller
	}

	ts, err := time.Parse(time.RFC3339Nano, msg.Time)
	if err != nil {
		ts = time.Now()
	}

	return VenueTrade{
		Venue:     "coinbase",
		Symbol:    NormalizeCoinbaseSymbol(msg.ProductID),
		Price:     price,
		Size:      size,
		TakerSide: takerSide,
		Timestamp: ts,
	}, nil
}

// CoinbaseDialFn abstracts the WebSocket dial for testing.
type CoinbaseDialFn func(ctx context.Context, url string) (*websocket.Conn, error)

// CoinbaseFeed connects to the Coinbase matches WebSocket stream and pumps
// trades into an Aggregator.
type CoinbaseFeed struct {
	url        string
	productIDs []string // e.g. "BTC-USD", "ETH-USD"
	agg        *Aggregator
	log        zerolog.Logger
	dialFn     CoinbaseDialFn

	mu      sync.Mutex
	backoff time.Duration
}

// NewCoinbaseFeed creates a feed for the given Coinbase product IDs.
func NewCoinbaseFeed(productIDs []string, agg *Aggregator, log zerolog.Logger) *CoinbaseFeed {
	return &CoinbaseFeed{
		url:        coinbaseWSURL,
		productIDs: productIDs,
		agg:        agg,
		log:        log.With().Str("component", "coinbase_feed").Logger(),
		dialFn:     defaultCoinbaseDial,
	}
}

func defaultCoinbaseDial(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose
	return conn, err
}

// Run connects, subscribes, and streams trades until ctx is canceled.
func (f *CoinbaseFeed) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := f.connectAndStream(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := f.nextBackoff()
		f.log.Warn().Err(err).Dur("retry_in", wait).Msg("coinbase feed disconnected, reconnecting")
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (f *CoinbaseFeed) connectAndStream(ctx context.Context) error {
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()

	conn, err := f.dialFn(dialCtx, f.url)
	if err != nil {
		return fmt.Errorf("flow: coinbase dial: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(256 * 1024)

	// Subscribe to matches channel.
	sub := struct {
		Type     string `json:"type"`
		Channels []struct {
			Name       string   `json:"name"`
			ProductIDs []string `json:"product_ids"`
		} `json:"channels"`
	}{
		Type: "subscribe",
		Channels: []struct {
			Name       string   `json:"name"`
			ProductIDs []string `json:"product_ids"`
		}{
			{Name: "matches", ProductIDs: f.productIDs},
		},
	}
	subBytes, _ := json.Marshal(sub)
	if err := conn.Write(ctx, websocket.MessageText, subBytes); err != nil {
		return fmt.Errorf("flow: coinbase subscribe: %w", err)
	}

	f.resetBackoff()
	f.log.Info().Strs("products", f.productIDs).Msg("coinbase feed connected")

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
			return fmt.Errorf("flow: coinbase read: %w", err)
		}

		trade, err := ParseCoinbaseMatch(msg)
		if err != nil {
			continue // subscription ack, heartbeat, etc.
		}

		f.agg.Ingest(trade)
	}
}

func (f *CoinbaseFeed) nextBackoff() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.backoff
	if b == 0 {
		b = coinbaseInitBackoff
	}
	f.backoff = time.Duration(math.Min(float64(b)*2, float64(coinbaseMaxBackoff)))
	return b
}

func (f *CoinbaseFeed) resetBackoff() {
	f.mu.Lock()
	f.backoff = coinbaseInitBackoff
	f.mu.Unlock()
}
