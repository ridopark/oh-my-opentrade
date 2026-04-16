package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

// WebSocket reconnection parameters.
const (
	wsMinBackoff    = 1 * time.Second
	wsMaxBackoff    = 60 * time.Second
	wsPingInterval  = 30 * time.Second
	wsReadLimit     = 1 << 20 // 1 MiB
)

// ──────────────────────────────────────────────────────────────────────────
// WebSocket message types
// ──────────────────────────────────────────────────────────────────────────

// WSTrade represents a trade from the trades channel.
type WSTrade struct {
	Coin  string    `json:"coin"`
	Side  string    `json:"side"` // "A" (ask/sell) or "B" (bid/buy)
	Price float64   `json:"-"`
	Size  float64   `json:"-"`
	Time  time.Time `json:"-"`
	Hash  string    `json:"hash"`
}

type wsTradeRaw struct {
	Coin  string `json:"coin"`
	Side  string `json:"side"`
	Px    string `json:"px"`
	Sz    string `json:"sz"`
	Time  int64  `json:"time"` // epoch millis
	Hash  string `json:"hash"`
}

func (w *WSTrade) fromRaw(r wsTradeRaw) {
	w.Coin = r.Coin
	w.Side = r.Side
	w.Price = parseFloat(r.Px)
	w.Size = parseFloat(r.Sz)
	w.Time = time.UnixMilli(r.Time)
	w.Hash = r.Hash
}

// WSL2Level represents a single price level in the order book.
type WSL2Level struct {
	Price float64
	Size  float64
	Count int
}

// WSL2Book represents an L2 order book snapshot from the l2Book channel.
type WSL2Book struct {
	Coin   string
	Bids   []WSL2Level
	Asks   []WSL2Level
	Time   time.Time
}

type wsL2BookRaw struct {
	Coin   string `json:"coin"`
	Time   int64  `json:"time"`
	Levels [][]wsL2LevelRaw `json:"levels"` // [bids, asks]
}

type wsL2LevelRaw struct {
	Px string `json:"px"`
	Sz string `json:"sz"`
	N  int    `json:"n"`
}

func (w *WSL2Book) fromRaw(r wsL2BookRaw) {
	w.Coin = r.Coin
	w.Time = time.UnixMilli(r.Time)
	if len(r.Levels) >= 1 {
		w.Bids = make([]WSL2Level, len(r.Levels[0]))
		for i, l := range r.Levels[0] {
			w.Bids[i] = WSL2Level{Price: parseFloat(l.Px), Size: parseFloat(l.Sz), Count: l.N}
		}
	}
	if len(r.Levels) >= 2 {
		w.Asks = make([]WSL2Level, len(r.Levels[1]))
		for i, l := range r.Levels[1] {
			w.Asks[i] = WSL2Level{Price: parseFloat(l.Px), Size: parseFloat(l.Sz), Count: l.N}
		}
	}
}

// WSFunding represents a funding rate update from the activeAssetCtx channel.
type WSFunding struct {
	Coin    string
	Rate    float64
	Premium float64
	Time    time.Time
}

// WSUserEvent represents a user-specific event (fills, liquidations, etc.).
type WSUserEvent struct {
	Fills []WSUserFill `json:"fills,omitempty"`
}

// WSUserFill represents a single fill from the userEvents channel.
type WSUserFill struct {
	Coin       string    `json:"coin"`
	Side       string    `json:"side"`     // "A" or "B"
	Price      float64   `json:"-"`
	Size       float64   `json:"-"`
	Time       time.Time `json:"-"`
	OID        int64     `json:"oid"`
	Hash       string    `json:"hash"`
	StartPosition float64 `json:"-"`
	ClosedPnl float64   `json:"-"`
	Fee        float64   `json:"-"`
}

type wsUserFillRaw struct {
	Coin          string `json:"coin"`
	Side          string `json:"side"`
	Px            string `json:"px"`
	Sz            string `json:"sz"`
	Time          int64  `json:"time"`
	OID           int64  `json:"oid"`
	Hash          string `json:"hash"`
	StartPosition string `json:"startPosition"`
	ClosedPnl     string `json:"closedPnl"`
	Fee           string `json:"fee"`
}

func (w *WSUserFill) fromRaw(r wsUserFillRaw) {
	w.Coin = r.Coin
	w.Side = r.Side
	w.Price = parseFloat(r.Px)
	w.Size = parseFloat(r.Sz)
	w.Time = time.UnixMilli(r.Time)
	w.OID = r.OID
	w.Hash = r.Hash
	w.StartPosition = parseFloat(r.StartPosition)
	w.ClosedPnl = parseFloat(r.ClosedPnl)
	w.Fee = parseFloat(r.Fee)
}

// ──────────────────────────────────────────────────────────────────────────
// Handler function types
// ──────────────────────────────────────────────────────────────────────────

// TradeHandlerFn handles incoming trade messages.
type TradeHandlerFn func(WSTrade)

// L2BookHandlerFn handles incoming L2 book updates.
type L2BookHandlerFn func(WSL2Book)

// FundingHandlerFn handles incoming funding rate updates.
type FundingHandlerFn func(WSFunding)

// UserEventHandlerFn handles incoming user events (fills, liquidations).
type UserEventHandlerFn func(WSUserEvent)

// ──────────────────────────────────────────────────────────────────────────
// WebSocket subscriber
// ──────────────────────────────────────────────────────────────────────────

// WSSubscriber manages a WebSocket connection to Hyperliquid, subscribes
// to channels, and dispatches messages to typed handlers. It automatically
// reconnects with exponential backoff on connection failures.
type WSSubscriber struct {
	wsURL   string
	address string // for userEvents subscription
	log     zerolog.Logger

	mu            sync.RWMutex
	tradeHandlers   []TradeHandlerFn
	l2BookHandlers  []L2BookHandlerFn
	fundingHandlers []FundingHandlerFn
	userHandlers    []UserEventHandlerFn

	subscriptions []wsSubscription

	// dialFn allows injection of a custom dialer for testing.
	dialFn func(ctx context.Context, url string) (*websocket.Conn, error)
}

type wsSubscription struct {
	Method       string `json:"method"`
	Subscription any    `json:"subscription"`
}

// NewWSSubscriber creates a new WebSocket subscriber.
func NewWSSubscriber(wsURL, address string, log zerolog.Logger) *WSSubscriber {
	return &WSSubscriber{
		wsURL:   wsURL,
		address: address,
		log:     log.With().Str("component", "hyperliquid_ws").Logger(),
		dialFn:  defaultDial,
	}
}

func defaultDial(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if conn != nil {
		conn.SetReadLimit(wsReadLimit)
	}
	return conn, err
}

// OnTrade registers a handler for trade messages.
func (ws *WSSubscriber) OnTrade(h TradeHandlerFn) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.tradeHandlers = append(ws.tradeHandlers, h)
}

// OnL2Book registers a handler for L2 order book updates.
func (ws *WSSubscriber) OnL2Book(h L2BookHandlerFn) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.l2BookHandlers = append(ws.l2BookHandlers, h)
}

// OnFunding registers a handler for funding rate updates.
func (ws *WSSubscriber) OnFunding(h FundingHandlerFn) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.fundingHandlers = append(ws.fundingHandlers, h)
}

// OnUserEvent registers a handler for user events.
func (ws *WSSubscriber) OnUserEvent(h UserEventHandlerFn) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.userHandlers = append(ws.userHandlers, h)
}

// SubscribeTrades adds a trades subscription for the given coin.
func (ws *WSSubscriber) SubscribeTrades(coin string) {
	ws.subscriptions = append(ws.subscriptions, wsSubscription{
		Method: "subscribe",
		Subscription: map[string]string{
			"type": "trades",
			"coin": coin,
		},
	})
}

// SubscribeL2Book adds an L2 book subscription for the given coin.
func (ws *WSSubscriber) SubscribeL2Book(coin string) {
	ws.subscriptions = append(ws.subscriptions, wsSubscription{
		Method: "subscribe",
		Subscription: map[string]string{
			"type": "l2Book",
			"coin": coin,
		},
	})
}

// SubscribeFunding adds an activeAssetCtx subscription for the given coin.
func (ws *WSSubscriber) SubscribeFunding(coin string) {
	ws.subscriptions = append(ws.subscriptions, wsSubscription{
		Method: "subscribe",
		Subscription: map[string]string{
			"type": "activeAssetCtx",
			"coin": coin,
		},
	})
}

// SubscribeUserEvents adds a userEvents subscription for the configured address.
func (ws *WSSubscriber) SubscribeUserEvents() {
	ws.subscriptions = append(ws.subscriptions, wsSubscription{
		Method: "subscribe",
		Subscription: map[string]string{
			"type": "userEvents",
			"user": ws.address,
		},
	})
}

// Run connects to the WebSocket and processes messages until ctx is canceled.
// It reconnects with exponential backoff on connection failures.
func (ws *WSSubscriber) Run(ctx context.Context) error {
	backoff := wsMinBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}

		started := time.Now()
		err := ws.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}

		// Reset backoff after a long-lived session so the next reconnect
		// doesn't inherit a stale escalated delay.
		if time.Since(started) > 5*time.Minute {
			backoff = wsMinBackoff
		}

		ws.log.Warn().Err(err).Dur("backoff", backoff).
			Msg("hyperliquid ws: connection lost, reconnecting")

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}

		backoff *= 2
		if backoff > wsMaxBackoff {
			backoff = wsMaxBackoff
		}
	}
}

// runOnce establishes a single WebSocket session, sends subscriptions, and
// reads messages until the connection fails or ctx is canceled.
func (ws *WSSubscriber) runOnce(ctx context.Context) error {
	conn, err := ws.dialFn(ctx, ws.wsURL)
	if err != nil {
		return fmt.Errorf("hyperliquid ws: dial: %w", err)
	}
	defer conn.CloseNow()

	// Send subscriptions.
	for _, sub := range ws.subscriptions {
		data, err := json.Marshal(sub)
		if err != nil {
			return fmt.Errorf("hyperliquid ws: marshal subscription: %w", err)
		}
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			return fmt.Errorf("hyperliquid ws: send subscription: %w", err)
		}
	}

	ws.log.Info().Int("subscriptions", len(ws.subscriptions)).
		Msg("hyperliquid ws: connected and subscribed")

	// Ping loop to keep the connection alive.
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go ws.pingLoop(pingCtx, conn)

	// Read loop.
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("hyperliquid ws: read: %w", err)
		}
		ws.dispatch(data)
	}
}

// pingLoop sends periodic pings to keep the connection alive.
func (ws *WSSubscriber) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingMsg := map[string]string{"method": "ping"}
			data, _ := json.Marshal(pingMsg)
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		}
	}
}

// dispatch parses a raw WebSocket message and routes it to the appropriate
// handler(s).
func (ws *WSSubscriber) dispatch(data []byte) {
	var envelope wsEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		// Pong responses and other non-channel messages are fine to ignore.
		return
	}

	if envelope.Channel == "" {
		return // subscription confirmation or pong
	}

	ws.mu.RLock()
	defer ws.mu.RUnlock()

	switch envelope.Channel {
	case "trades":
		ws.dispatchTrades(envelope.Data)
	case "l2Book":
		ws.dispatchL2Book(envelope.Data)
	case "activeAssetCtx":
		ws.dispatchFunding(envelope.Data)
	case "userEvents":
		ws.dispatchUserEvents(envelope.Data)
	}
}

type wsEnvelope struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

func (ws *WSSubscriber) dispatchTrades(data json.RawMessage) {
	if len(ws.tradeHandlers) == 0 {
		return
	}
	var rawTrades []wsTradeRaw
	if err := json.Unmarshal(data, &rawTrades); err != nil {
		ws.log.Warn().Err(err).Msg("hyperliquid ws: unmarshal trades")
		return
	}
	for _, rt := range rawTrades {
		var t WSTrade
		t.fromRaw(rt)
		for _, h := range ws.tradeHandlers {
			h(t)
		}
	}
}

func (ws *WSSubscriber) dispatchL2Book(data json.RawMessage) {
	if len(ws.l2BookHandlers) == 0 {
		return
	}
	var raw wsL2BookRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		ws.log.Warn().Err(err).Msg("hyperliquid ws: unmarshal l2Book")
		return
	}
	var book WSL2Book
	book.fromRaw(raw)
	for _, h := range ws.l2BookHandlers {
		h(book)
	}
}

func (ws *WSSubscriber) dispatchFunding(data json.RawMessage) {
	if len(ws.fundingHandlers) == 0 {
		return
	}
	// activeAssetCtx data is an assetCtx object with a coin field added.
	var raw struct {
		Coin    string `json:"coin"`
		Ctx     assetCtx `json:"ctx"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		ws.log.Warn().Err(err).Msg("hyperliquid ws: unmarshal funding")
		return
	}
	f := WSFunding{
		Coin:    raw.Coin,
		Rate:    parseFloat(raw.Ctx.Funding),
		Premium: parseFloat(raw.Ctx.Premium),
		Time:    time.Now(),
	}
	for _, h := range ws.fundingHandlers {
		h(f)
	}
}

func (ws *WSSubscriber) dispatchUserEvents(data json.RawMessage) {
	if len(ws.userHandlers) == 0 {
		return
	}
	// userEvents data can contain fills array.
	var rawEvent struct {
		Fills []wsUserFillRaw `json:"fills"`
	}
	if err := json.Unmarshal(data, &rawEvent); err != nil {
		ws.log.Warn().Err(err).Msg("hyperliquid ws: unmarshal userEvents")
		return
	}
	evt := WSUserEvent{
		Fills: make([]WSUserFill, len(rawEvent.Fills)),
	}
	for i, rf := range rawEvent.Fills {
		evt.Fills[i].fromRaw(rf)
	}
	for _, h := range ws.userHandlers {
		h(evt)
	}
}
