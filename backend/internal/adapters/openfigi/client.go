package openfigi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

const (
	defaultBaseURL = "https://api.openfigi.com/v3/mapping"
	maxBatchSize = 10 // free tier limit; 100 with API key
)

// Client resolves CUSIPs to ticker symbols via the OpenFIGI API.
type Client struct {
	baseURL string
	apiKey  string
	limiter *rate.Limiter
	client  *http.Client
	log     zerolog.Logger
}

// NewClient creates an OpenFIGI client. If apiKey is non-empty, the higher
// authenticated rate limit is used (20 req/s vs ~10 req/min).
func NewClient(apiKey string, log zerolog.Logger) *Client {
	lim := rate.NewLimiter(rate.Every(6*time.Second), 1) // ~10 req/min without key
	if apiKey != "" {
		lim = rate.NewLimiter(rate.Limit(20), 1) // 20 req/sec with key
	}
	return &Client{
		baseURL: defaultBaseURL,
		apiKey:  apiKey,
		limiter: lim,
		client:  &http.Client{Timeout: 30 * time.Second},
		log:     log.With().Str("component", "openfigi").Logger(),
	}
}

// mappingRequest is a single item in the OpenFIGI batch request.
type mappingRequest struct {
	IDType  string `json:"idType"`
	IDValue string `json:"idValue"`
}

// figiResult is a single resolved entry within a response array element.
type figiResult struct {
	FIGI     string `json:"figi"`
	Ticker   string `json:"ticker"`
	Name     string `json:"name"`
	ExchCode string `json:"exchCode"`
	Warning  string `json:"warning"`
}

// ResolveCUSIPs maps a slice of CUSIPs to ticker symbols via OpenFIGI.
// Automatically batches into chunks of 100 (API limit).
// Returns a map of CUSIP -> CUSIPMapping for successfully resolved CUSIPs.
func (c *Client) ResolveCUSIPs(ctx context.Context, cusips []string) (map[string]domain.CUSIPMapping, error) {
	// Deduplicate.
	seen := make(map[string]struct{}, len(cusips))
	unique := make([]string, 0, len(cusips))
	for _, cu := range cusips {
		if _, ok := seen[cu]; ok {
			continue
		}
		seen[cu] = struct{}{}
		unique = append(unique, cu)
	}

	result := make(map[string]domain.CUSIPMapping, len(unique))

	// Process in chunks of maxBatchSize.
	for i := 0; i < len(unique); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[i:end]

		resolved, err := c.resolveChunk(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("openfigi: resolve chunk %d-%d: %w", i, end, err)
		}
		for k, v := range resolved {
			result[k] = v
		}
	}

	c.log.Info().
		Int("requested", len(cusips)).
		Int("unique", len(unique)).
		Int("resolved", len(result)).
		Msg("CUSIP resolution complete")

	return result, nil
}

// resolveChunk resolves a single batch (<=100 CUSIPs) against the OpenFIGI API.
func (c *Client) resolveChunk(ctx context.Context, cusips []string) (map[string]domain.CUSIPMapping, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("openfigi: rate limiter: %w", err)
	}

	// Build request body.
	reqs := make([]mappingRequest, len(cusips))
	for i, cu := range cusips {
		reqs[i] = mappingRequest{IDType: "ID_CUSIP", IDValue: cu}
	}
	body, err := json.Marshal(reqs)
	if err != nil {
		return nil, fmt.Errorf("openfigi: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openfigi: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-OPENFIGI-APIKEY", c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openfigi: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openfigi: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	// Response is an array of arrays/objects. Each top-level element corresponds
	// to the request at the same index. A successful lookup returns an array of
	// result objects; a failed lookup returns an object with a "warning" field.
	var raw []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("openfigi: decode response: %w", err)
	}

	if len(raw) != len(cusips) {
		return nil, fmt.Errorf("openfigi: response length %d != request length %d", len(raw), len(cusips))
	}

	now := time.Now()
	result := make(map[string]domain.CUSIPMapping, len(cusips))

	for idx, entry := range raw {
		cusip := cusips[idx]

		// Response format: {"data":[{figi, ticker, ...}]} or {"warning":"..."}
		var wrapper struct {
			Data    []figiResult `json:"data"`
			Warning string       `json:"warning"`
		}
		if err := json.Unmarshal(entry, &wrapper); err != nil {
			c.log.Debug().Str("cusip", cusip).Msg("failed to parse response entry")
			continue
		}
		if wrapper.Warning != "" {
			c.log.Debug().Str("cusip", cusip).Str("warning", wrapper.Warning).Msg("CUSIP not found")
			continue
		}
		if len(wrapper.Data) > 0 {
			r := wrapper.Data[0]
			if r.Ticker == "" {
				c.log.Debug().Str("cusip", cusip).Msg("no ticker in response")
				continue
			}
			result[cusip] = domain.CUSIPMapping{
				CUSIP:      cusip,
				Ticker:     r.Ticker,
				FIGI:       r.FIGI,
				ResolvedAt: now,
			}
			continue
		}



		c.log.Warn().Str("cusip", cusip).RawJSON("raw", entry).Msg("could not parse response entry")
	}

	return result, nil
}
