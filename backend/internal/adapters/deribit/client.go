// Package deribit is a read-only REST client for the Deribit public API.
// It fetches live options instrument metadata and ticker data (IV, Greeks,
// open interest) for crypto assets, then computes IV surface metrics used
// by the skew-regime classifier to gate carry-trade exposure.
//
// No authentication is required: only unauthenticated public/get_instruments
// and public/ticker endpoints are used. Rate limiting is enforced at 20
// req/s (Deribit public tier) via a token-bucket limiter.
package deribit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/httputil"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	defaultBaseURL     = "https://www.deribit.com/api/v2/"
	defaultRateLimit   = 20
	defaultHTTPTimeout = 10 * time.Second
	maxConcurrent      = 10
	maxRetries         = 3
	retryBaseDelay     = 500 * time.Millisecond
)

// Config tunes the Deribit REST client.
type Config struct {
	BaseURL      string
	PollInterval time.Duration
	Assets       []string
	HTTPTimeout  time.Duration
}

// Instrument represents a Deribit options contract.
type Instrument struct {
	InstrumentName string
	BaseCurrency   string
	QuoteCurrency  string
	Strike         float64
	Expiration     time.Time
	OptionType     string // "call" or "put"
	IsActive       bool
}

// Ticker represents a live snapshot of an option's IV and Greeks.
type Ticker struct {
	InstrumentName  string
	MarkIV          float64 // implied vol as fraction (0-1)
	BidIV           float64
	AskIV           float64
	Delta           float64
	Gamma           float64
	Vega            float64
	Theta           float64
	UnderlyingPrice float64
	MarkPrice       float64
	OpenInterest    float64
}

// Client is a Deribit REST adapter. It implements ports.OptionsIVPort by
// fetching live options data, building the IV surface, and exposing
// derived metrics.
type Client struct {
	baseURL string
	http    httputil.HTTPDoer
	log     zerolog.Logger
	assets  []string

	tokens    chan struct{}
	closeOnce sync.Once
	closeCh   chan struct{}
}

// Compile-time interface assertion.
var _ ports.OptionsIVPort = (*Client)(nil)

// NewClient constructs a Deribit REST client. The client is read-only and
// requires no authentication.
func NewClient(cfg Config, log zerolog.Logger) (*Client, error) {
	l := log.With().Str("component", "deribit").Logger()

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}

	assets := cfg.Assets
	if len(assets) == 0 {
		assets = []string{"BTC", "ETH"}
	}

	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}

	c := &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
		log:     l,
		assets:  assets,
		tokens:  make(chan struct{}, defaultRateLimit),
		closeCh: make(chan struct{}),
	}

	// Pre-fill the token bucket.
	for range defaultRateLimit {
		c.tokens <- struct{}{}
	}
	go c.refill(time.Second / time.Duration(defaultRateLimit))

	l.Info().Strs("assets", assets).Str("base_url", baseURL).Msg("deribit adapter initialized")
	return c, nil
}

// SetHTTPClient swaps the underlying HTTP doer for testing.
func (c *Client) SetHTTPClient(d httputil.HTTPDoer) {
	if c == nil || d == nil {
		return
	}
	c.http = d
}

// Close stops the rate-limiter refill loop.
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

// --- Deribit JSON response types ---

type jsonRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rawInstrument struct {
	InstrumentName  string  `json:"instrument_name"`
	BaseCurrency    string  `json:"base_currency"`
	QuoteCurrency   string  `json:"quote_currency"`
	Strike          float64 `json:"strike"`
	ExpirationTS    int64   `json:"expiration_timestamp"`
	OptionType      string  `json:"option_type"`
	IsActive        bool    `json:"is_active"`
	Kind            string  `json:"kind"`
}

type rawTicker struct {
	InstrumentName  string     `json:"instrument_name"`
	MarkIV          float64    `json:"mark_iv"`
	BidIV           float64    `json:"bid_iv"`
	AskIV           float64    `json:"ask_iv"`
	UnderlyingPrice float64    `json:"underlying_price"`
	MarkPrice       float64    `json:"mark_price"`
	OpenInterest    float64    `json:"open_interest"`
	Greeks          rawGreeks  `json:"greeks"`
}

type rawGreeks struct {
	Delta float64 `json:"delta"`
	Gamma float64 `json:"gamma"`
	Vega  float64 `json:"vega"`
	Theta float64 `json:"theta"`
}

// GetInstruments fetches all active options for a currency (e.g. "BTC").
func (c *Client) GetInstruments(ctx context.Context, currency string) ([]Instrument, error) {
	params := url.Values{
		"currency": {currency},
		"kind":     {"option"},
		"expired":  {"false"},
	}

	body, err := c.doGet(ctx, "public/get_instruments", params)
	if err != nil {
		return nil, fmt.Errorf("deribit: get_instruments: %w", err)
	}

	var rpc jsonRPCResponse
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, fmt.Errorf("deribit: get_instruments: decode: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("deribit: get_instruments: rpc error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}

	var raw []rawInstrument
	if err := json.Unmarshal(rpc.Result, &raw); err != nil {
		return nil, fmt.Errorf("deribit: get_instruments: decode result: %w", err)
	}

	instruments := make([]Instrument, 0, len(raw))
	for _, r := range raw {
		instruments = append(instruments, Instrument{
			InstrumentName: r.InstrumentName,
			BaseCurrency:   r.BaseCurrency,
			QuoteCurrency:  r.QuoteCurrency,
			Strike:         r.Strike,
			Expiration:     time.UnixMilli(r.ExpirationTS).UTC(),
			OptionType:     r.OptionType,
			IsActive:       r.IsActive,
		})
	}
	return instruments, nil
}

// GetTicker fetches the ticker for a single instrument.
func (c *Client) GetTicker(ctx context.Context, instrumentName string) (Ticker, error) {
	params := url.Values{
		"instrument_name": {instrumentName},
	}

	body, err := c.doGet(ctx, "public/ticker", params)
	if err != nil {
		return Ticker{}, fmt.Errorf("deribit: ticker: %w", err)
	}

	var rpc jsonRPCResponse
	if err := json.Unmarshal(body, &rpc); err != nil {
		return Ticker{}, fmt.Errorf("deribit: ticker: decode: %w", err)
	}
	if rpc.Error != nil {
		return Ticker{}, fmt.Errorf("deribit: ticker: rpc error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}

	var raw rawTicker
	if err := json.Unmarshal(rpc.Result, &raw); err != nil {
		return Ticker{}, fmt.Errorf("deribit: ticker: decode result: %w", err)
	}

	return Ticker{
		InstrumentName:  raw.InstrumentName,
		MarkIV:          raw.MarkIV / 100.0, // Deribit returns IV as percentage, convert to fraction
		BidIV:           raw.BidIV / 100.0,
		AskIV:           raw.AskIV / 100.0,
		Delta:           raw.Greeks.Delta,
		Gamma:           raw.Greeks.Gamma,
		Vega:            raw.Greeks.Vega,
		Theta:           raw.Greeks.Theta,
		UnderlyingPrice: raw.UnderlyingPrice,
		MarkPrice:       raw.MarkPrice,
		OpenInterest:    raw.OpenInterest,
	}, nil
}

// GetTickerBatch fetches tickers for all active options of a currency using
// a bounded goroutine pool (max 10 concurrent requests).
func (c *Client) GetTickerBatch(ctx context.Context, currency string) ([]Ticker, error) {
	instruments, err := c.GetInstruments(ctx, currency)
	if err != nil {
		return nil, fmt.Errorf("deribit: ticker_batch: %w", err)
	}
	return c.getTickersForInstruments(ctx, currency, instruments)
}

// getTickersForInstruments fetches tickers for the provided instruments.
// Shared by GetTickerBatch (which fetches instruments itself) and Surface
// (which reuses a previously fetched instrument list to avoid double-fetch).
func (c *Client) getTickersForInstruments(ctx context.Context, currency string, instruments []Instrument) ([]Ticker, error) {
	type result struct {
		ticker Ticker
		err    error
	}

	results := make([]result, len(instruments))
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for i, inst := range instruments {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			tk, ferr := c.GetTicker(ctx, name)
			results[idx] = result{ticker: tk, err: ferr}
		}(i, inst.InstrumentName)
	}
	wg.Wait()

	var tickers []Ticker
	var errs int
	for _, r := range results {
		if r.err != nil {
			errs++
			if errs <= 3 {
				c.log.Warn().Err(r.err).Msg("ticker fetch failed")
			}
			continue
		}
		tickers = append(tickers, r.ticker)
	}

	if len(tickers) == 0 && errs > 0 {
		return nil, fmt.Errorf("deribit: ticker_batch: all %d fetches failed", errs)
	}

	c.log.Debug().
		Str("currency", currency).
		Int("total", len(instruments)).
		Int("fetched", len(tickers)).
		Int("errors", errs).
		Msg("ticker batch complete")

	return tickers, nil
}

// Surface implements ports.OptionsIVPort. It fetches all tickers for the
// asset and builds an IV surface. Instruments are fetched once inside
// GetTickerBatch and reused here to avoid a duplicate API call.
func (c *Client) Surface(ctx context.Context, asset string) (ports.IVSurface, error) {
	// GetInstruments is called once here; we pass the result to
	// getTickerBatchWithInstruments to avoid the double-fetch.
	instruments, err := c.GetInstruments(ctx, asset)
	if err != nil {
		return ports.IVSurface{}, fmt.Errorf("deribit: surface: instruments: %w", err)
	}

	tickers, err := c.getTickersForInstruments(ctx, asset, instruments)
	if err != nil {
		return ports.IVSurface{}, fmt.Errorf("deribit: surface: %w", err)
	}

	surface := BuildIVSurface(asset, time.Now().UTC(), instruments, tickers)
	return surface, nil
}

// SkewRR implements ports.OptionsIVPort.
func (c *Client) SkewRR(ctx context.Context, asset string, tenor string) (float64, error) {
	surface, err := c.Surface(ctx, asset)
	if err != nil {
		return 0, err
	}
	switch tenor {
	case "7d":
		return surface.RR25d7d, nil
	case "30d":
		return surface.RR25d30d, nil
	default:
		return 0, fmt.Errorf("deribit: skew_rr: unsupported tenor %q (use 7d or 30d)", tenor)
	}
}

// TermSlope implements ports.OptionsIVPort.
func (c *Client) TermSlope(ctx context.Context, asset string) (float64, error) {
	surface, err := c.Surface(ctx, asset)
	if err != nil {
		return 0, err
	}
	return surface.TermSlope, nil
}

// doGet performs an HTTP GET with rate limiting and retry on 429/5xx.
func (c *Client) doGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	var lastErr error
	for attempt := range maxRetries {
		if err := c.acquire(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("deribit: new request: %w", err)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			c.backoff(ctx, attempt)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			c.backoff(ctx, attempt)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
			c.log.Warn().
				Int("status", resp.StatusCode).
				Str("path", path).
				Int("attempt", attempt+1).
				Msg("retryable error")
			c.backoff(ctx, attempt)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("deribit: HTTP %d from %s: %s", resp.StatusCode, path, string(body))
		}

		return body, nil
	}

	return nil, fmt.Errorf("deribit: %s: exhausted retries: %w", path, lastErr)
}

func (c *Client) backoff(ctx context.Context, attempt int) {
	delay := retryBaseDelay << uint(attempt) // 500ms, 1s, 2s
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}
