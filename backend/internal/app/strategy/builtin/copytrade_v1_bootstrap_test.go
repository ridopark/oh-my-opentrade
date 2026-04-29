package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rightEnum(s string) domain.OptionRight {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P", "PUT":
		return domain.OptionRightPut
	default:
		return domain.OptionRightCall
	}
}

func rightShort(r domain.OptionRight) string {
	if r == domain.OptionRightPut {
		return "PUT"
	}
	return "CALL"
}

func btoEvent(t *testing.T, ts time.Time, author, ticker, expiry string, strike float64, right, refPrice string) domain.StrategySignalEvent {
	t.Helper()
	expiryDate, err := time.Parse("2006-01-02", expiry)
	require.NoError(t, err)
	re := rightEnum(right)
	contract := domain.FormatOCCSymbol(ticker, expiryDate, re, strike)
	tags := map[string]string{
		"author":           author,
		"contract_symbol":  contract,
		"copytrade_action": "BTO",
		"force_expiry":     expiry,
		"force_strike":     strconv.FormatFloat(strike, 'f', -1, 64),
		"force_right":      rightShort(re),
		"generation":       "1",
		"ref_price":        refPrice,
		"signal_id":        "bto-" + author + "-" + ticker + "-" + expiry,
	}
	sig := struct {
		Symbol string            `json:"Symbol"`
		Tags   map[string]string `json:"Tags"`
	}{Symbol: ticker, Tags: tags}
	payload, err := json.Marshal(sig)
	require.NoError(t, err)
	return domain.StrategySignalEvent{
		TS:       ts,
		TenantID: "default",
		EnvMode:  domain.EnvModePaper,
		Strategy: "copytrade_v1",
		SignalID: tags["signal_id"],
		Symbol:   ticker,
		Kind:     "entry",
		Side:     "BUY",
		Status:   domain.SignalStatusGenerated,
		Reason:   author + ": " + "BTO " + ticker,
		Payload:  payload,
	}
}

func stcEvent(t *testing.T, ts time.Time, author, ticker, expiry string, strike float64, right, tail string) domain.StrategySignalEvent {
	t.Helper()
	expiryDate, err := time.Parse("2006-01-02", expiry)
	require.NoError(t, err)
	contract := domain.FormatOCCSymbol(ticker, expiryDate, rightEnum(right), strike)
	rationale := domain.ComposeAuthorText(author, "STC "+ticker+" "+tail)
	return domain.StrategySignalEvent{
		TS:       ts,
		TenantID: "default",
		EnvMode:  domain.EnvModePaper,
		Strategy: "copytrade_v1",
		SignalID: "stc-" + author + "-" + ticker + "-" + ts.Format("150405"),
		Symbol:   contract,
		Kind:     "exit",
		Side:     "SELL",
		Status:   domain.SignalStatusValidated,
		Reason:   rationale,
		Payload:  json.RawMessage(`{}`),
	}
}

// occTrade fabricates a broker position trade for a given option (OCC symbol
// and quantity). Matches the shape ports.BrokerPort.GetPositions returns.
func occTrade(ticker, expiry string, strike float64, right string, qty float64) domain.Trade {
	expiryDate, _ := time.Parse("2006-01-02", expiry)
	re := rightEnum(right)
	contract := domain.FormatOCCSymbol(ticker, expiryDate, re, strike)
	return domain.Trade{
		Symbol:   domain.Symbol(contract),
		Side:     "BUY",
		Quantity: qty,
	}
}

func runBootstrap(t *testing.T, events []domain.StrategySignalEvent, positions []domain.Trade) (*CopytradeStrategy, *copytradeState) {
	t.Helper()
	s, st := initCopytrade(t, copytradeDefaultParams())
	deps := CopytradeBootstrapDeps{
		TenantID: "default",
		EnvMode:  domain.EnvModePaper,
		Now:      time.Date(2026, 4, 24, 14, 0, 0, 0, time.UTC),
		SignalEvents: func(_ context.Context, _ string, _, _ time.Time) ([]domain.StrategySignalEvent, error) {
			return events, nil
		},
		Positions: func(_ context.Context) ([]domain.Trade, error) {
			return positions, nil
		},
	}
	out, err := s.Bootstrap(context.Background(), st, deps)
	require.NoError(t, err)
	return s, out.(*copytradeState)
}

func TestCopytradeBootstrap_BTOOnly_BrokerConfirms(t *testing.T) {
	ts := time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC)
	events := []domain.StrategySignalEvent{
		btoEvent(t, ts, "alice", "AAPL", "2026-04-25", 190, "CALL", "1.25"),
	}
	positions := []domain.Trade{occTrade("AAPL", "2026-04-25", 190, "CALL", 3)}

	_, cst := runBootstrap(t, events, positions)

	require.Len(t, cst.Positions, 1)
	for _, pos := range cst.Positions {
		assert.False(t, pos.Pending, "broker confirmed position must not be Pending")
		assert.Equal(t, 1.0, pos.RemainingFrac)
		assert.False(t, pos.TrailArmed)
		assert.Equal(t, 1, pos.Generation)
		assert.InDelta(t, 1.25, pos.EntryPremium, 1e-9)
	}
	expiry, _ := time.Parse("2006-01-02", "2026-04-25")
	base := positionBase("alice", "AAPL", expiry, 190, "CALL")
	assert.Equal(t, 1, cst.Generations[base])
}

func TestCopytradeBootstrap_BTOOnly_BrokerMissing_DropsAndRollsBackGeneration(t *testing.T) {
	ts := time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC)
	events := []domain.StrategySignalEvent{
		btoEvent(t, ts, "alice", "AAPL", "2026-04-25", 190, "CALL", "1.25"),
	}
	// No broker positions -> phantom should be dropped, Generations cleared.
	_, cst := runBootstrap(t, events, nil)

	assert.Empty(t, cst.Positions, "phantom BTO must be dropped when broker has no matching contract")
	expiry, _ := time.Parse("2026-04-25", "2026-04-25")
	_ = expiry
	base := positionBase("alice", "AAPL", mustParseDate("2026-04-25"), 190, "CALL")
	assert.Zero(t, cst.Generations[base], "Generations must not retain a phantom count after broker-confirm drop")
}

func TestCopytradeBootstrap_PartialSTC_SetsResidualAndArmsTrail(t *testing.T) {
	expiry := "2026-04-25"
	events := []domain.StrategySignalEvent{
		btoEvent(t, time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC),
			"alice", "AAPL", expiry, 190, "CALL", "1.25"),
		stcEvent(t, time.Date(2026, 4, 23, 15, 0, 0, 0, time.UTC),
			"alice", "AAPL", expiry, 190, "CALL", "half out at 1.80"),
	}
	positions := []domain.Trade{occTrade("AAPL", expiry, 190, "CALL", 2)}

	_, cst := runBootstrap(t, events, positions)

	require.Len(t, cst.Positions, 1)
	for _, pos := range cst.Positions {
		assert.False(t, pos.Pending)
		assert.InDelta(t, 0.5, pos.RemainingFrac, 1e-9, "half-out should leave 0.5 residual")
		assert.True(t, pos.TrailArmed, "first partial must arm trail")
	}
}

func TestCopytradeBootstrap_FullSTC_DeletesPositionAndRetainsGeneration(t *testing.T) {
	expiry := "2026-04-25"
	events := []domain.StrategySignalEvent{
		btoEvent(t, time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC),
			"alice", "AAPL", expiry, 190, "CALL", "1.25"),
		stcEvent(t, time.Date(2026, 4, 23, 15, 0, 0, 0, time.UTC),
			"alice", "AAPL", expiry, 190, "CALL", "all out at 1.90"),
	}
	// No broker position (closed via the STC).
	_, cst := runBootstrap(t, events, nil)

	assert.Empty(t, cst.Positions, "full close must leave no position")
	base := positionBase("alice", "AAPL", mustParseDate(expiry), 190, "CALL")
	assert.Equal(t, 1, cst.Generations[base],
		"full close via STC retains Generations[base] so the next BTO for the same contract increments to gen+1")
}

func TestCopytradeBootstrap_SecondBTO_TracksAsNewGeneration(t *testing.T) {
	expiry := "2026-04-25"
	events := []domain.StrategySignalEvent{
		btoEvent(t, time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC),
			"alice", "AAPL", expiry, 190, "CALL", "1.25"),
		btoEvent(t, time.Date(2026, 4, 23, 14, 45, 0, 0, time.UTC),
			"alice", "AAPL", expiry, 190, "CALL", "1.30"),
	}
	// Broker holds one aggregated position; both gens should survive the
	// broker-confirm pass since the OCC contract is present with qty>0.
	positions := []domain.Trade{occTrade("AAPL", expiry, 190, "CALL", 5)}

	_, cst := runBootstrap(t, events, positions)

	require.Len(t, cst.Positions, 2, "both generations should be tracked")
	base := positionBase("alice", "AAPL", mustParseDate(expiry), 190, "CALL")
	assert.Equal(t, 2, cst.Generations[base])
}

func TestCopytradeBootstrap_IgnoresOtherStrategiesAndBlockedRows(t *testing.T) {
	expiry := "2026-04-25"
	events := []domain.StrategySignalEvent{
		// Non-copytrade row bleeding through the filter (defense-in-depth):
		{
			TS:       time.Date(2026, 4, 23, 14, 0, 0, 0, time.UTC),
			Strategy: "avwap_v4",
			SignalID: "noise",
			Symbol:   "AAPL",
			Kind:     "entry",
			Side:     "BUY",
			Status:   domain.SignalStatusGenerated,
			Payload:  json.RawMessage(`{}`),
		},
		// Blocked gate row for copytrade — must be skipped:
		{
			TS:       time.Date(2026, 4, 23, 14, 5, 0, 0, time.UTC),
			Strategy: "copytrade_v1",
			SignalID: "blocked",
			Symbol:   "AAPL",
			Kind:     "entry",
			Side:     "BUY",
			Status:   domain.SignalStatusBlocked,
			Payload:  json.RawMessage(`{}`),
		},
		btoEvent(t, time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC),
			"alice", "AAPL", expiry, 190, "CALL", "1.25"),
	}
	positions := []domain.Trade{occTrade("AAPL", expiry, 190, "CALL", 3)}

	_, cst := runBootstrap(t, events, positions)

	require.Len(t, cst.Positions, 1, "only the copytrade BTO should seed a position")
}

func TestCopytradeBootstrap_SkippedForBacktestTenant(t *testing.T) {
	s, st := initCopytrade(t, copytradeDefaultParams())
	called := false
	deps := CopytradeBootstrapDeps{
		TenantID: "backtest",
		EnvMode:  domain.EnvModePaper,
		Now:      time.Date(2026, 4, 24, 14, 0, 0, 0, time.UTC),
		SignalEvents: func(_ context.Context, _ string, _, _ time.Time) ([]domain.StrategySignalEvent, error) {
			called = true
			return nil, nil
		},
		Positions: func(_ context.Context) ([]domain.Trade, error) {
			return nil, nil
		},
	}
	_, err := s.Bootstrap(context.Background(), st, deps)
	require.NoError(t, err)
	assert.False(t, called, "backtest tenant must short-circuit before the query")
}

func TestCopytradeBootstrap_BrokerFailure_SkipsCleanly(t *testing.T) {
	ts := time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC)
	events := []domain.StrategySignalEvent{
		btoEvent(t, ts, "alice", "AAPL", "2026-04-25", 190, "CALL", "1.25"),
	}
	s, st := initCopytrade(t, copytradeDefaultParams())
	deps := CopytradeBootstrapDeps{
		TenantID: "default",
		EnvMode:  domain.EnvModePaper,
		Now:      time.Date(2026, 4, 24, 14, 0, 0, 0, time.UTC),
		SignalEvents: func(_ context.Context, _ string, _, _ time.Time) ([]domain.StrategySignalEvent, error) {
			return events, nil
		},
		Positions: func(_ context.Context) ([]domain.Trade, error) {
			return nil, fmt.Errorf("ibkr gateway down")
		},
	}
	out, err := s.Bootstrap(context.Background(), st, deps)
	require.NoError(t, err)
	cst := out.(*copytradeState)
	assert.Empty(t, cst.Positions, "broker failure must yield empty state, not a phantom restore")
	assert.Empty(t, cst.Generations)
}

func TestCopytradeBootstrap_NilPositionsFn_SkipsCleanly(t *testing.T) {
	ts := time.Date(2026, 4, 23, 14, 30, 0, 0, time.UTC)
	events := []domain.StrategySignalEvent{
		btoEvent(t, ts, "alice", "AAPL", "2026-04-25", 190, "CALL", "1.25"),
	}
	s, st := initCopytrade(t, copytradeDefaultParams())
	deps := CopytradeBootstrapDeps{
		TenantID: "default",
		EnvMode:  domain.EnvModePaper,
		SignalEvents: func(_ context.Context, _ string, _, _ time.Time) ([]domain.StrategySignalEvent, error) {
			return events, nil
		},
		// Positions: nil
	}
	out, err := s.Bootstrap(context.Background(), st, deps)
	require.NoError(t, err)
	cst := out.(*copytradeState)
	assert.Empty(t, cst.Positions)
}

func mustParseDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}
