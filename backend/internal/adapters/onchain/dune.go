// Package onchain provides a read-only adapter for tracking whale/custodian
// on-chain flows via the Dune Analytics API. No transactions or signing are
// performed — this is purely a data consumption layer for confluence scoring.
package onchain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"
)

const (
	defaultDuneBaseURL = "https://api.dune.com/api/v1/"
	defaultDuneTimeout = 30 * time.Second
	pollInterval       = 2 * time.Second
	maxPollAttempts    = 60 // 2 min max wait for query execution
)

// Sentinel errors for the Dune adapter.
var (
	ErrDuneRateLimit    = errors.New("dune: rate limit exceeded")
	ErrDuneTimeout      = errors.New("dune: query execution timed out")
	ErrDuneInvalidResp  = errors.New("dune: invalid response")
	ErrDuneQueryFailed  = errors.New("dune: query execution failed")
	ErrDuneNotConfigured = errors.New("dune: API key not configured")
)

// HTTPDoer is the minimal interface the client needs from net/http.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// DuneClient is a read-only REST client for the Dune Analytics API.
type DuneClient struct {
	baseURL string
	apiKey  string
	http    HTTPDoer
	log     zerolog.Logger
}

// NewDuneClient creates a Dune client with the given API key and base URL.
func NewDuneClient(apiKey, baseURL string, log zerolog.Logger) (*DuneClient, error) {
	if apiKey == "" {
		return nil, ErrDuneNotConfigured
	}
	if baseURL == "" {
		baseURL = defaultDuneBaseURL
	}
	return &DuneClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: defaultDuneTimeout},
		log:     log.With().Str("component", "dune_client").Logger(),
	}, nil
}

// NewDuneClientWithHTTP creates a client using the provided HTTPDoer (useful for testing).
func NewDuneClientWithHTTP(apiKey, baseURL string, doer HTTPDoer, log zerolog.Logger) *DuneClient {
	if baseURL == "" {
		baseURL = defaultDuneBaseURL
	}
	return &DuneClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    doer,
		log:     log.With().Str("component", "dune_client").Logger(),
	}
}

// executeResponse is the Dune API response for query execution.
type executeResponse struct {
	ExecutionID string `json:"execution_id"`
	State       string `json:"state"`
}

// resultResponse is the Dune API response for query results.
type resultResponse struct {
	ExecutionID string `json:"execution_id"`
	State       string `json:"state"`
	Result      *struct {
		Rows []map[string]any `json:"rows"`
	} `json:"result"`
}

// latestResultResponse is the Dune API response for latest cached results.
type latestResultResponse struct {
	ExecutionID string `json:"execution_id"`
	State       string `json:"state"`
	Result      *struct {
		Rows []map[string]any `json:"rows"`
	} `json:"result"`
}

// ExecuteQuery triggers a query execution on Dune and returns the execution ID.
func (c *DuneClient) ExecuteQuery(ctx context.Context, queryID int, params map[string]string) (string, error) {
	url := fmt.Sprintf("%sexecution/%d/execute", c.baseURL, queryID)

	body := map[string]any{}
	if len(params) > 0 {
		qp := make([]map[string]string, 0, len(params))
		for k, v := range params {
			qp = append(qp, map[string]string{"key": k, "value": v})
		}
		body["query_parameters"] = qp
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("dune: marshal execute body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, io.NopCloser(
		readCloserFromBytes(bodyBytes),
	))
	if err != nil {
		return "", fmt.Errorf("dune: create execute request: %w", err)
	}
	req.Header.Set("x-dune-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("dune: execute query %d: %w", queryID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrDuneRateLimit
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dune: execute query %d: status %d", queryID, resp.StatusCode)
	}

	var execResp executeResponse
	if err := json.NewDecoder(resp.Body).Decode(&execResp); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDuneInvalidResp, err)
	}

	c.log.Debug().
		Int("query_id", queryID).
		Str("execution_id", execResp.ExecutionID).
		Msg("query execution started")

	return execResp.ExecutionID, nil
}

// GetResults polls for query execution results until completion or timeout.
func (c *DuneClient) GetResults(ctx context.Context, executionID string) ([]map[string]any, error) {
	url := fmt.Sprintf("%sexecution/%s/results", c.baseURL, executionID)

	for attempt := 0; attempt < maxPollAttempts; attempt++ {
		rows, done, err := c.fetchResults(ctx, url)
		if err != nil {
			return nil, err
		}
		if done {
			return rows, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	return nil, ErrDuneTimeout
}

// fetchResults makes a single GET to the results endpoint. Returns (rows, done, err).
func (c *DuneClient) fetchResults(ctx context.Context, url string) ([]map[string]any, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("dune: create results request: %w", err)
	}
	req.Header.Set("x-dune-api-key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("dune: get results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, false, ErrDuneRateLimit
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("dune: get results: status %d", resp.StatusCode)
	}

	var rr resultResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrDuneInvalidResp, err)
	}

	switch rr.State {
	case "QUERY_STATE_COMPLETED":
		if rr.Result == nil {
			return nil, true, nil
		}
		return rr.Result.Rows, true, nil
	case "QUERY_STATE_PENDING", "QUERY_STATE_EXECUTING":
		return nil, false, nil
	case "QUERY_STATE_FAILED":
		return nil, false, ErrDuneQueryFailed
	default:
		return nil, false, fmt.Errorf("dune: unexpected state %q", rr.State)
	}
}

// GetLatestResults returns the cached results from the most recent execution
// of the given query. This avoids triggering a new execution and consuming credits.
func (c *DuneClient) GetLatestResults(ctx context.Context, queryID int) ([]map[string]any, error) {
	url := fmt.Sprintf("%squery/%d/results", c.baseURL, queryID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("dune: create latest results request: %w", err)
	}
	req.Header.Set("x-dune-api-key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dune: get latest results for query %d: %w", queryID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrDuneRateLimit
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dune: get latest results: status %d for query %d", resp.StatusCode, queryID)
	}

	var lr latestResultResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDuneInvalidResp, err)
	}

	if lr.Result == nil {
		return nil, nil
	}
	return lr.Result.Rows, nil
}

// readCloserFromBytes wraps a byte slice in an io.Reader.
func readCloserFromBytes(b []byte) io.Reader {
	return &bytesReader{data: b}
}

type bytesReader struct {
	data []byte
	off  int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

// parseFloat64 safely extracts a float64 from a map value that may be
// string, float64, or json.Number.
func parseFloat64(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	case json.Number:
		f, _ := val.Float64()
		return f
	default:
		return 0
	}
}

// parseString safely extracts a string from a map value.
func parseString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
