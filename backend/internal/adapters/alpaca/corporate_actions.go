// Corporate-actions client for Sprint 6.3. The Alpaca Broker/Trading API
// documents a /v2/corporate_actions announcements endpoint
// (https://docs.alpaca.markets/reference/get-v2-corporate-actions-announcements,
// TODO-verify the exact path/query params once we integrate a real API key
// — the doc set moved during the v2 overhaul). This client is API-key-gated
// and nil-safe the same way thetadata.Client is: a zero client simply
// returns an empty slice with no error so callers can wire it
// unconditionally.
package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// CorporateActionsClient fetches split/dividend announcements from Alpaca.
type CorporateActionsClient struct {
	baseURL   string
	apiKey    string
	apiSecret string
	http      *http.Client
	log       zerolog.Logger
}

// NewCorporateActionsClient returns nil (with no error) when credentials are
// empty. Returning nil mirrors thetadata.NewClient so the composite chain
// can skip this source without extra branching.
func NewCorporateActionsClient(baseURL, apiKey, apiSecret string, log zerolog.Logger) *CorporateActionsClient {
	if apiKey == "" || apiSecret == "" {
		return nil
	}
	return &CorporateActionsClient{
		baseURL:   strings.TrimSuffix(baseURL, "/"),
		apiKey:    apiKey,
		apiSecret: apiSecret,
		http:      &http.Client{Timeout: 10 * time.Second},
		log:       log.With().Str("component", "alpaca_corporate_actions").Logger(),
	}
}

// corpActionAnnouncement mirrors the subset of fields we use from the Alpaca
// /v2/corporate_actions/announcements payload. Fields we don't consume are
// intentionally omitted so JSON decoding stays forward-compatible.
type corpActionAnnouncement struct {
	CAType            string `json:"ca_type"`         // 'split' | 'dividend' | 'merger' | ...
	CASubType         string `json:"ca_sub_type"`     // e.g., 'stock_split', 'reverse_split'
	InitiatingSymbol  string `json:"initiating_symbol"`
	TargetSymbol      string `json:"target_symbol"`
	EffectiveDate     string `json:"effective_date"`  // YYYY-MM-DD
	OldRate           string `json:"old_rate"`        // pre-action shares count, string-encoded
	NewRate           string `json:"new_rate"`        // post-action shares count
	CashAmount        string `json:"cash"`            // dividend cash per share
}

// GetAllSplits returns split and reverse-split actions for all symbols in
// [from, to] with a single API call. The Alpaca endpoint returns all matching
// announcements when the symbol parameter is omitted, eliminating the N+1
// per-symbol pattern.
func (c *CorporateActionsClient) GetAllSplits(ctx context.Context, from, to time.Time) ([]ports.CorporateAction, error) {
	if c == nil {
		return nil, nil
	}
	params := url.Values{}
	params.Set("ca_types", "forward_split,reverse_split")
	params.Set("since", from.Format("2006-01-02"))
	params.Set("until", to.Format("2006-01-02"))

	reqURL := fmt.Sprintf("%s/v2/corporate_actions/announcements?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("alpaca_corporate_actions: new request: %w", err)
	}
	req.Header.Set(headerAPIKey, c.apiKey)
	req.Header.Set(headerAPISecret, c.apiSecret)

	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn().Err(err).Msg("alpaca corp actions batch request failed")
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Warn().Int("status", resp.StatusCode).Msg("alpaca corp actions batch non-2xx")
		return nil, nil
	}

	var anns []corpActionAnnouncement
	if err := json.NewDecoder(resp.Body).Decode(&anns); err != nil {
		c.log.Warn().Err(err).Msg("alpaca corp actions batch decode failed")
		return nil, nil
	}

	return c.parseAnnouncements(anns), nil
}

// GetSplits returns split and reverse-split actions for a single symbol in
// [from, to]. Prefer GetAllSplits when refreshing the full universe.
func (c *CorporateActionsClient) GetSplits(ctx context.Context, symbol string, from, to time.Time) ([]ports.CorporateAction, error) {
	if c == nil {
		return nil, nil
	}
	params := url.Values{}
	params.Set("ca_types", "forward_split,reverse_split")
	params.Set("since", from.Format("2006-01-02"))
	params.Set("until", to.Format("2006-01-02"))
	params.Set("symbol", symbol)

	reqURL := fmt.Sprintf("%s/v2/corporate_actions/announcements?%s", c.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("alpaca_corporate_actions: new request: %w", err)
	}
	req.Header.Set(headerAPIKey, c.apiKey)
	req.Header.Set(headerAPISecret, c.apiSecret)

	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn().Err(err).Str("symbol", symbol).Msg("alpaca corp actions request failed")
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.log.Warn().Int("status", resp.StatusCode).Str("symbol", symbol).Msg("alpaca corp actions non-2xx")
		return nil, nil
	}

	var anns []corpActionAnnouncement
	if err := json.NewDecoder(resp.Body).Decode(&anns); err != nil {
		c.log.Warn().Err(err).Str("symbol", symbol).Msg("alpaca corp actions decode failed")
		return nil, nil
	}

	return c.parseAnnouncements(anns), nil
}

func (c *CorporateActionsClient) parseAnnouncements(anns []corpActionAnnouncement) []ports.CorporateAction {
	out := make([]ports.CorporateAction, 0, len(anns))
	for _, a := range anns {
		effDate, err := time.Parse("2006-01-02", a.EffectiveDate)
		if err != nil {
			continue
		}
		sym := a.InitiatingSymbol
		if sym == "" {
			sym = a.TargetSymbol
		}
		oldRate, _ := strconv.ParseFloat(a.OldRate, 64)
		newRate, _ := strconv.ParseFloat(a.NewRate, 64)
		if oldRate <= 0 {
			oldRate = 1
		}
		if newRate <= 0 {
			newRate = 1
		}

		actionType := "split"
		if strings.Contains(strings.ToLower(a.CASubType), "reverse") || newRate < oldRate {
			actionType = "reverse_split"
		}

		cash, _ := strconv.ParseFloat(a.CashAmount, 64)
		out = append(out, ports.CorporateAction{
			Symbol:           sym,
			ActionType:       actionType,
			EffectiveDate:    effDate,
			RatioNumerator:   newRate,
			RatioDenominator: oldRate,
			CashComponent:    cash,
			Source:           "alpaca",
		})
	}
	return out
}
