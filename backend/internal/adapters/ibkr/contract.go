package ibkr

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/scmhub/ibsync"
)

const cryptoExchange = "ZEROHASH"

func newContract(symbol domain.Symbol) *ibsync.Contract {
	if symbol.IsCryptoSymbol() {
		base := strings.SplitN(string(symbol), "/", 2)[0]
		return ibsync.NewCrypto(base, cryptoExchange, "USD")
	}
	if domain.IsOCCSymbol(symbol) {
		return newOptionContract(symbol)
	}
	return ibsync.NewStock(string(symbol), "SMART", "USD")
}

// newOptionContract parses an OCC symbol (e.g. AFRM260515P00052500) into an
// ibsync Option contract for IBKR market data queries.
func newOptionContract(symbol domain.Symbol) *ibsync.Contract {
	s := string(symbol)
	// OCC format: {UNDERLYING}{YYMMDD}{C|P}{8-digit strike*1000}
	suffix := s[len(s)-15:]
	underlying := s[:len(s)-15]
	expiry := "20" + suffix[:6] // YYYYMMDD
	right := "C"
	if suffix[6] == 'P' {
		right = "P"
	}
	strikeInt, _ := strconv.ParseFloat(suffix[7:], 64)
	strike := strikeInt / 1000.0

	return ibsync.NewOption(
		underlying,
		expiry,
		strike,
		right,
		"SMART",
		fmt.Sprintf("%d", 100),
		"USD",
	)
}
