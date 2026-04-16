// Package hyperliquid implements adapters for the Hyperliquid perpetual
// exchange, providing BrokerPort, FundingRatesPort, and OpenInterestPort
// implementations against the documented HTTP + WebSocket protocol.
package hyperliquid

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/time/rate"

	"github.com/oh-my-opentrade/backend/internal/config"
)

// Base URL constants for the Hyperliquid API.
const (
	MainnetBaseURL = "https://api.hyperliquid.xyz"
	TestnetBaseURL = "https://api.hyperliquid-testnet.xyz"

	MainnetWSURL = "wss://api.hyperliquid.xyz/ws"
	TestnetWSURL = "wss://api.hyperliquid-testnet.xyz/ws"

	infoPath     = "/info"
	exchangePath = "/exchange"

	// Rate limits per Hyperliquid docs.
	readRateLimit  = 1200 // requests per minute
	writeRateLimit = 100  // requests per minute

	defaultTimeout     = 10 * time.Second
	maxRetries         = 5
	baseRetryBackoff   = 2 * time.Second
	maxRetryBackoff    = 30 * time.Second
)

// Sentinel errors for the Hyperliquid adapter.
var (
	ErrAuth               = errors.New("hyperliquid: authentication failed")
	ErrRateLimit          = errors.New("hyperliquid: rate limit exceeded")
	ErrInsufficientMargin = errors.New("hyperliquid: insufficient margin")
	ErrOrderNotFound      = errors.New("hyperliquid: order not found")
	ErrInvalidResponse    = errors.New("hyperliquid: invalid response")
	ErrAssetNotFound      = errors.New("hyperliquid: asset not found")
	ErrNotConfigured      = errors.New("hyperliquid: private key not configured")
	ErrStreamNotSupported = errors.New("hyperliquid: streaming not supported in read-only adapter")
)

// Client is the shared HTTP client for all Hyperliquid API interactions.
// It handles authentication (EIP-712 signing), rate limiting, retries with
// backoff, and asset ID resolution.
type Client struct {
	baseURL      string
	wsURL        string
	address      string
	vaultAddress string
	privateKey   *ecdsa.PrivateKey
	httpClient   *http.Client
	readLimiter  *rate.Limiter
	writeLimiter *rate.Limiter
	log          zerolog.Logger

	// Asset mapping: coin name → internal integer ID. Populated lazily from
	// the meta endpoint and cached for the lifetime of the client.
	assetMu  sync.RWMutex
	assetMap map[string]int // "BTC" → 0, "ETH" → 1, etc.
}

// NewClient creates a shared HTTP client for Hyperliquid API calls.
// If cfg.PrivateKey is empty the client is read-only (info endpoints work,
// exchange endpoints return ErrNotConfigured).
func NewClient(cfg config.HyperliquidConfig, log zerolog.Logger) (*Client, error) {
	baseURL := TestnetBaseURL
	wsURL := TestnetWSURL
	if cfg.Network == "mainnet" {
		baseURL = MainnetBaseURL
		wsURL = MainnetWSURL
	}

	c := &Client{
		baseURL: baseURL,
		wsURL:   wsURL,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		readLimiter:  rate.NewLimiter(rate.Limit(float64(readRateLimit)/60.0), readRateLimit/10),
		writeLimiter: rate.NewLimiter(rate.Limit(float64(writeRateLimit)/60.0), writeRateLimit/10),
		log:          log.With().Str("component", "hyperliquid_client").Logger(),
		assetMap:     make(map[string]int),
		vaultAddress: cfg.VaultAddress,
	}

	if cfg.PrivateKey != "" {
		pk, err := parsePrivateKey(cfg.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("hyperliquid: parse private key: %w", err)
		}
		c.privateKey = pk
		c.address = deriveAddress(pk)
	}

	// Allow explicit address override (e.g. for read-only queries on a
	// different account).
	if cfg.Address != "" {
		c.address = cfg.Address
	}

	return c, nil
}

// Address returns the Ethereum address used for API queries.
func (c *Client) Address() string { return c.address }

// BaseURL returns the configured API base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// WSURL returns the configured WebSocket URL.
func (c *Client) WSURL() string { return c.wsURL }

// PostInfo sends a read request to the /info endpoint with rate limiting and
// retry logic. The body is JSON-marshaled before sending.
func (c *Client) PostInfo(ctx context.Context, body any) (json.RawMessage, error) {
	return c.post(ctx, infoPath, body, c.readLimiter, false)
}

// PostExchange sends a signed write request to the /exchange endpoint. The
// action and nonce are signed with the configured private key using EIP-712
// typed data signing. Returns ErrNotConfigured if no private key is set.
func (c *Client) PostExchange(ctx context.Context, action any, nonce int64) (json.RawMessage, error) {
	if c.privateKey == nil {
		return nil, ErrNotConfigured
	}

	actionBytes, err := json.Marshal(action)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: marshal action: %w", err)
	}

	sig, err := c.signAction(actionBytes, nonce)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: sign action: %w", err)
	}

	payload := exchangeRequest{
		Action:    action,
		Nonce:     nonce,
		Signature: sig,
	}
	if c.vaultAddress != "" {
		payload.VaultAddress = &c.vaultAddress
	}

	return c.post(ctx, exchangePath, payload, c.writeLimiter, true)
}

// ResolveAsset maps a coin name (e.g. "BTC") to its internal integer asset
// ID. The mapping is populated lazily from the meta endpoint on first call
// and cached thereafter.
func (c *Client) ResolveAsset(ctx context.Context, coin string) (int, error) {
	c.assetMu.RLock()
	if id, ok := c.assetMap[coin]; ok {
		c.assetMu.RUnlock()
		return id, nil
	}
	c.assetMu.RUnlock()

	if err := c.refreshAssetMap(ctx); err != nil {
		return 0, err
	}

	c.assetMu.RLock()
	defer c.assetMu.RUnlock()
	id, ok := c.assetMap[coin]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrAssetNotFound, coin)
	}
	return id, nil
}

// refreshAssetMap fetches the meta endpoint and populates the coin → asset ID
// mapping.
func (c *Client) refreshAssetMap(ctx context.Context) error {
	raw, err := c.PostInfo(ctx, infoRequest{Type: "meta"})
	if err != nil {
		return fmt.Errorf("hyperliquid: refresh asset map: %w", err)
	}

	var meta metaResponse
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("hyperliquid: unmarshal meta: %w", err)
	}

	c.assetMu.Lock()
	defer c.assetMu.Unlock()
	for i, u := range meta.Universe {
		c.assetMap[u.Name] = i
	}
	return nil
}

// SetAssetMap injects a pre-built asset mapping, useful for testing.
func (c *Client) SetAssetMap(m map[string]int) {
	c.assetMu.Lock()
	defer c.assetMu.Unlock()
	c.assetMap = m
}

// post is the shared POST helper with rate limiting, retries, and error
// classification.
func (c *Client) post(ctx context.Context, path string, body any, limiter *rate.Limiter, isWrite bool) (json.RawMessage, error) {
	if err := limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("hyperliquid: rate limiter wait: %w", err)
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid: marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := baseRetryBackoff * time.Duration(1<<uint(attempt-1))
			if backoff > maxRetryBackoff {
				backoff = maxRetryBackoff
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("hyperliquid: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("hyperliquid: http request: %w", err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("hyperliquid: read response: %w", err)
			continue
		}

		switch resp.StatusCode {
		case http.StatusOK:
			return json.RawMessage(respBody), nil
		case http.StatusTooManyRequests:
			lastErr = ErrRateLimit
			continue
		case http.StatusForbidden:
			return nil, ErrAuth
		default:
			lastErr = classifyError(resp.StatusCode, respBody)
			if !isRetryable(resp.StatusCode) {
				return nil, lastErr
			}
		}
	}
	return nil, fmt.Errorf("hyperliquid: max retries exhausted: %w", lastErr)
}

// classifyError inspects the status code and body to return a descriptive error.
func classifyError(statusCode int, body []byte) error {
	msg := string(body)
	if strings.Contains(msg, "insufficient margin") || strings.Contains(msg, "Insufficient margin") {
		return fmt.Errorf("%w: %s", ErrInsufficientMargin, msg)
	}
	return fmt.Errorf("%w: status %d: %s", ErrInvalidResponse, statusCode, msg)
}

// isRetryable returns true for transient HTTP error codes.
func isRetryable(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

// ──────────────────────────────────────────────────────────────────────────
// EIP-712 signing helpers
// ──────────────────────────────────────────────────────────────────────────

// signAction produces an EIP-712 signature over the action payload.
// Hyperliquid uses a simplified EIP-712 scheme: the "message" to sign is
// keccak256(action_json || nonce_bytes). We produce a standard Ethereum
// personal-sign compatible signature (v, r, s) encoded as a hex string.
func (c *Client) signAction(actionJSON []byte, nonce int64) (Signature, error) {
	hash := hashAction(actionJSON, nonce)
	return signHash(c.privateKey, hash)
}

// Signature holds the three EIP-712 signature components as hex strings
// without "0x" prefix. Serialized to JSON for the exchange endpoint.
type Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V int    `json:"v"`
}

// hashAction produces the keccak256 digest that Hyperliquid expects to be
// signed. The scheme is: keccak256(abi.encodePacked(action_hash, nonce))
// where action_hash = keccak256(action_json_bytes).
func hashAction(actionJSON []byte, nonce int64) []byte {
	actionHash := keccak256(actionJSON)
	nonceBig := big.NewInt(nonce)
	nonceBytes := make([]byte, 8)
	nonceBig.FillBytes(nonceBytes)
	packed := make([]byte, 0, len(actionHash)+len(nonceBytes))
	packed = append(packed, actionHash...)
	packed = append(packed, nonceBytes...)
	return keccak256(packed)
}

// signHash signs a 32-byte hash with the private key and returns (r, s, v).
func signHash(key *ecdsa.PrivateKey, hash []byte) (Signature, error) {
	// Ethereum personal_sign prefix
	prefix := []byte("\x19Ethereum Signed Message:\n32")
	prefixed := keccak256(append(prefix, hash...))

	sig, err := ecSign(key, prefixed)
	if err != nil {
		return Signature{}, fmt.Errorf("hyperliquid: ecdsa sign: %w", err)
	}
	return sig, nil
}

// ──────────────────────────────────────────────────────────────────────────
// Request / response types
// ──────────────────────────────────────────────────────────────────────────

type infoRequest struct {
	Type string `json:"type"`
}

type exchangeRequest struct {
	Action       any       `json:"action"`
	Nonce        int64     `json:"nonce"`
	Signature    Signature `json:"signature"`
	VaultAddress *string   `json:"vaultAddress,omitempty"`
}

type metaResponse struct {
	Universe []metaAsset `json:"universe"`
}

type metaAsset struct {
	Name       string `json:"name"`
	SzDecimals int    `json:"szDecimals"`
}
