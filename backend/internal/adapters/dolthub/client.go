// Package dolthub provides a client for the DoltHub SQL API to fetch
// historical option chain data from the post-no-preference/options database.
package dolthub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

const (
	baseURL    = "https://www.dolthub.com/api/v1alpha1/post-no-preference/options/master"
	maxPerPage = 5000
)

// Client fetches historical option chain data from the DoltHub SQL API.
type Client struct {
	http *http.Client
	log  zerolog.Logger
}

// NewClient creates a new DoltHub API client.
func NewClient(httpClient *http.Client, log zerolog.Logger) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{http: httpClient, log: log.With().Str("component", "dolthub").Logger()}
}

// apiResponse is the envelope returned by the DoltHub SQL API.
type apiResponse struct {
	QueryExecutionStatus  string `json:"query_execution_status"`
	QueryExecutionMessage string `json:"query_execution_message"`
	RepositoryOwner       string `json:"repository_owner"`
	RepositoryName        string `json:"repository_name"`
	SchemaLen             int    `json:"schema_len"`
	Rows                  []row  `json:"rows"`
}

// row is a single result row from the DoltHub API (values as strings keyed by column name).
type row = map[string]string

// FetchChain fetches all option chain rows for a symbol on a single date from DoltHub.
func (c *Client) FetchChain(ctx context.Context, symbol string, date time.Time) ([]domain.HistoricalOptionChainRow, error) {
	dateStr := date.Format("2006-01-02")
	var all []domain.HistoricalOptionChainRow
	offset := 0

	for {
		q := fmt.Sprintf(
			"SELECT date, act_symbol, expiration, strike, call_put, bid, ask, vol, delta, gamma, theta, vega, rho "+
				"FROM option_chain WHERE act_symbol = '%s' AND date = '%s' "+
				"ORDER BY expiration, strike, call_put LIMIT %d OFFSET %d",
			symbol, dateStr, maxPerPage, offset)

		resp, err := c.query(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("dolthub query failed for %s %s: %w", symbol, dateStr, err)
		}

		if resp.QueryExecutionStatus != "Success" {
			return nil, fmt.Errorf("dolthub query error: %s", resp.QueryExecutionMessage)
		}

		for _, r := range resp.Rows {
			parsed, err := parseRow(r)
			if err != nil {
				c.log.Warn().Err(err).Str("symbol", symbol).Str("date", dateStr).Msg("skipping malformed row")
				continue
			}
			all = append(all, parsed)
		}

		if len(resp.Rows) < maxPerPage {
			break
		}
		offset += maxPerPage
	}

	return all, nil
}

func (c *Client) query(ctx context.Context, sql string) (*apiResponse, error) {
	u := baseURL + "?q=" + url.QueryEscape(sql)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dolthub HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("dolthub JSON decode: %w", err)
	}
	return &result, nil
}

func parseRow(r row) (domain.HistoricalOptionChainRow, error) {
	date, err := time.Parse("2006-01-02", r["date"])
	if err != nil {
		return domain.HistoricalOptionChainRow{}, fmt.Errorf("parse date %q: %w", r["date"], err)
	}
	expiry, err := time.Parse("2006-01-02", r["expiration"])
	if err != nil {
		return domain.HistoricalOptionChainRow{}, fmt.Errorf("parse expiration %q: %w", r["expiration"], err)
	}

	right := domain.OptionRightCall
	if r["call_put"] == "Put" {
		right = domain.OptionRightPut
	}

	return domain.HistoricalOptionChainRow{
		Date:       date,
		Symbol:     domain.Symbol(r["act_symbol"]),
		Expiration: expiry,
		Strike:     parseFloat(r["strike"]),
		Right:      right,
		Bid:        parseFloat(r["bid"]),
		Ask:        parseFloat(r["ask"]),
		IV:         parseFloat(r["vol"]),
		Delta:      parseFloat(r["delta"]),
		Gamma:      parseFloat(r["gamma"]),
		Theta:      parseFloat(r["theta"]),
		Vega:       parseFloat(r["vega"]),
		Rho:        parseFloat(r["rho"]),
	}, nil
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
