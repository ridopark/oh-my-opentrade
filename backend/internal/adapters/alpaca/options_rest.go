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
		// C is a trade condition code string ("A", "I", etc.) — not a price; omitted.
	} `json:"latestQuote"`
	LatestTrade struct {
		P float64 `json:"p"` // last trade price
	} `json:"latestTrade"`
	OpenInterest int `json:"openInterest"`
}

// GetOptionChain retrieves option contract snapshots with greeks and quotes for the given
// underlying symbol, expiry date, and option right (call/put).
//
// Two-step process:
//  1. Fetch OCC contract symbols from the broker API (/v2/options/contracts).
//  2. Fetch live snapshots (greeks, bid/ask, IV) from the data API
//     (/v1beta1/options/snapshots).
func (c *RESTClient) GetOptionChain(
	ctx context.Context,
	dataURL string,
	underlying domain.Symbol,
	expiry time.Time,
	right domain.OptionRight,
) ([]domain.OptionContractSnapshot, error) {
	if underlying == "" {
		return nil, fmt.Errorf("underlying symbol must not be empty")
	}

	rightStr := strings.ToLower(string(right))

	// Use a ±7 day window around the target expiry to find the nearest listed
	// expiry. Options expire on Fridays and monthlies; an exact date match is
	// rarely available, so we widen the search and let the caller's DTE filter
	// (in ContractSelectionService) narrow it down.
	expiryFrom := expiry.AddDate(0, 0, -7).Format("2006-01-02")
	expiryTo := expiry.AddDate(0, 0, 7).Format("2006-01-02")

	contractsPath := fmt.Sprintf(
		"/v2/options/contracts?underlying_symbols=%s&expiration_date_gte=%s&expiration_date_lte=%s&type=%s&limit=250",
		underlying.String(), expiryFrom, expiryTo, rightStr,
	)

	contractsResp, err := c.doReqWithOpts(ctx, http.MethodGet, contractsPath, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
	if err != nil {
		return nil, fmt.Errorf("alpaca: list option contracts: %w", err)
	}
	defer contractsResp.Body.Close()

	contractsBody, _ := io.ReadAll(contractsResp.Body)
	if contractsResp.StatusCode < 200 || contractsResp.StatusCode >= 300 {
		return nil, fmt.Errorf("alpaca: list option contracts failed (status %d): %s", contractsResp.StatusCode, string(contractsBody))
	}

	var contractList alpacaOptionsContractListResponse
	if err := json.NewDecoder(bytes.NewReader(contractsBody)).Decode(&contractList); err != nil {
		return nil, fmt.Errorf("alpaca: decode option contracts list: %w", err)
	}

	if len(contractList.OptionContracts) == 0 {
		return nil, nil
	}

	// Collect tradable OCC symbols.
	occSymbols := make([]string, 0, len(contractList.OptionContracts))
	for _, c := range contractList.OptionContracts {
		if c.Tradable && c.Status == "active" {
			occSymbols = append(occSymbols, c.Symbol)
		}
	}
	if len(occSymbols) == 0 {
		return nil, nil
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

		snapResp, err := c.doReqDataAPI(ctx, dataURL, http.MethodGet, snapshotPath, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
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
	snapshots := make([]domain.OptionContractSnapshot, 0, len(allSnapshots))
	for _, item := range contractList.OptionContracts {
		if !item.Tradable || item.Status != "active" {
			continue
		}
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
		resp, err := c.doReqDataAPI(ctx, dataURL, http.MethodGet, snapshotPath, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
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
