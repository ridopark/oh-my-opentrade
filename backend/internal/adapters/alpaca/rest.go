package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
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
	limiter   *RateLimiter
	client    *http.Client
	log       zerolog.Logger
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
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	urlStr := strings.TrimSuffix(c.baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(headerAPIKey, c.apiKey)
	req.Header.Set(headerAPISecret, c.apiSecret)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.client.Do(req)
}

// SubmitOrder submits a new order to the Alpaca REST API.
func (c *RESTClient) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	side := "sell"
	if intent.Direction == domain.DirectionLong {
		side = "buy"
	}

	reqBody := map[string]interface{}{
		"symbol":        intent.Symbol.String(),
		"qty":           fmt.Sprintf("%g", intent.Quantity),
		"side":          side,
		"type":          "limit",
		"time_in_force": "gtc",
		"limit_price":   intent.LimitPrice,
	}
	if intent.StopLoss > 0 {
		reqBody["type"] = "stop_limit"
		reqBody["stop_price"] = intent.StopLoss
	}

	b, _ := json.Marshal(reqBody)
	resp, err := c.doReq(ctx, http.MethodPost, pathOrders, bytes.NewReader(b))
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
	resp, err := c.doReq(ctx, http.MethodDelete, pathOrders+"/"+orderID, nil)

	if err != nil {
		c.log.Error().Err(err).Str("order_id", orderID).Msg("cancel order HTTP request failed")
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		c.log.Error().Int("status", resp.StatusCode).Str("order_id", orderID).Msg("cancel order rejected by Alpaca")
		return fmt.Errorf("alpaca: cancel order failed (status %d): %s", resp.StatusCode, string(body))
	}
	c.log.Info().Str("order_id", orderID).Msg("order cancelled")
	return nil
}

// GetOrderStatus fetches the current status of an order.
func (c *RESTClient) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	resp, err := c.doReq(ctx, http.MethodGet, pathOrders+"/"+orderID, nil)

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

// GetPositions retrieves all current positions.
func (c *RESTClient) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	resp, err := c.doReq(ctx, http.MethodGet, pathPositions, nil)

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
		})
	}
	c.log.Debug().Int("count", len(trades)).Msg("positions retrieved")
	return trades, nil
}

// GetQuote queries the latest quote for a given symbol.
func (c *RESTClient) GetQuote(ctx context.Context, symbol domain.Symbol) (bid float64, ask float64, err error) {
	resp, err := c.doReq(ctx, http.MethodGet, pathStocks+symbol.String()+"/quotes/latest", nil)

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
		path := fmt.Sprintf("/v2/stocks/%s/bars?timeframe=%s&start=%s&end=%s&limit=1000&feed=iex",
			symbol.String(), tf,
			from.UTC().Format(time.RFC3339),
			to.UTC().Format(time.RFC3339),
		)
		if nextToken != "" {
			path += "&page_token=" + nextToken
		}

		// Historical bars come from data.alpaca.markets, not paper-api.
		urlStr := strings.TrimSuffix(dataURL, "/") + path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set(headerAPIKey, c.apiKey)
		req.Header.Set(headerAPISecret, c.apiSecret)

		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		resp, err := c.client.Do(req)
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
	T time.Time `json:"t"`
	O float64   `json:"o"`
	H float64   `json:"h"`
	L float64   `json:"l"`
	C float64   `json:"c"`
	V float64   `json:"v"`
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
	resp, err := c.doReq(ctx, http.MethodGet, "/v2/account", nil)
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
