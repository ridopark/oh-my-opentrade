package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// FormatOCCSymbol produces the OCC option ticker string.
// Format: {UNDERLYING}{YYMMDD}{C|P}{strike * 1000 zero-padded to 8 digits}
// Example: AAPL240119C00190000 for AAPL $190 call expiring 2024-01-19.
func FormatOCCSymbol(underlying string, expiry time.Time, right domain.OptionRight, strike float64) string {
	dateStr := expiry.Format("060102") // YYMMDD
	rightChar := "C"
	if right == domain.OptionRightPut {
		rightChar = "P"
	}
	strikeInt := int(math.Round(strike * 1000))
	return fmt.Sprintf("%s%s%s%08d", underlying, dateStr, rightChar, strikeInt)
}

// alpacaOptionsContractListResponse is the Alpaca broker API response for listing option contracts.
// Endpoint: GET /v2/options/contracts
type alpacaOptionsContractListResponse struct {
	OptionContracts []alpacaOptionsContractItem `json:"option_contracts"`
	NextPageToken   *string                     `json:"next_page_token"`
}

type alpacaOptionsContractItem struct {
	Symbol           string `json:"symbol"`
	UnderlyingSymbol string `json:"underlying_symbol"`
	ExpirationDate   string `json:"expiration_date"`
	StrikePrice      string `json:"strike_price"`
	Type             string `json:"type"` // "call" or "put"
	Style            string `json:"style"`
	Multiplier       string `json:"multiplier"`
	OpenInterest     string `json:"open_interest"`
	Tradable         bool   `json:"tradable"`
	Status           string `json:"status"`
}

// alpacaOptionsSnapshotResponse is the Alpaca data API response for option snapshots.
// Endpoint: GET /v1beta1/options/snapshots
type alpacaOptionsSnapshotResponse struct {
	Snapshots     map[string]alpacaOptionSnapshot `json:"snapshots"`
	NextPageToken *string                         `json:"next_page_token"`
}

type alpacaOptionSnapshot struct {
	Greeks struct {
		Delta float64 `json:"delta"`
		Gamma float64 `json:"gamma"`
		Theta float64 `json:"theta"`
		Vega  float64 `json:"vega"`
		Rho   float64 `json:"rho"`
	} `json:"greeks"`
	ImpliedVolatility float64 `json:"impliedVolatility"`
	LatestQuote       struct {
		BP float64 `json:"bp"`
		AP float64 `json:"ap"`
		BS int     `json:"bs"`
		AS int     `json:"as"`
		// C is a trade condition code string ("A", "I", etc.) — not a price; omitted.
	} `json:"latestQuote"`
	LatestTrade struct {
		P float64 `json:"p"` // last trade price
	} `json:"latestTrade"`
	OpenInterest int `json:"openInterest"`
}

// GetOptionChain retrieves option contract snapshots with greeks and quotes for
// the given underlying symbol, calendar-date window, and option right.
//
// expiryFrom/expiryTo are inclusive date bounds on the Alpaca
// expiration_date_gte/lte parameters — callers pass the full configured DTE
// range so the contract list pulls every relevant strike, not just the
// expiries ordered first by the API.
//
// Two-step process:
//  1. Fetch OCC contract symbols from the broker API (/v2/options/contracts),
//     paginated via next_page_token until exhausted.
//  2. Fetch live snapshots (greeks, bid/ask, IV) from the data API
//     (/v1beta1/options/snapshots).
//
// optionsChainMaxContracts (set via SetOptionsChainMaxContracts, defaulted to
// 250 by config.Load) caps the slice returned to callers — the underlying
// pagination still runs to completion so a WARN log can record the full
// chain size whenever truncation happens. Lifting the cap (-1) is the
// promote step after the operator has reviewed the divergence numbers.
func (c *RESTClient) GetOptionChain(
	ctx context.Context,
	dataURL string,
	underlying domain.Symbol,
	expiryFrom, expiryTo time.Time,
	right domain.OptionRight,
) ([]domain.OptionContractSnapshot, error) {
	if underlying == "" {
		return nil, fmt.Errorf("underlying symbol must not be empty")
	}

	rightStr := strings.ToLower(string(right))

	fromStr := expiryFrom.Format("2006-01-02")
	toStr := expiryTo.Format("2006-01-02")

	// Step 1: paginate the contract list across all pages. Mirrors
	// ListOptionContractsAsOf's loop shape so future maintenance has one
	// pagination idiom to recognize. We retain each item alongside its
	// OCC symbol because the snapshot-merge step downstream needs
	// `OpenInterest` from the contract item as a fallback.
	occSymbols := make([]string, 0, 1024)
	itemByOCC := make(map[string]alpacaOptionsContractItem, 1024)
	pageToken := ""
	for {
		contractsPath := fmt.Sprintf(
			"/v2/options/contracts?underlying_symbols=%s&expiration_date_gte=%s&expiration_date_lte=%s&type=%s&limit=1000",
			underlying.String(), fromStr, toStr, rightStr,
		)
		if pageToken != "" {
			contractsPath += "&page_token=" + pageToken
		}

		contractsResp, err := c.doReqWithOpts(ctx, http.MethodGet, contractsPath, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
		if err != nil {
			return nil, fmt.Errorf("alpaca: list option contracts: %w", err)
		}
		contractsBody, _ := io.ReadAll(contractsResp.Body)
		contractsResp.Body.Close()
		if contractsResp.StatusCode < 200 || contractsResp.StatusCode >= 300 {
			return nil, fmt.Errorf("alpaca: list option contracts failed (status %d): %s", contractsResp.StatusCode, string(contractsBody))
		}

		var contractList alpacaOptionsContractListResponse
		if err := json.NewDecoder(bytes.NewReader(contractsBody)).Decode(&contractList); err != nil {
			return nil, fmt.Errorf("alpaca: decode option contracts list: %w", err)
		}

		for _, item := range contractList.OptionContracts {
			if item.Tradable && item.Status == "active" {
				occSymbols = append(occSymbols, item.Symbol)
				itemByOCC[item.Symbol] = item
			}
		}

		if contractList.NextPageToken == nil || *contractList.NextPageToken == "" {
			break
		}
		pageToken = *contractList.NextPageToken
	}

	if len(occSymbols) == 0 {
		return nil, nil
	}

	// Shadow-mode cap. cap == 0 means uncapped (operator promoted via
	// options_chain_max_contracts: -1 in YAML, materialized as <=0 here);
	// cap > 0 truncates while emitting a WARN whenever the full set
	// exceeded the cap, so the operator can audit divergence in logs
	// before promoting.
	cap := c.optionsChainMaxContracts
	fullCount := len(occSymbols)
	if cap > 0 && fullCount > cap {
		c.log.Warn().
			Str("underlying", underlying.String()).
			Str("right", rightStr).
			Int("full_count", fullCount).
			Int("cap", cap).
			Int("dropped", fullCount-cap).
			Msg("option chain truncated by shadow-mode cap; full chain available, set options_chain_max_contracts: -1 to lift")
		occSymbols = occSymbols[:cap]
	}

	// ── Step 2: fetch snapshots (greeks, quotes) from data API ──────────────
	// Alpaca's snapshot endpoint accepts up to 100 symbols per request.
	const snapshotBatchSize = 100
	allSnapshots := make(map[string]alpacaOptionSnapshot, len(occSymbols))

	for i := 0; i < len(occSymbols); i += snapshotBatchSize {
		end := i + snapshotBatchSize
		if end > len(occSymbols) {
			end = len(occSymbols)
		}
		batch := occSymbols[i:end]

		snapshotPath := fmt.Sprintf(
			"/v1beta1/options/snapshots?symbols=%s&feed=indicative",
			strings.Join(batch, ","),
		)

		snapResp, err := retryTransient(ctx, c.log, "options_snapshots_chain", func() (*http.Response, error) {
			return c.doReqDataAPI(ctx, dataURL, http.MethodGet, snapshotPath, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
		})
		if err != nil {
			return nil, fmt.Errorf("alpaca: fetch option snapshots: %w", err)
		}
		snapBody, _ := io.ReadAll(snapResp.Body)
		snapResp.Body.Close()
		if snapResp.StatusCode < 200 || snapResp.StatusCode >= 300 {
			return nil, fmt.Errorf("alpaca: fetch option snapshots failed (status %d): %s", snapResp.StatusCode, string(snapBody))
		}

		var snapPage alpacaOptionsSnapshotResponse
		if err := json.NewDecoder(bytes.NewReader(snapBody)).Decode(&snapPage); err != nil {
			return nil, fmt.Errorf("alpaca: decode option snapshots: %w", err)
		}

		for sym, snap := range snapPage.Snapshots {
			allSnapshots[sym] = snap
		}
	}

	// ── Merge contract list with snapshot data ───────────────────────────────
	// Iterate the (possibly truncated) occSymbols slice rather than the
	// raw contract list so the cap is honored end-to-end. itemByOCC was
	// populated alongside occSymbols already filtered for tradable+active.
	snapshots := make([]domain.OptionContractSnapshot, 0, len(allSnapshots))
	for _, occ := range occSymbols {
		item := itemByOCC[occ]
		snap, hasSnap := allSnapshots[item.Symbol]
		if !hasSnap {
			// No live snapshot for this contract — skip it.
			continue
		}

		contract, err := parseOCCSymbol(item.Symbol)
		if err != nil {
			continue
		}

		greeks, err := domain.NewGreeks(snap.Greeks.Delta, snap.Greeks.Gamma, snap.Greeks.Theta, snap.Greeks.Vega, snap.Greeks.Rho, snap.ImpliedVolatility)
		if err != nil {
			greeks = domain.Greeks{}
		}

		oi := snap.OpenInterest
		if oi == 0 {
			// Fall back to broker-side open interest (end-of-day figure).
			if parsed, err := strconv.Atoi(item.OpenInterest); err == nil {
				oi = parsed
			}
		}

		snapshot := domain.OptionContractSnapshot{
			OptionContract: contract,
			OptionQuote: domain.OptionQuote{
				Bid:       snap.LatestQuote.BP,
				Ask:       snap.LatestQuote.AP,
				Last:      snap.LatestTrade.P,
				BidSize:   snap.LatestQuote.BS,
				AskSize:   snap.LatestQuote.AS,
				Timestamp: time.Now(),
			},
			Greeks:       greeks,
			OpenInterest: oi,
		}
		snapshots = append(snapshots, snapshot)
	}

	return snapshots, nil
}

// GetOptionPrices fetches live bid/ask/last quotes for a specific list of OCC contract symbols.
// Calls /v1beta1/options/snapshots directly — no broker contract lookup needed.
func (c *RESTClient) GetOptionPrices(ctx context.Context, dataURL string, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
	if len(symbols) == 0 {
		return nil, nil
	}

	syms := make([]string, len(symbols))
	for i, s := range symbols {
		syms[i] = string(s)
	}

	const batchSize = 100
	result := make(map[domain.Symbol]domain.OptionQuote, len(symbols))

	for i := 0; i < len(syms); i += batchSize {
		end := i + batchSize
		if end > len(syms) {
			end = len(syms)
		}
		batch := syms[i:end]

		snapshotPath := fmt.Sprintf("/v1beta1/options/snapshots?symbols=%s&feed=indicative", strings.Join(batch, ","))
		resp, err := retryTransient(ctx, c.log, "options_snapshots_direct", func() (*http.Response, error) {
			return c.doReqDataAPI(ctx, dataURL, http.MethodGet, snapshotPath, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
		})
		if err != nil {
			return nil, fmt.Errorf("alpaca: get option prices: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("alpaca: get option prices failed (status %d): %s", resp.StatusCode, string(body))
		}

		var page alpacaOptionsSnapshotResponse
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&page); err != nil {
			return nil, fmt.Errorf("alpaca: decode option prices: %w", err)
		}

		for sym, snap := range page.Snapshots {
			result[domain.Symbol(sym)] = domain.OptionQuote{
				Bid:       snap.LatestQuote.BP,
				Ask:       snap.LatestQuote.AP,
				Last:      snap.LatestTrade.P,
				BidSize:   snap.LatestQuote.BS,
				AskSize:   snap.LatestQuote.AS,
				Timestamp: time.Now(),
			}
		}
	}

	return result, nil
}

func (c *RESTClient) GetHistoricalOptionBars(ctx context.Context, dataURL string, symbols []domain.Symbol, start, end time.Time) (map[domain.Symbol][]domain.MarketBar, error) {
	if len(symbols) == 0 {
		return nil, nil
	}

	syms := make([]string, len(symbols))
	for i, s := range symbols {
		syms[i] = string(s)
	}

	result := make(map[domain.Symbol][]domain.MarketBar, len(symbols))
	nextToken := ""

	for {
		path := fmt.Sprintf(
			"/v1beta1/options/bars?symbols=%s&timeframe=1Min&start=%s&end=%s&limit=10000",
			strings.Join(syms, ","),
			start.UTC().Format(time.RFC3339),
			end.UTC().Format(time.RFC3339),
		)
		if nextToken != "" {
			path += "&page_token=" + nextToken
		}

		resp, err := c.doReqDataAPI(ctx, dataURL, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 2})
		if err != nil {
			return nil, fmt.Errorf("alpaca: get historical option bars: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("alpaca: get historical option bars failed (status %d): %s", resp.StatusCode, string(body))
		}

		var page struct {
			Bars map[string][]struct {
				T time.Time `json:"t"`
				O float64   `json:"o"`
				H float64   `json:"h"`
				L float64   `json:"l"`
				C float64   `json:"c"`
				V float64   `json:"v"`
			} `json:"bars"`
			NextPageToken string `json:"next_page_token"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("alpaca: decode historical option bars: %w", err)
		}

		for symStr, rawBars := range page.Bars {
			sym := domain.Symbol(symStr)
			tf := domain.Timeframe("1m")
			for _, b := range rawBars {
				bar, err := domain.NewMarketBar(b.T, sym, tf, b.O, b.H, b.L, b.C, b.V)
				if err != nil {
					continue
				}
				result[sym] = append(result[sym], bar)
			}
		}

		if page.NextPageToken == "" {
			break
		}
		nextToken = page.NextPageToken
	}

	return result, nil
}

// parseOCCSymbol parses an OCC option ticker into an OptionContract.
// OCC format: {UNDERLYING (1-6 chars)}{YYMMDD}{C|P}{8-digit strike * 1000}
// Example: AAPL240119C00190000
func parseOCCSymbol(occ string) (domain.OptionContract, error) {
	if len(occ) < 15 {
		return domain.OptionContract{}, fmt.Errorf("OCC symbol too short: %q", occ)
	}

	// Last 15 chars = 6 (date) + 1 (right) + 8 (strike)
	suffix := occ[len(occ)-15:]
	underlying := occ[:len(occ)-15]
	if underlying == "" {
		return domain.OptionContract{}, fmt.Errorf("OCC symbol missing underlying: %q", occ)
	}

	dateStr := suffix[:6]   // YYMMDD
	rightChar := suffix[6]  // C or P
	strikeStr := suffix[7:] // 8 digits

	expiry, err := time.Parse("060102", dateStr)
	if err != nil {
		return domain.OptionContract{}, fmt.Errorf("OCC parse expiry %q: %w", dateStr, err)
	}
	// time.Parse with 2-digit year assumes 2000s
	expiry = time.Date(expiry.Year(), expiry.Month(), expiry.Day(), 0, 0, 0, 0, time.UTC)

	var right domain.OptionRight
	switch rightChar {
	case 'C':
		right = domain.OptionRightCall
	case 'P':
		right = domain.OptionRightPut
	default:
		return domain.OptionContract{}, fmt.Errorf("OCC unknown right char: %q", rightChar)
	}

	var strikeMillis int
	_, err = fmt.Sscanf(strikeStr, "%d", &strikeMillis)
	if err != nil {
		return domain.OptionContract{}, fmt.Errorf("OCC parse strike %q: %w", strikeStr, err)
	}
	strike := float64(strikeMillis) / 1000.0

	return domain.OptionContract{
		ContractSymbol: domain.Symbol(occ),
		Underlying:     domain.Symbol(underlying),
		Expiry:         expiry,
		Strike:         strike,
		Right:          right,
		Style:          domain.OptionStyleAmerican,
		Multiplier:     100,
	}, nil
}

// ListOptionContractsAsOf enumerates OCC option contracts whose expiration
// falls in [asOf, asOf + dteRangeDays] for the given underlying. Queries
// both status=active (currently-listed) and status=inactive (expired) and
// merges by OCC symbol — Alpaca's API tags each contract by its CURRENT
// status, not its status as of the query's date filter, so for a past-dated
// asOf the expiries that are now expired come back only via status=inactive.
// Empty status (no filter) returns nothing on this endpoint.
//
// Pagination: each side follows next_page_token until exhausted.
func (c *RESTClient) ListOptionContractsAsOf(
	ctx context.Context,
	underlying domain.Symbol,
	asOf time.Time,
	dteRangeDays int,
) ([]domain.OptionContract, error) {
	if underlying == "" {
		return nil, fmt.Errorf("underlying symbol must not be empty")
	}
	if dteRangeDays < 0 {
		return nil, fmt.Errorf("dteRangeDays must be non-negative, got %d", dteRangeDays)
	}

	fromStr := asOf.Format("2006-01-02")
	toStr := asOf.AddDate(0, 0, dteRangeDays).Format("2006-01-02")

	seen := make(map[string]struct{}, 512)
	out := make([]domain.OptionContract, 0, 512)

	for _, status := range []string{"active", "inactive"} {
		pageToken := ""
		for {
			path := fmt.Sprintf(
				"/v2/options/contracts?underlying_symbols=%s&expiration_date_gte=%s&expiration_date_lte=%s&status=%s&limit=1000",
				underlying.String(), fromStr, toStr, status,
			)
			if pageToken != "" {
				path += "&page_token=" + pageToken
			}

			resp, err := c.doReqWithOpts(ctx, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 2})
			if err != nil {
				return nil, fmt.Errorf("alpaca: list option contracts asOf %s status=%s: %w", fromStr, status, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, fmt.Errorf("alpaca: list option contracts asOf %s status=%s failed (status %d): %s",
					fromStr, status, resp.StatusCode, string(body))
			}

			var page alpacaOptionsContractListResponse
			if err := json.NewDecoder(bytes.NewReader(body)).Decode(&page); err != nil {
				return nil, fmt.Errorf("alpaca: decode option contracts asOf %s status=%s: %w", fromStr, status, err)
			}

			for _, item := range page.OptionContracts {
				if _, dup := seen[item.Symbol]; dup {
					continue
				}
				contract, err := parseOCCSymbol(item.Symbol)
				if err != nil {
					continue
				}
				seen[item.Symbol] = struct{}{}
				out = append(out, contract)
			}

			if page.NextPageToken == nil || *page.NextPageToken == "" {
				break
			}
			pageToken = *page.NextPageToken
		}
	}
	return out, nil
}

// GetOptionDayBars fetches 1-day bars for a batch of OCC contracts on the
// given date. Returns a map keyed by OCC symbol; missing keys mean "no bar
// published" and callers must treat that as "skip the row" rather than as
// a zero-valued bar (skip-don't-default).
//
// Chunks the input list into batches of 100 OCCs per request, matching
// the Alpaca data API's per-call cap. With 100 OCCs/call, a typical
// 600-strike (sym, date) collapses from 600 round-trips to 6.
func (c *RESTClient) GetOptionDayBars(
	ctx context.Context,
	dataURL string,
	occSymbols []domain.Symbol,
	date time.Time,
) (map[domain.Symbol]*domain.MarketBar, error) {
	if len(occSymbols) == 0 {
		return map[domain.Symbol]*domain.MarketBar{}, nil
	}
	day := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	end := day.AddDate(0, 0, 1)
	startStr := day.Format(time.RFC3339)
	endStr := end.Format(time.RFC3339)

	const batchSize = 100
	out := make(map[domain.Symbol]*domain.MarketBar, len(occSymbols))

	for i := 0; i < len(occSymbols); i += batchSize {
		j := i + batchSize
		if j > len(occSymbols) {
			j = len(occSymbols)
		}
		batch := occSymbols[i:j]
		batchStrs := make([]string, len(batch))
		for k, s := range batch {
			if s == "" {
				return nil, fmt.Errorf("OCC symbol at index %d must not be empty", i+k)
			}
			batchStrs[k] = s.String()
		}

		path := fmt.Sprintf(
			"/v1beta1/options/bars?symbols=%s&timeframe=1Day&start=%s&end=%s&limit=10000",
			strings.Join(batchStrs, ","),
			startStr,
			endStr,
		)
		resp, err := c.doReqDataAPI(ctx, dataURL, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 2})
		if err != nil {
			return nil, fmt.Errorf("alpaca: get option day bars (batch %d-%d): %w", i, j-1, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("alpaca: get option day bars (batch %d-%d) failed (status %d): %s",
				i, j-1, resp.StatusCode, string(body))
		}

		var page struct {
			Bars map[string][]struct {
				T time.Time `json:"t"`
				O float64   `json:"o"`
				H float64   `json:"h"`
				L float64   `json:"l"`
				C float64   `json:"c"`
				V float64   `json:"v"`
			} `json:"bars"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("alpaca: decode option day bars (batch %d-%d): %w", i, j-1, err)
		}
		for symStr, rawBars := range page.Bars {
			if len(rawBars) == 0 {
				continue
			}
			b := rawBars[0]
			bar, err := domain.NewMarketBar(b.T, domain.Symbol(symStr), domain.Timeframe("1d"), b.O, b.H, b.L, b.C, b.V)
			if err != nil {
				continue
			}
			out[domain.Symbol(symStr)] = &bar
		}
	}
	return out, nil
}

// GetOptionDayBar fetches a single 1-day bar. Returns nil when no bar is
// published — callers treat that as "skip the row" rather than an error.
// Thin wrapper over GetOptionDayBars; preserved so single-OCC callers
// don't have to construct a one-element slice.
func (c *RESTClient) GetOptionDayBar(
	ctx context.Context,
	dataURL string,
	occSymbol domain.Symbol,
	date time.Time,
) (*domain.MarketBar, error) {
	if occSymbol == "" {
		return nil, fmt.Errorf("OCC symbol must not be empty")
	}
	bars, err := c.GetOptionDayBars(ctx, dataURL, []domain.Symbol{occSymbol}, date)
	if err != nil {
		return nil, err
	}
	return bars[occSymbol], nil
}
