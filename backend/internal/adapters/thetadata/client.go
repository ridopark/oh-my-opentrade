// Package thetadata is a thin REST client for Theta Data's options
// quote and Greeks snapshot endpoints. It implements
// ports.OptionMarketDataPort so it can be wired in front of Alpaca
// snapshots and DoltHub historical lookups via the composite
// market-data fallback chain.
//
// The package is intentionally plumbing-only at first ship: there is
// no WebSocket support, no on-disk cache, and no historical backfill.
// The default base URL points at https://rest.thetadata.net which is
// the documented entry point for the v2 REST snapshot API; the exact
// path/payload shapes are tagged TODO-verify so that the first
// integration test against a real key catches any spec drift.
package thetadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// HTTPDoer is the minimal interface the client needs from net/http. It is
// taken as an interface so tests can substitute httptest.Server-backed
// clients (or fully synthetic doers) without spinning a real listener.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config tunes the Theta Data REST client.
type Config struct {
	APIKey          string
	BaseURL         string        // defaults to https://rest.thetadata.net
	RateLimitPerSec int           // tokens issued per second; defaults to 10
	HTTPTimeout     time.Duration // defaults to 10s
}

// Client is a Theta Data REST adapter.
//
// Invariants:
//   - A nil Client is a valid zero value: every method returns
//     ports.ErrOptionDataNotConfigured. This lets bootstrap code wire the
//     composite chain unconditionally without nil-checking.
//   - All HTTP calls are gated by a ticker-based limiter so a burst of
//     calls cannot exceed the entry-tier 10 req/s ceiling.
type Client struct {
	apiKey  string
	baseURL string
	http    HTTPDoer
	log     zerolog.Logger

	tokens    chan struct{}
	closeOnce sync.Once
	closeCh   chan struct{}
}

// Compile-time interface assertion.
var _ ports.OptionMarketDataPort = (*Client)(nil)

// NewClient constructs a Client. When cfg.APIKey is empty, the function
// returns (nil, nil) and logs a debug message — bootstrap treats nil as
// "use fallback" exactly the way fredfinnhub.NewClient does. Returning a
// nil Client (rather than an error) keeps the call site simple: pass it
// straight into the composite market-data chain and it will be skipped.
func NewClient(cfg Config, log zerolog.Logger) (*Client, error) {
	l := log.With().Str("component", "thetadata").Logger()
	if cfg.APIKey == "" {
		l.Debug().Msg("no API key configured; thetadata adapter disabled")
		return nil, nil
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://rest.thetadata.net"
	}
	rate := cfg.RateLimitPerSec
	if rate <= 0 {
		rate = 10
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	c := &Client{
		apiKey:  cfg.APIKey,
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
		log:     l,
		tokens:  make(chan struct{}, rate),
		closeCh: make(chan struct{}),
	}

	// Pre-fill the token bucket and start a refill ticker. A buffered
	// channel of capacity = rate caps burst at one second's budget; the
	// ticker tops it off rate times per second.
	for range rate {
		c.tokens <- struct{}{}
	}
	go c.refill(time.Second / time.Duration(rate))
	return c, nil
}

// SetHTTPClient swaps the underlying HTTP doer. Tests use this to inject
// an httptest.Server-backed client.
func (c *Client) SetHTTPClient(d HTTPDoer) {
	if c == nil || d == nil {
		return
	}
	c.http = d
}

// Close stops the rate-limiter refill loop. Safe to call more than once.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() { close(c.closeCh) })
}

func (c *Client) refill(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.closeCh:
			return
		case <-t.C:
			select {
			case c.tokens <- struct{}{}:
			default:
				// bucket full
			}
		}
	}
}

func (c *Client) acquire(ctx context.Context) error {
	select {
	case <-c.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Quote returns the current bid/ask/last snapshot for the given contract.
// TODO-verify: confirm exact path and JSON shape against the live
// /v2/snapshot/option/quote endpoint once a subscription is available.
func (c *Client) Quote(ctx context.Context, underlying string, expiry time.Time, strike float64, right string) (ports.OptionQuote, error) {
	if c == nil {
		return ports.OptionQuote{}, ports.ErrOptionDataNotConfigured
	}
	body, err := c.snapshot(ctx, "/v2/snapshot/option/quote", underlying, expiry, strike, right)
	if err != nil {
		return ports.OptionQuote{}, err
	}
	return parseQuote(body, underlying, expiry, strike, right)
}

// Greeks returns the current IV and first-order Greeks for the given
// contract. TODO-verify: same caveat as Quote — the v2 snapshot/greeks
// path and JSON shape need to be confirmed against the live endpoint.
func (c *Client) Greeks(ctx context.Context, underlying string, expiry time.Time, strike float64, right string) (ports.OptionGreeks, error) {
	if c == nil {
		return ports.OptionGreeks{}, ports.ErrOptionDataNotConfigured
	}
	body, err := c.snapshot(ctx, "/v2/snapshot/option/greeks", underlying, expiry, strike, right)
	if err != nil {
		return ports.OptionGreeks{}, err
	}
	return parseGreeks(body)
}

func (c *Client) snapshot(ctx context.Context, path, underlying string, expiry time.Time, strike float64, right string) ([]byte, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("root", underlying)
	q.Set("exp", expiry.UTC().Format("20060102"))
	// Theta Data encodes strikes as integer thousandths of a dollar
	// (e.g. $150 -> 150000). TODO-verify against the live spec.
	q.Set("strike", strconv.FormatInt(int64(strike*1000), 10))
	q.Set("right", normalizeRight(right))

	endpoint := fmt.Sprintf("%s%s?%s", c.baseURL, path, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("thetadata: new request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// TODO-verify: Theta Data documents an Authorization: Bearer scheme
	// for the cloud REST tier. Confirm header name once a key is live.
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("thetadata: http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("thetadata: read: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("thetadata: status %d: %w", resp.StatusCode, ports.ErrOptionDataNotConfigured)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("thetadata: status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// quoteResponse mirrors the documented v2 snapshot/quote shape:
// {"header": {...}, "response": [[ms, bidSize, bid, askSize, ask, last, ...]]}
// TODO-verify field order against the live spec.
type quoteResponse struct {
	Response [][]json.Number `json:"response"`
}

type greeksResponse struct {
	Response [][]json.Number `json:"response"`
}

func parseQuote(body []byte, underlying string, expiry time.Time, strike float64, right string) (ports.OptionQuote, error) {
	var resp quoteResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ports.OptionQuote{}, fmt.Errorf("thetadata: decode quote: %w", err)
	}
	if len(resp.Response) == 0 || len(resp.Response[0]) < 6 {
		return ports.OptionQuote{}, errors.New("thetadata: empty quote response")
	}
	row := resp.Response[0]
	ms := numberInt(row[0])
	bidSize := numberInt(row[1])
	bid := numberFloat(row[2])
	askSize := numberInt(row[3])
	ask := numberFloat(row[4])
	last := numberFloat(row[5])
	return ports.OptionQuote{
		Symbol:    underlying,
		Expiry:    expiry,
		Strike:    strike,
		Right:     normalizeRight(right),
		Bid:       bid,
		Ask:       ask,
		Last:      last,
		BidSize:   bidSize,
		AskSize:   askSize,
		Timestamp: time.UnixMilli(int64(ms)).UTC(),
	}, nil
}

func parseGreeks(body []byte) (ports.OptionGreeks, error) {
	var resp greeksResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ports.OptionGreeks{}, fmt.Errorf("thetadata: decode greeks: %w", err)
	}
	if len(resp.Response) == 0 || len(resp.Response[0]) < 8 {
		return ports.OptionGreeks{}, errors.New("thetadata: empty greeks response")
	}
	// Documented column order: ms, iv, delta, gamma, theta, vega, rho, underlying.
	// TODO-verify against the live response.
	row := resp.Response[0]
	ms := numberInt(row[0])
	return ports.OptionGreeks{
		IV:              numberFloat(row[1]),
		Delta:           numberFloat(row[2]),
		Gamma:           numberFloat(row[3]),
		Theta:           numberFloat(row[4]),
		Vega:            numberFloat(row[5]),
		Rho:             numberFloat(row[6]),
		UnderlyingPrice: numberFloat(row[7]),
		Timestamp:       time.UnixMilli(int64(ms)).UTC(),
	}, nil
}

func numberFloat(n json.Number) float64 {
	f, err := n.Float64()
	if err != nil {
		return 0
	}
	return f
}

func numberInt(n json.Number) int {
	i, err := n.Int64()
	if err != nil {
		// fall back through float for "1.6e9"-style timestamps
		f, ferr := n.Float64()
		if ferr != nil {
			return 0
		}
		return int(f)
	}
	return int(i)
}

func normalizeRight(r string) string {
	if len(r) == 0 {
		return ""
	}
	switch r[0] {
	case 'C', 'c':
		return "C"
	case 'P', 'p':
		return "P"
	}
	return r
}
