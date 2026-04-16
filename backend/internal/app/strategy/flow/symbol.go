package flow

import "strings"

// NormalizeBinanceSymbol converts Binance-style symbols (e.g. "BTCUSDT")
// to the canonical "BTC/USD" format used internally.
func NormalizeBinanceSymbol(s string) string {
	s = strings.ToUpper(s)
	// Binance uses USDT pairs; strip trailing "USDT" or "USD".
	for _, suffix := range []string{"USDT", "BUSD", "USD"} {
		if strings.HasSuffix(s, suffix) {
			base := s[:len(s)-len(suffix)]
			return base + "/USD"
		}
	}
	return s
}

// NormalizeCoinbaseSymbol converts Coinbase-style symbols (e.g. "BTC-USD")
// to the canonical "BTC/USD" format.
func NormalizeCoinbaseSymbol(s string) string {
	return strings.ReplaceAll(strings.ToUpper(s), "-", "/")
}
