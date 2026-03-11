package alpaca

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	headerAPIKey    = "APCA-API-KEY-ID"
	headerAPISecret = "APCA-API-SECRET-KEY"
	pathOrders      = "/v2/orders"
	pathPositions   = "/v2/positions"
	pathStocks      = "/v2/stocks/"
	httpTimeout     = 10 * time.Second
)

// RESTClient handles standard HTTP API calls to Alpaca.
type RESTClient struct {
	baseURL   string
	apiKey    string
	apiSecret string
	feed      string
	limiter   *RateLimiter
	client    *http.Client
	log       zerolog.Logger
}

func (c *RESTClient) equityFeed() string {
	if c.feed != "" {
		return c.feed
	}
	return "iex"
}

type reqOpts struct {
	priority   RequestPriority
	maxRetries int
}

func normalizeReqOpts(opts reqOpts) reqOpts {
	if opts.maxRetries <= 0 {
		if opts.priority == PriorityBackground {
			opts.maxRetries = 1
		} else {
			opts.maxRetries = 3
		}
	}
	return opts
}

func randUnitFloat64() float64 {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return 0
	}
	v := binary.LittleEndian.Uint64(b[:])
	return float64(v) / (float64(^uint64(0)) + 1)
}

func jitterDuration(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(randUnitFloat64() * float64(max))
}

func (c *RESTClient) doReqDataAPI(ctx context.Context, dataURL string, method, path string, body io.Reader, opts reqOpts) (*http.Response, error) {
	fullURL := strings.TrimSuffix(dataURL, "/") + path
	return c.doReqFullWithPath(ctx, method, fullURL, path, body, opts)
}

// NewRESTClient constructs a new RESTClient with proper rate limiting.
func NewRESTClient(baseURL string, apiKey string, apiSecret string, limiter *RateLimiter, log zerolog.Logger) *RESTClient {
	return &RESTClient{
		baseURL:   baseURL,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		limiter:   limiter,
		client:    &http.Client{Timeout: httpTimeout},
		log:       log,
	}
}

func (c *RESTClient) doReq(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	return c.doReqWithOpts(ctx, method, path, body, reqOpts{priority: PriorityTrading})
}

func (c *RESTClient) doReqWithOpts(ctx context.Context, method, path string, body io.Reader, opts reqOpts) (*http.Response, error) {
	urlStr := strings.TrimSuffix(c.baseURL, "/") + path
	return c.doReqFullWithPath(ctx, method, urlStr, path, body, opts)
}

func (c *RESTClient) doReqFullWithPath(ctx context.Context, method, fullURL, pathForLog string, body io.Reader, opts reqOpts) (*http.Response, error) {
	opts = normalizeReqOpts(opts)

	var bodyBytes []byte
	if body != nil {
		b, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		bodyBytes = b
	}

	for attempt := 0; ; attempt++ {
		if err := c.limiter.WaitWithPriority(ctx, opts.priority); err != nil {
			return nil, err
		}

		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
		if err != nil {
			return nil, err
		}
		req.Header.Set(headerAPIKey, c.apiKey)
		req.Header.Set(headerAPISecret, c.apiSecret)
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if attempt >= opts.maxRetries {
			return resp, nil
		}

		logPath := pathForLog
		if logPath == "" {
			logPath = req.URL.Path
		}

		remainingRaw := strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining"))
		resetRaw := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset"))

		resetIn := time.Duration(0)
		if resetRaw != "" {
			if ts, parseErr := strconv.ParseInt(resetRaw, 10, 64); parseErr == nil {
				until := time.Until(time.Unix(ts, 0))
				if until > 0 && until < 60*time.Second {
					resetIn = until
				}
			}
		}

		sleep := time.Duration(0)
		if resetIn > 0 {
			sleep = resetIn + jitterDuration(250*time.Millisecond)
		} else {
			base := time.Second * time.Duration(math.Pow(2, float64(attempt)))
			if base > 30*time.Second {
				base = 30 * time.Second
			}
			sleep = jitterDuration(base)
		}

		evt := c.log.Warn().Str("priority", opts.priority.String()).Str("path", logPath)
		if remainingRaw != "" {
			if remainingInt, parseErr := strconv.Atoi(remainingRaw); parseErr == nil {
				evt = evt.Int("remaining", remainingInt)
			} else {
				evt = evt.Str("remaining", remainingRaw)
			}
		}
		if resetIn > 0 {
			evt = evt.Dur("reset_in", resetIn)
		}
		evt.
			Str("attempt", fmt.Sprintf("%d/%d", attempt+1, opts.maxRetries)).
			Dur("sleep", sleep).
			Msg("rate limit hit — retrying")

		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		t := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			if !t.Stop() {
				<-t.C
			}
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// SubmitOrder submits a new order to the Alpaca REST API.
func (c *RESTClient) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	side := "sell"
	if intent.Direction == domain.DirectionLong {
		side = "buy"
	}

	// Determine order type: respect intent override, else default to "limit".
	orderType := intent.OrderType
	if orderType == "" {
		orderType = "limit"
	}

	tif := intent.TimeInForce
	if tif == "" {
		tif = "gtc"
	}
	isFractional := intent.Quantity != math.Floor(intent.Quantity)
	if isFractional && !intent.Symbol.IsCryptoSymbol() {
		if tif != "day" {
			c.log.Info().
				Str("symbol", intent.Symbol.String()).
				Str("original_tif", tif).
				Float64("qty", intent.Quantity).
				Msg("overriding TIF to day for fractional equity order")
		}
		tif = "day"
	}

	reqBody := map[string]interface{}{
		"symbol":        intent.Symbol.String(),
		"qty":           fmt.Sprintf("%g", intent.Quantity),
		"side":          side,
		"type":          orderType,
		"time_in_force": tif,
	}
	if orderType != "market" {
		reqBody["limit_price"] = roundPrice(intent.LimitPrice)
	}

	b, _ := json.Marshal(reqBody)
	resp, err := c.doReqWithOpts(ctx, http.MethodPost, pathOrders, bytes.NewReader(b), reqOpts{priority: PriorityTrading, maxRetries: 3})
	if err != nil {
		c.log.Error().Err(err).Str("symbol", intent.Symbol.String()).Msg("submit order HTTP request failed")
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Error().
			Int("status", resp.StatusCode).
			Str("symbol", intent.Symbol.String()).
			Str("response", string(body)).
			Msg("submit order rejected by Alpaca")
		return "", fmt.Errorf("alpaca: submit order failed (status %d): %s", resp.StatusCode, string(body))
	}
	// We read the body into a buffer so we can still decode it
	respBody := bytes.NewReader(body)

	var res struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(respBody).Decode(&res); err != nil {
		return "", err
	}
	c.log.Info().
		Str("symbol", intent.Symbol.String()).
		Str("side", side).
		Str("broker_order_id", res.ID).
		Msg("order submitted successfully")
	return res.ID, nil
}

// CancelOrder requests cancellation of a specific order by ID.
func (c *RESTClient) CancelOrder(ctx context.Context, orderID string) error {
	resp, err := c.doReqWithOpts(ctx, http.MethodDelete, pathOrders+"/"+orderID, nil, reqOpts{priority: PriorityTrading, maxRetries: 3})

	if err != nil {
		c.log.Error().Err(err).Str("order_id", orderID).Msg("cancel order HTTP request failed")
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 422 {
			c.log.Warn().Int("status", resp.StatusCode).Str("order_id", orderID).Msg("cancel order rejected — order likely already terminal")
		} else {
			c.log.Error().Int("status", resp.StatusCode).Str("order_id", orderID).Msg("cancel order rejected by Alpaca")
		}
		return fmt.Errorf("alpaca: cancel order failed (status %d): %s", resp.StatusCode, string(body))
	}
	c.log.Info().Str("order_id", orderID).Msg("order canceled")
	return nil
}

// GetOrderStatus fetches the current status of an order.
func (c *RESTClient) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	resp, err := c.doReqWithOpts(ctx, http.MethodGet, pathOrders+"/"+orderID, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})

	if err != nil {
		c.log.Error().Err(err).Str("order_id", orderID).Msg("get order status HTTP request failed")
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Error().Int("status", resp.StatusCode).Str("order_id", orderID).Msg("get order status failed")
		return "", fmt.Errorf("alpaca: get order status failed (status %d): %s", resp.StatusCode, string(body))
	}
	respBody := bytes.NewReader(body)

	var res struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(respBody).Decode(&res); err != nil {
		return "", err
	}
	c.log.Debug().Str("order_id", orderID).Str("status", res.Status).Msg("order status retrieved")
	return res.Status, nil
}

func (c *RESTClient) GetOrderDetails(ctx context.Context, orderID string) (ports.OrderDetails, error) {
	resp, err := c.doReqWithOpts(ctx, http.MethodGet, pathOrders+"/"+orderID, nil, reqOpts{priority: PriorityBackground, maxRetries: 2})
	if err != nil {
		c.log.Error().Err(err).Str("order_id", orderID).Msg("get order details HTTP request failed")
		return ports.OrderDetails{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Error().Int("status", resp.StatusCode).Str("order_id", orderID).Msg("get order details failed")
		return ports.OrderDetails{}, fmt.Errorf("alpaca: get order details failed (status %d): %s", resp.StatusCode, string(body))
	}

	var raw struct {
		ID             string  `json:"id"`
		Symbol         string  `json:"symbol"`
		Side           string  `json:"side"`
		Qty            string  `json:"qty"`
		FilledQty      string  `json:"filled_qty"`
		FilledAvgPrice string  `json:"filled_avg_price"`
		Status         string  `json:"status"`
		FilledAt       *string `json:"filled_at"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ports.OrderDetails{}, fmt.Errorf("alpaca: failed to decode order details: %w", err)
	}

	qty, _ := strconv.ParseFloat(raw.Qty, 64)
	filledQty, _ := strconv.ParseFloat(raw.FilledQty, 64)
	filledAvgPrice, _ := strconv.ParseFloat(raw.FilledAvgPrice, 64)

	var filledAt time.Time
	if raw.FilledAt != nil && *raw.FilledAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, *raw.FilledAt); err == nil {
			filledAt = t
		}
	}

	c.log.Debug().Str("order_id", orderID).Str("status", raw.Status).
		Float64("filled_qty", filledQty).Float64("filled_avg_price", filledAvgPrice).
		Msg("order details retrieved")

	return ports.OrderDetails{
		BrokerOrderID:  raw.ID,
		Status:         raw.Status,
		FilledQty:      filledQty,
		FilledAvgPrice: filledAvgPrice,
		FilledAt:       filledAt,
		Symbol:         raw.Symbol,
		Side:           raw.Side,
		Qty:            qty,
	}, nil
}

// GetPositions retrieves all current positions.
func (c *RESTClient) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	resp, err := c.doReqWithOpts(ctx, http.MethodGet, pathPositions, nil, reqOpts{priority: PriorityTrading, maxRetries: 3})

	if err != nil {
		c.log.Error().Err(err).Msg("get positions HTTP request failed")
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Error().Int("status", resp.StatusCode).Msg("get positions failed")
		return nil, fmt.Errorf("alpaca: get positions failed (status %d): %s", resp.StatusCode, string(body))
	}
	respBody := bytes.NewReader(body)

	var rawPositions []struct {
		Symbol        string `json:"symbol"`
		Qty           string `json:"qty"`
		Side          string `json:"side"`
		AvgEntryPrice string `json:"avg_entry_price"`
		CurrentPrice  string `json:"current_price"`
		AssetClass    string `json:"asset_class"`
	}
	if err := json.NewDecoder(respBody).Decode(&rawPositions); err != nil {
		return nil, err
	}

	var trades []domain.Trade
	for _, rp := range rawPositions {
		sym, err := domain.NewSymbol(rp.Symbol)
		if err != nil {
			c.log.Warn().Str("symbol", rp.Symbol).Err(err).Msg("skipping position with invalid symbol")
			continue
		}
		qty, err := strconv.ParseFloat(rp.Qty, 64)
		if err != nil {
			continue
		}
		price, err := strconv.ParseFloat(rp.AvgEntryPrice, 64)
		if err != nil {
			continue
		}

		// Normalize crypto symbols: Alpaca returns "BTCUSD" but we use "BTC/USD".
		sym = sym.ToSlashFormat()

		// Determine asset class from Alpaca's asset_class field.
		var ac domain.AssetClass
		if sym.IsCryptoSymbol() || rp.AssetClass == "crypto" {
			ac = domain.AssetClassCrypto
		} else {
			ac = domain.AssetClassEquity
		}

		trades = append(trades, domain.Trade{
			Time:       time.Now(),
			TenantID:   tenantID,
			EnvMode:    envMode,
			TradeID:    uuid.New(),
			Symbol:     sym,
			Side:       rp.Side,
			Quantity:   qty,
			Price:      price,
			Commission: 0,
			Status:     "open",
			AssetClass: ac,
		})
	}
	c.log.Debug().Int("count", len(trades)).Msg("positions retrieved")
	return trades, nil
}

func (c *RESTClient) GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error) {
	symStr := strings.ReplaceAll(symbol.String(), "/", "")
	path := pathPositions + "/" + symStr

	resp, err := c.doReqWithOpts(ctx, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		return 0, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("alpaca: get position failed (status %d): %s", resp.StatusCode, string(body))
	}

	var res struct {
		Qty string `json:"qty"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return 0, fmt.Errorf("alpaca: failed to decode position response: %w", err)
	}
	qty, err := strconv.ParseFloat(res.Qty, 64)
	if err != nil {
		return 0, fmt.Errorf("alpaca: failed to parse position qty %q: %w", res.Qty, err)
	}
	return qty, nil
}

func (c *RESTClient) ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error) {
	symStr := strings.ReplaceAll(symbol.String(), "/", "")
	path := pathPositions + "/" + symStr

	resp, err := c.doReqWithOpts(ctx, http.MethodDelete, path, nil, reqOpts{priority: PriorityTrading, maxRetries: 2})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == 422 {
		c.log.Info().Int("status", resp.StatusCode).Str("symbol", symStr).
			Msg("close position: already gone")
		return "", nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("alpaca: close position failed (status %d): %s", resp.StatusCode, string(body))
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("alpaca: failed to decode close position response: %w", err)
	}

	c.log.Info().Str("symbol", symStr).Str("order_id", res.ID).
		Msg("close position: DELETE accepted — sweep market order created")
	return res.ID, nil
}

// GetQuote queries the latest quote for a given symbol from the Alpaca data API.
func (c *RESTClient) GetQuote(ctx context.Context, dataURL string, symbol domain.Symbol) (bid float64, ask float64, err error) {
	path := fmt.Sprintf("/v2/stocks/%s/quotes/latest?feed=%s", symbol.String(), c.equityFeed())
	urlStr := strings.TrimSuffix(dataURL, "/") + path
	resp, err := c.doReqFullWithPath(ctx, http.MethodGet, urlStr, path, nil, reqOpts{priority: PriorityTrading, maxRetries: 3})
	if err != nil {
		c.log.Error().Err(err).Str("symbol", symbol.String()).Msg("get quote HTTP request failed")
		return 0, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Error().Int("status", resp.StatusCode).Str("symbol", symbol.String()).Msg("get quote failed")
		return 0, 0, fmt.Errorf("alpaca: get quote failed (status %d): %s", resp.StatusCode, string(body))
	}
	respBody := bytes.NewReader(body)

	var res struct {
		Quote struct {
			BP float64 `json:"bp"`
			AP float64 `json:"ap"`
		} `json:"quote"`
	}
	if err := json.NewDecoder(respBody).Decode(&res); err != nil {
		return 0, 0, err
	}
	c.log.Debug().
		Str("symbol", symbol.String()).
		Float64("bid", res.Quote.BP).
		Float64("ask", res.Quote.AP).
		Msg("quote retrieved")
	return res.Quote.BP, res.Quote.AP, nil
}

// GetHistoricalBars fetches historical OHLCV bars from the Alpaca data API.
// It paginates via next_page_token until all results are returned.
func (c *RESTClient) GetHistoricalBars(ctx context.Context, dataURL string, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	tf := toAlpacaTimeframe(string(timeframe))
	var bars []domain.MarketBar
	nextToken := ""

	for {
		path := fmt.Sprintf("/v2/stocks/%s/bars?timeframe=%s&start=%s&end=%s&limit=1000&feed=%s",
			symbol.String(), tf,
			from.UTC().Format(time.RFC3339),
			to.UTC().Format(time.RFC3339),
			c.equityFeed(),
		)
		if nextToken != "" {
			path += "&page_token=" + nextToken
		}

		urlStr := strings.TrimSuffix(dataURL, "/") + path
		resp, err := c.doReqFullWithPath(ctx, http.MethodGet, urlStr, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
		if err != nil {
			c.log.Error().Err(err).Str("symbol", symbol.String()).Msg("historical bars HTTP request failed")
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			c.log.Error().Int("status", resp.StatusCode).Str("symbol", symbol.String()).Msg("historical bars request failed")
			return nil, fmt.Errorf("alpaca: get historical bars failed (status %d): %s", resp.StatusCode, string(body))
		}

		var page struct {
			Bars          []historicalBar `json:"bars"`
			NextPageToken string          `json:"next_page_token"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("alpaca: failed to decode historical bars: %w", err)
		}

		for _, b := range page.Bars {
			bar, err := domain.NewMarketBar(b.T, symbol, timeframe, b.O, b.H, b.L, b.C, b.V)
			if err != nil {
				c.log.Warn().Err(err).Str("symbol", symbol.String()).Msg("skipping invalid historical bar")
				continue
			}
			bar.TradeCount = b.N
			bars = append(bars, bar)
		}

		if page.NextPageToken == "" {
			break
		}
		nextToken = page.NextPageToken
	}

	c.log.Debug().
		Str("symbol", symbol.String()).
		Int("count", len(bars)).
		Msg("historical bars retrieved")
	return bars, nil
}

// historicalBar is the JSON shape for a single bar from Alpaca's REST bar endpoints.
type historicalBar struct {
	T  time.Time `json:"t"`
	O  float64   `json:"o"`
	H  float64   `json:"h"`
	L  float64   `json:"l"`
	C  float64   `json:"c"`
	V  float64   `json:"v"`
	N  uint64    `json:"n"`
	VW float64   `json:"vw"`
}

// toAlpacaTimeframe converts our short timeframe strings to Alpaca API format.
func toAlpacaTimeframe(tf string) string {
	switch tf {
	case "1m":
		return "1Min"
	case "5m":
		return "5Min"
	case "15m":
		return "15Min"
	case "1h":
		return "1Hour"
	case "1d":
		return "1Day"
	default:
		return "1Min"
	}
}

// GetAccountEquity fetches the current account equity from the Alpaca broker API.
func (c *RESTClient) GetAccountEquity(ctx context.Context) (float64, error) {
	resp, err := c.doReqWithOpts(ctx, http.MethodGet, "/v2/account", nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
	if err != nil {
		c.log.Error().Err(err).Msg("get account HTTP request failed")
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Error().Int("status", resp.StatusCode).Msg("get account failed")
		return 0, fmt.Errorf("alpaca: get account failed (status %d): %s", resp.StatusCode, string(body))
	}
	var res struct {
		Equity string `json:"equity"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return 0, fmt.Errorf("alpaca: failed to decode account equity: %w", err)
	}
	equity, err := strconv.ParseFloat(res.Equity, 64)
	if err != nil {
		return 0, fmt.Errorf("alpaca: failed to parse equity %q: %w", res.Equity, err)
	}
	c.log.Info().Float64("equity", equity).Msg("account equity retrieved")
	return equity, nil
}

// AccountBuyingPower holds the buying power fields from the Alpaca /v2/account response.
type AccountBuyingPower struct {
	DayTradingBuyingPower    float64
	EffectiveBuyingPower     float64
	NonMarginableBuyingPower float64
	PatternDayTrader         bool
}

// GetAccountBuyingPower fetches DTBP, effective buying power, and PDT flag from /v2/account.
func (c *RESTClient) GetAccountBuyingPower(ctx context.Context) (AccountBuyingPower, error) {
	resp, err := c.doReqWithOpts(ctx, http.MethodGet, "/v2/account", nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
	if err != nil {
		c.log.Error().Err(err).Msg("get account buying power HTTP request failed")
		return AccountBuyingPower{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Error().Int("status", resp.StatusCode).Msg("get account buying power failed")
		return AccountBuyingPower{}, fmt.Errorf("alpaca: get account buying power failed (status %d): %s", resp.StatusCode, string(body))
	}
	var res struct {
		DayTradingBuyingPower    string `json:"daytrading_buying_power"`
		EffectiveBuyingPower     string `json:"effective_buying_power"`
		NonMarginableBuyingPower string `json:"non_marginable_buying_power"` //nolint:misspell // Alpaca API field name
		PatternDayTrader         bool   `json:"pattern_day_trader"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return AccountBuyingPower{}, fmt.Errorf("alpaca: failed to decode account buying power: %w", err)
	}
	dtbp, _ := strconv.ParseFloat(res.DayTradingBuyingPower, 64)
	ebp, _ := strconv.ParseFloat(res.EffectiveBuyingPower, 64)
	nmbp, _ := strconv.ParseFloat(res.NonMarginableBuyingPower, 64)
	c.log.Debug().
		Float64("dtbp", dtbp).
		Float64("effective_bp", ebp).
		Float64("non_marginal_bp", nmbp).
		Bool("pdt", res.PatternDayTrader).
		Msg("account buying power retrieved")
	return AccountBuyingPower{
		DayTradingBuyingPower:    dtbp,
		EffectiveBuyingPower:     ebp,
		NonMarginableBuyingPower: nmbp,
		PatternDayTrader:         res.PatternDayTrader,
	}, nil
}

// CancelOpenOrders cancels all open orders for a given symbol and side on Alpaca.
// It queries open orders via GET /v2/orders?status=open&symbols={symbol}&side={side},
// then cancels each one via DELETE /v2/orders/{id}.
// Returns the number of successfully canceled orders.
func (c *RESTClient) CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error) {
	// Alpaca orders API uses no-slash format for crypto (e.g. "ETHUSD" not "ETH/USD").
	symStr := strings.ReplaceAll(symbol.String(), "/", "")
	path := fmt.Sprintf("%s?status=open&symbols=%s&side=%s", pathOrders, symStr, side)

	resp, err := c.doReqWithOpts(ctx, http.MethodGet, path, nil, reqOpts{priority: PriorityTrading, maxRetries: 3})
	if err != nil {
		c.log.Error().Err(err).Str("symbol", symStr).Str("side", side).Msg("list open orders HTTP request failed")
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Error().Int("status", resp.StatusCode).Str("symbol", symStr).Msg("list open orders failed")
		return 0, fmt.Errorf("alpaca: list open orders failed (status %d): %s", resp.StatusCode, string(body))
	}

	var orders []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &orders); err != nil {
		return 0, fmt.Errorf("alpaca: failed to decode open orders: %w", err)
	}

	if len(orders) == 0 {
		return 0, nil
	}

	canceled := 0
	for _, o := range orders {
		cancelResp, cancelErr := c.doReqWithOpts(ctx, http.MethodDelete, pathOrders+"/"+o.ID, nil, reqOpts{priority: PriorityTrading, maxRetries: 2})
		if cancelErr != nil {
			c.log.Warn().Err(cancelErr).Str("order_id", o.ID).Msg("cancel open order HTTP request failed")
			continue
		}
		cancelBody, _ := io.ReadAll(cancelResp.Body)
		cancelResp.Body.Close()

		if cancelResp.StatusCode >= 200 && cancelResp.StatusCode < 300 {
			canceled++
			c.log.Info().Str("order_id", o.ID).Str("symbol", symStr).Msg("canceled stale open order")
		} else {
			// 422 or 500 typically means order already filled/canceled — not a real error.
			c.log.Warn().
				Int("status", cancelResp.StatusCode).
				Str("order_id", o.ID).
				Str("response", string(cancelBody)).
				Msg("cancel open order returned non-2xx — order may already be terminal")
		}
	}

	return canceled, nil
}

// ClosedOrder represents a closed/filled order from the Alpaca REST API.
type ClosedOrder struct {
	ID             string  `json:"id"`
	Symbol         string  `json:"symbol"`
	Qty            string  `json:"qty"`
	FilledQty      string  `json:"filled_qty"`
	FilledAvgPrice string  `json:"filled_avg_price"`
	Side           string  `json:"side"`
	Type           string  `json:"type"`
	Status         string  `json:"status"`
	FilledAt       *string `json:"filled_at"`
	SubmittedAt    string  `json:"submitted_at"`
	CreatedAt      string  `json:"created_at"`
}

// GetClosedOrders fetches all closed orders from Alpaca, paginating via the
// created_at timestamp. It returns orders in ascending chronological order.
func (c *RESTClient) GetClosedOrders(ctx context.Context, after, until time.Time) ([]ClosedOrder, error) {
	var allOrders []ClosedOrder
	// Alpaca paginates closed orders using 'after' and 'until' timestamps.
	// We request in ascending order (oldest first) and walk forward.
	currentAfter := after

	for {
		path := fmt.Sprintf("/v2/orders?status=closed&limit=500&direction=asc&after=%s&until=%s",
			currentAfter.UTC().Format(time.RFC3339),
			until.UTC().Format(time.RFC3339),
		)

		resp, err := c.doReq(ctx, http.MethodGet, path, nil)
		if err != nil {
			c.log.Error().Err(err).Msg("get closed orders HTTP request failed")
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			c.log.Error().Int("status", resp.StatusCode).Msg("get closed orders failed")
			return nil, fmt.Errorf("alpaca: get closed orders failed (status %d): %s", resp.StatusCode, string(body))
		}

		var page []ClosedOrder
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("alpaca: failed to decode closed orders: %w", err)
		}

		if len(page) == 0 {
			break
		}

		allOrders = append(allOrders, page...)

		// If we got fewer than 500, we've reached the end.
		if len(page) < 500 {
			break
		}

		// Move the cursor forward to the last order's created_at time.
		lastCreated := page[len(page)-1].CreatedAt
		parsed, err := time.Parse(time.RFC3339Nano, lastCreated)
		if err != nil {
			c.log.Warn().Str("created_at", lastCreated).Err(err).Msg("failed to parse last order created_at for pagination")
			break
		}
		currentAfter = parsed
	}

	c.log.Info().Int("count", len(allOrders)).Msg("closed orders retrieved")
	return allOrders, nil
}

func roundPrice(v float64) string {
	if v == 0 {
		return "0"
	}
	abs := v
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 1.0:
		return strconv.FormatFloat(v, 'f', 2, 64)
	case abs >= 0.01:
		return strconv.FormatFloat(v, 'f', 4, 64)
	case abs >= 0.0001:
		return strconv.FormatFloat(v, 'f', 6, 64)
	default:
		return strconv.FormatFloat(v, 'f', 8, 64)
	}
}
