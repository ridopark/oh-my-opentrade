package coinbase

import (
	"fmt"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// toCoinbaseGranularity maps a domain.Timeframe to the granularity (in
// seconds) required by the Coinbase /candles endpoint. Coinbase only accepts
// the fixed set {60, 300, 900, 3600, 21600, 86400}; any other timeframe
// (including 30m, which is a legal domain timeframe but not a supported
// Coinbase bucket) returns an error so the caller can skip the product.
func toCoinbaseGranularity(tf domain.Timeframe) (int, error) {
	switch tf {
	case "1m":
		return 60, nil
	case "5m":
		return 300, nil
	case "15m":
		return 900, nil
	case "1h":
		return 3600, nil
	case "6h":
		return 21600, nil
	case "1d":
		return 86400, nil
	default:
		return 0, fmt.Errorf("coinbase: unsupported timeframe %q", tf.String())
	}
}
