package ibkr

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports/brokerporttest"
	"github.com/rs/zerolog"
)

// TestBrokerPortContract_IBKR runs the shared BrokerPort contract suite
// against the IBKR adapter via the existing in-package mockIB fixture.
// Internal test package (`package ibkr`) is required to access mockIB
// without exporting it; the brokerporttest helper itself doesn't
// depend on ibkr, so there's no circular import.
//
// mockIB defaults to empty trades / openTrades / positions when
// constructed with `&mockIB{connected: true}`, so the harness's
// "GetPositions on fresh adapter is empty" invariant holds without
// SkipFreshPositionsCheck.
func TestBrokerPortContract_IBKR(t *testing.T) {
	mock := &mockIB{connected: true}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	syms := []domain.Symbol{"AAPL", "NFLX"}
	prices := map[domain.Symbol]float64{
		"AAPL": 150.0,
		"NFLX": 450.0,
	}

	env := &brokerporttest.Env{
		TestSymbols:  syms,
		InitialPrice: prices,
		TestTenantID: "tenant-1",
		TestEnvMode:  domain.EnvModePaper,
	}

	brokerporttest.RunBrokerPortContract(t, a, nil, env)
}
