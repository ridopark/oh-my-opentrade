package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	tradeStreamPaperURL = "wss://paper-api.alpaca.markets/stream"
	tradeStreamLiveURL  = "wss://api.alpaca.markets/stream"

	maxReconnectBackoff = 60 * time.Second
	initialBackoff      = 1 * time.Second
	// Alpaca's trading stream is idle outside market hours and between
	// trade updates. The coder/websocket library handles ping/pong
	// keepalives automatically, so we rely on the parent context for
	// read cancellation instead of a per-read timeout.
)

type tradeStreamMsg struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

type tradeUpdateData struct {
	Event     string         `json:"event"`
	Price     string         `json:"price"`
	Qty       string         `json:"qty"`
	Timestamp string         `json:"timestamp"`
	Order     tradeOrderData `json:"order"`
}

type tradeOrderData struct {
	ID             string `json:"id"`
	ClientOrderID  string `json:"client_order_id"`
	FilledQty      string `json:"filled_qty"`
	FilledAvgPrice string `json:"filled_avg_price"`
	Status         string `json:"status"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
}

// DialFn abstracts the WebSocket dial so tests can inject a fake server.
type DialFn func(ctx context.Context, url string) (*websocket.Conn, error)

// TradeStreamClient connects to the Alpaca trading WebSocket and streams
// order updates (fills, cancellations, etc.) into a Go channel.
type TradeStreamClient struct {
	url       string
	apiKey    string
	apiSecret string
	log       zerolog.Logger
	dialFn    DialFn
	onConnect func(connected bool)

	mu        sync.Mutex
	connected bool
	backoff   time.Duration
}

func NewTradeStreamClient(baseURL, apiKey, apiSecret string, paperMode bool, log zerolog.Logger) *TradeStreamClient {
	url := tradeStreamLiveURL
	if paperMode {
		url = tradeStreamPaperURL
	}

	return &TradeStreamClient{
		url:       url,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		log:       log.With().Str("component", "trade_stream").Logger(),
		dialFn:    defaultDial,
	}
}

func defaultDial(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, url, nil) //nolint:bodyclose // websocket.Dial returns *websocket.Conn, not http.Response
	return conn, err
}

func (ts *TradeStreamClient) IsConnected() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.connected
}

func (ts *TradeStreamClient) SetOnConnect(fn func(connected bool)) {
	ts.onConnect = fn
}

func (ts *TradeStreamClient) setConnected(v bool) {
	ts.mu.Lock()
	ts.connected = v
	ts.mu.Unlock()
	if ts.onConnect != nil {
		ts.onConnect(v)
	}
}

func (ts *TradeStreamClient) resetBackoff() {
	ts.mu.Lock()
	ts.backoff = initialBackoff
	ts.mu.Unlock()
}

func (ts *TradeStreamClient) nextBackoff() time.Duration {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	b := ts.backoff
	if b == 0 {
		b = initialBackoff
	}
	ts.backoff = time.Duration(math.Min(float64(b)*2, float64(maxReconnectBackoff)))
	return b
}

// Run connects to the Alpaca trade stream and sends order updates to the
// provided channel. It reconnects with exponential backoff on failure.
// The channel is closed when ctx is canceled.
func (ts *TradeStreamClient) Run(ctx context.Context, ch chan<- ports.OrderUpdate) {
	defer close(ch)

	for {
		if ctx.Err() != nil {
			return
		}

		err := ts.connectAndStream(ctx, ch)
		ts.setConnected(false)

		if ctx.Err() != nil {
			return
		}

		wait := ts.nextBackoff()
		ts.log.Warn().Err(err).Dur("retry_in", wait).Msg("trade stream disconnected, reconnecting")

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (ts *TradeStreamClient) connectAndStream(ctx context.Context, ch chan<- ports.OrderUpdate) error {
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()

	conn, err := ts.dialFn(dialCtx, ts.url)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	conn.SetReadLimit(64 * 1024)

	if err := ts.authenticate(ctx, conn); err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	if err := ts.subscribe(ctx, conn); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	ts.setConnected(true)
	ts.resetBackoff()
	ts.log.Info().Str("url", ts.url).Msg("trade stream connected and subscribed")

	for {
		if ctx.Err() != nil {
			conn.Close(websocket.StatusNormalClosure, "shutdown")
			return nil
		}

		_, msgBytes, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}

		var msgs []tradeStreamMsg
		if err := json.Unmarshal(msgBytes, &msgs); err != nil {
			var single tradeStreamMsg
			if err2 := json.Unmarshal(msgBytes, &single); err2 != nil {
				ts.log.Debug().Str("raw", string(msgBytes)).Msg("unparseable trade stream message")
				continue
			}
			msgs = []tradeStreamMsg{single}
		}

		for _, msg := range msgs {
			if msg.Stream != "trade_updates" {
				continue
			}

			update, ok := ts.parseTradeUpdate(msg.Data)
			if !ok {
				continue
			}

			select {
			case ch <- update:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func (ts *TradeStreamClient) authenticate(ctx context.Context, conn *websocket.Conn) error {
	authMsg := map[string]any{
		"action": "authenticate",
		"data": map[string]string{
			"key_id":     ts.apiKey,
			"secret_key": ts.apiSecret,
		},
	}
	b, _ := json.Marshal(authMsg)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("write auth: %w", err)
	}

	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, respBytes, err := conn.Read(readCtx)
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}

	var resp struct {
		Stream string `json:"stream"`
		Data   struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBytes, &resp); err != nil {
		var arr []json.RawMessage
		if err2 := json.Unmarshal(respBytes, &arr); err2 == nil && len(arr) > 0 {
			_ = json.Unmarshal(arr[0], &resp)
		}
	}

	if resp.Data.Status == "unauthorized" {
		return fmt.Errorf("unauthorized: %s", resp.Data.Message)
	}

	ts.log.Debug().Str("status", resp.Data.Status).Msg("trade stream authenticated")
	return nil
}

func (ts *TradeStreamClient) subscribe(ctx context.Context, conn *websocket.Conn) error {
	subMsg := map[string]any{
		"action": "listen",
		"data": map[string]any{
			"streams": []string{"trade_updates"},
		},
	}
	b, _ := json.Marshal(subMsg)
	return conn.Write(ctx, websocket.MessageText, b)
}

func (ts *TradeStreamClient) parseTradeUpdate(data json.RawMessage) (ports.OrderUpdate, bool) {
	var tu tradeUpdateData
	if err := json.Unmarshal(data, &tu); err != nil {
		ts.log.Warn().Err(err).Msg("failed to parse trade update")
		return ports.OrderUpdate{}, false
	}

	qty, _ := strconv.ParseFloat(tu.Qty, 64)
	price, _ := strconv.ParseFloat(tu.Price, 64)
	filledQty, _ := strconv.ParseFloat(tu.Order.FilledQty, 64)
	filledAvgPrice, _ := strconv.ParseFloat(tu.Order.FilledAvgPrice, 64)

	var filledAt time.Time
	if tu.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, tu.Timestamp); err == nil {
			filledAt = t
		}
	}
	if filledAt.IsZero() {
		filledAt = time.Now().UTC()
	}

	return ports.OrderUpdate{
		BrokerOrderID:  tu.Order.ID,
		Event:          tu.Event,
		Qty:            qty,
		Price:          price,
		FilledQty:      filledQty,
		FilledAvgPrice: filledAvgPrice,
		FilledAt:       filledAt,
	}, true
}
