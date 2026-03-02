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
}

// NewRESTClient constructs a new RESTClient with proper rate limiting.
func NewRESTClient(baseURL string, apiKey string, apiSecret string, limiter *RateLimiter) *RESTClient {
	return &RESTClient{
		baseURL:   baseURL,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		limiter:   limiter,
		client:    &http.Client{Timeout: httpTimeout},
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
		"stop_price":    intent.StopLoss,
	}

	b, _ := json.Marshal(reqBody)
	resp, err := c.doReq(ctx, http.MethodPost, pathOrders, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
	return res.ID, nil
}

// CancelOrder requests cancellation of a specific order by ID.
func (c *RESTClient) CancelOrder(ctx context.Context, orderID string) error {
	resp, err := c.doReq(ctx, http.MethodDelete, pathOrders+"/"+orderID, nil)

	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("alpaca: cancel order failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// GetOrderStatus fetches the current status of an order.
func (c *RESTClient) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	resp, err := c.doReq(ctx, http.MethodGet, pathOrders+"/"+orderID, nil)

	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("alpaca: get order status failed (status %d): %s", resp.StatusCode, string(body))
	}
	respBody := bytes.NewReader(body)

	var res struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(respBody).Decode(&res); err != nil {
		return "", err
	}
	return res.Status, nil
}

// GetPositions retrieves all current positions.
func (c *RESTClient) GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	resp, err := c.doReq(ctx, http.MethodGet, pathPositions, nil)

	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
	return trades, nil
}

// GetQuote queries the latest quote for a given symbol.
func (c *RESTClient) GetQuote(ctx context.Context, symbol domain.Symbol) (bid float64, ask float64, err error) {
	resp, err := c.doReq(ctx, http.MethodGet, pathStocks+symbol.String()+"/quotes/latest", nil)

	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
	return res.Quote.BP, res.Quote.AP, nil
}
