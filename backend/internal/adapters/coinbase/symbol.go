package coinbase

import (
	"fmt"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// toCoinbaseProduct converts a domain.Symbol to the Coinbase product id.
// The domain uses slash-delimited USD pairs (e.g. "BTC/USD"); Coinbase uses a
// dash (e.g. "BTC-USD"). Equity-style tickers (no "/") or empty symbols are
// rejected so callers do not silently hit a 404 on the candles endpoint.
func toCoinbaseProduct(sym domain.Symbol) (string, error) {
	s := strings.TrimSpace(sym.String())
	if s == "" {
		return "", fmt.Errorf("coinbase: symbol is empty")
	}
	if !strings.Contains(s, "/") {
		return "", fmt.Errorf("coinbase: symbol %q is not a crypto pair (expected e.g. BTC/USD)", s)
	}
	// Coinbase products are uppercase (BTC-USD); domain symbols are already
	// uppercase but normalise defensively in case callers pass lowercased
	// strings from TOML configs or user input.
	return strings.ToUpper(strings.Replace(s, "/", "-", 1)), nil
}
