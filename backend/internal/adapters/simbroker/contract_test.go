package simbroker_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports/brokerporttest"
	"github.com/rs/zerolog"
)

// TestBrokerPortContract_SimBroker runs the shared BrokerPort contract
// suite against SimBroker. First adapter wired into the harness; IBKR
// (via the existing mockIB) and Hyperliquid (via the existing httptest
// fixture) follow in PR 2.
//
// Adapter-specific Setup primes UpdatePrice for each test symbol.
// SimBroker's SubmitOrder consults the last-trade price to compute the
// fill; without UpdatePrice the order would fail at the price-lookup
// before reaching the harness's "non-empty orderID" assertion.
func TestBrokerPortContract_SimBroker(t *testing.T) {
	log := zerolog.Nop()
	broker := simbroker.New(simbroker.Config{SlippageBPS: 5}, log)

	syms := []domain.Symbol{"AAPL", "MSFT", "BTC/USD"}
	prices := map[domain.Symbol]float64{
		"AAPL":    150.0,
		"MSFT":    300.0,
		"BTC/USD": 50000.0,
	}

	env := &brokerporttest.Env{
		TestSymbols:  syms,
		InitialPrice: prices,
		TestTenantID: "tenant-1",
		TestEnvMode:  domain.EnvModePaper,
		Setup: func(_ *testing.T) error {
			now := time.Unix(1700000000, 0).UTC()
			for sym, price := range prices {
				broker.UpdatePrice(sym, price, now)
			}
			return nil
		},
	}

	brokerporttest.RunBrokerPortContract(t, broker, broker, env)
}
