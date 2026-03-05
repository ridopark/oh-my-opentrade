package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
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

// alpacaOptionsSnapshotResponse is the raw Alpaca API response for option snapshots.
type alpacaOptionsSnapshotResponse struct {
	Snapshots map[string]alpacaOptionSnapshot `json:"snapshots"`
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
		BP float64 `json:"bp"` // bid price
		AP float64 `json:"ap"` // ask price
		C  float64 `json:"c"`  // last price
	} `json:"latestQuote"`
	OpenInterest int `json:"openInterest"`
}

// GetOptionChain retrieves option contract snapshots from Alpaca for the given
// underlying symbol, expiry date, and option right (call/put).
func (c *RESTClient) GetOptionChain(
	ctx context.Context,
	underlying domain.Symbol,
	expiry time.Time,
	right domain.OptionRight,
) ([]domain.OptionContractSnapshot, error) {
	if underlying == "" {
		return nil, fmt.Errorf("underlying symbol must not be empty")
	}

	rightStr := strings.ToLower(string(right)) // "call" or "put"
	expiryStr := expiry.Format("2006-01-02")   // YYYY-MM-DD

	path := fmt.Sprintf(
		"/v2/options/contracts?underlying_symbols=%s&expiration_date=%s&type=%s&feed=indicative",
		underlying.String(), expiryStr, rightStr,
	)

	resp, err := c.doReqWithOpts(ctx, http.MethodGet, path, nil, reqOpts{priority: PriorityBackground, maxRetries: 1})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alpaca: get option chain failed (status %d): %s", resp.StatusCode, string(body))
	}

	var raw alpacaOptionsSnapshotResponse
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("alpaca: decode option chain response: %w", err)
	}

	snapshots := make([]domain.OptionContractSnapshot, 0, len(raw.Snapshots))
	for contractSym, snap := range raw.Snapshots {
		// Parse the OCC symbol to extract contract details
		contract, err := parseOCCSymbol(contractSym)
		if err != nil {
			// Skip malformed symbols
			continue
		}

		greeks, err := domain.NewGreeks(snap.Greeks.Delta, snap.Greeks.Gamma, snap.Greeks.Theta, snap.Greeks.Vega, snap.Greeks.Rho, snap.ImpliedVolatility)
		if err != nil {
			// Use zero greeks if validation fails
			greeks = domain.Greeks{}
		}

		snapshot := domain.OptionContractSnapshot{
			OptionContract: contract,
			OptionQuote: domain.OptionQuote{
				Bid:       snap.LatestQuote.BP,
				Ask:       snap.LatestQuote.AP,
				Last:      snap.LatestQuote.C,
				Timestamp: time.Now(),
			},
			Greeks:       greeks,
			OpenInterest: snap.OpenInterest,
		}
		snapshots = append(snapshots, snapshot)
	}

	return snapshots, nil
}

// parseOCCSymbol parses an OCC option ticker into an OptionContract.
// OCC format: {UNDERLYING (1-6 chars)}{YYMMDD}{C|P}{8-digit strike * 1000}
// Example: AAPL240119C00190000
func parseOCCSymbol(occ string) (domain.OptionContract, error) {
	if len(occ) < 15 {
		return domain.OptionContract{}, fmt.Errorf("OCC symbol too short: %q", occ)
	}

	// Find right char (C or P) position by scanning from position 6 backwards from end
	// The date is always 6 digits, right char 1 char, strike 8 chars = 15 chars from end
	if len(occ) < 15 {
		return domain.OptionContract{}, fmt.Errorf("OCC symbol malformed: %q", occ)
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
