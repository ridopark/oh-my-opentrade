// Package bybit is a read-only REST client for Bybit's public v5 API.
// It provides funding rate data for perpetual contracts used by the
// funding arbitrage research pipeline. No authentication is required;
// only public market data endpoints are used. Rate limiting is enforced
// at 120 req/min per Bybit's documented public tier.
package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/oh-my-opentrade/backend/internal/httputil"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL    = "https://api.bybit.com"
	defaultTimeout    = 10 * time.Second
	rateLimit         = 120 // requests per minute
	maxRetries        = 3
	baseRetryBackoff  = 500 * time.Millisecond
)

// Sentinel errors for the Bybit adapter.
var (
	ErrRateLimit          = errors.New("bybit: rate limit exceeded")
	ErrInvalidResponse    = errors.New("bybit: invalid response")
	ErrSymbolNotFound     = errors.New("bybit: symbol not found")
	ErrStreamNotSupported = errors.New("bybit: streaming not supported in read-only adapter")
)

// symbolMap translates internal symbol notation to Bybit linear contract tickers.
var symbolMap = map[string]string{
	"BTC/USD": "BTCUSDT",
	"ETH/USD": "ETHUSDT",
	"SOL/USD": "SOLUSDT",
}

// Client is the Bybit REST adapter for public market data endpoints.
type Client struct {
	baseURL string
	http    httputil.HTTPDoer
	limiter *rate.Limiter
	log     zerolog.Logger
}

// NewClient creates a Bybit client with sensible defaults.
func NewClient(baseURL string, log zerolog.Logger) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: defaultTimeout},
		limiter: rate.NewLimiter(rate.Every(time.Minute/rateLimit), 1),
		log:     log.With().Str("component", "bybit_client").Logger(),
	}
}

// NewClientWithHTTP creates a client using the provided HTTPDoer (useful for testing).
func NewClientWithHTTP(baseURL string, doer httputil.HTTPDoer, log zerolog.Logger) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: baseURL,
		http:    doer,
		limiter: rate.NewLimiter(rate.Inf, 1), // no throttle in tests
		log:     log.With().Str("component", "bybit_client").Logger(),
	}
}

// v5Response is the top-level envelope for Bybit v5 API responses.
type v5Response struct {
	RetCode int             `json:"retCode"`
	RetMsg  string          `json:"retMsg"`
	Result  json.RawMessage `json:"result"`
}

// get performs a GET request and decodes the response into the v5 envelope.
func (c *Client) get(ctx context.Context, path string) (*v5Response, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("bybit: rate limiter: %w", err)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("bybit: create request: %w", err)
	}

	var resp *http.Response
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err = c.http.Do(req)
		if err == nil && resp.StatusCode != http.StatusTooManyRequests {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		if attempt < maxRetries {
			backoff := baseRetryBackoff * time.Duration(1<<attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("bybit: http get %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimit
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bybit: unexpected status %d for %s", resp.StatusCode, path)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("bybit: read body: %w", err)
	}

	var v5 v5Response
	if err := json.Unmarshal(body, &v5); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	if v5.RetCode != 0 {
		return nil, fmt.Errorf("bybit: api error code=%d msg=%s", v5.RetCode, v5.RetMsg)
	}
	return &v5, nil
}

// toBybitSymbol maps an internal symbol (e.g. "BTC/USD") to a Bybit
// linear contract ticker (e.g. "BTCUSDT").
func toBybitSymbol(s string) (string, error) {
	if bs, ok := symbolMap[s]; ok {
		return bs, nil
	}
	return "", fmt.Errorf("%w: no mapping for %q", ErrSymbolNotFound, s)
}
