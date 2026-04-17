// Package coinbase implements a read-only adapter over the Coinbase Exchange
// public market-data REST API. It exists so the omo backfill and warm-up paths
// can source crypto historical bars from a real USD venue with continuous
// volume, replacing the Alpaca US-only crypto feed whose 1m/5m bars contain
// 60%+ zero-volume gaps during 2024.
//
// Only the public /products/{product-id}/candles endpoint is wired today; no
// authentication is needed. Rate limits are enforced client-side with a
// conservative 8 req/s budget against the 10 req/s public cap to leave
// headroom for bursty backfills.
package coinbase

import (
	"errors"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/time/rate"

	"github.com/oh-my-opentrade/backend/internal/config"
)

// ErrSymbolUnknown is returned when Coinbase does not recognize the product id
// derived from the supplied domain.Symbol (HTTP 404 / NotFound on the candles
// endpoint). Callers may treat this as a soft failure and fall back to another
// data source rather than aborting the whole backfill.
var ErrSymbolUnknown = errors.New("coinbase: unknown product")

// Default values mirror what `Load` seeds into CoinbaseConfig so adapter
// callers get sensible behavior even when the YAML file omits the section.
const (
	defaultBaseURL    = "https://api.exchange.coinbase.com"
	defaultRateLimit  = 8
	defaultTimeoutSec = 30
	// maxCandlesPerRequest is the hard page size Coinbase returns for the
	// /candles endpoint. Documented: up to 300 candles per response.
	maxCandlesPerRequest = 300
	// maxRetries is the total attempts per HTTP call (1 initial + N retries).
	// Applied to 429 and 5xx responses with exponential backoff.
	maxRetries = 3
)

// Client is a read-only wrapper around the Coinbase Exchange public REST API.
// It is safe for concurrent use; the embedded rate limiter serializes request
// pacing across goroutines.
type Client struct {
	httpClient *http.Client
	limiter    *rate.Limiter
	baseURL    string
	log        zerolog.Logger
}

// NewClient constructs a Coinbase REST client with the provided configuration.
// Zero-valued fields in cfg are replaced with package defaults so callers can
// pass a nearly-empty CoinbaseConfig during tests.
func NewClient(cfg config.CoinbaseConfig, log zerolog.Logger) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	rps := cfg.RateLimitRPS
	if rps <= 0 {
		rps = defaultRateLimit
	}
	timeoutSec := cfg.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSec
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
		},
		limiter: rate.NewLimiter(rate.Limit(rps), rps),
		baseURL: baseURL,
		log:     log.With().Str("component", "coinbase").Logger(),
	}
}
