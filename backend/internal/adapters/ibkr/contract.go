package ibkr

import (
	"strings"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/scmhub/ibsync"
)

const cryptoExchange = "PAXOS"

func newContract(symbol domain.Symbol) *ibsync.Contract {
	if symbol.IsCryptoSymbol() {
		base := strings.SplitN(string(symbol), "/", 2)[0]
		return ibsync.NewCrypto(base, cryptoExchange, "USD")
	}
	return ibsync.NewStock(string(symbol), "SMART", "USD")
}
