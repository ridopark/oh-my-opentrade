package backtest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/adapters/simbroker"
	backtest "github.com/oh-my-opentrade/backend/internal/app/backtest"
	"github.com/oh-my-opentrade/backend/internal/app/copytradereplay"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExpireOptions_BusBridge_EmitsSellFills_EndToEnd validates the wiring
// chain ExpireOptions → OnExpiryFill callback → memory.Bus.PublishDirect →
// Collector + Ledger. Standing up a full backtest Runner (with DB, strategies,
// streams) is out of reach for a unit test; this exercises the
// boundary the bootstrap closure spans, using the same payload schema the
// production callback constructs.
//
// Seeds three OCC contracts that all expire on the same session date so a
// single per-bar ExpireOptions sweep flushes the book. Asserts:
//   - Bus subscribers receive three FillReceived events tagged
//     exit_reason=OPTION_EXPIRY.
//   - Ledger captures each row (note: ledger doesn't yet propagate
//     exit_reason to CSV; see the deliverable report).
//   - Collector classifies each as a CLOSE_LONG round-trip exit.
func TestExpireOptions_BusBridge_EmitsSellFills_EndToEnd(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	const (
		callSym = "INTC260320C00043000"
		putSym1 = "NVDA260213P00185000"
		putSym2 = "TSLA260213P00405000"
	)

	expiry := time.Date(2026, 4, 17, 16, 0, 0, 0, loc) // synthetic shared expiry close

	bus := memory.NewBus()

	// Capture every FillReceived for direct assertions.
	var (
		fillsMu sync.Mutex
		fills   []map[string]any
	)
	require.NoError(t, bus.Subscribe(context.Background(), domain.EventFillReceived, func(_ context.Context, ev domain.Event) error {
		p, ok := ev.Payload.(map[string]any)
		if !ok {
			return nil
		}
		fillsMu.Lock()
		fills = append(fills, p)
		fillsMu.Unlock()
		return nil
	}))

	// Ledger writes to a temp file; we only assert that rows were written.
	tmpDir := t.TempDir()
	ledgerPath := filepath.Join(tmpDir, "fills.csv")
	ledger, err := copytradereplay.NewLedger(ledgerPath, zerolog.Nop())
	require.NoError(t, err)
	require.NoError(t, ledger.Subscribe(context.Background(), bus))

	// Collector with sane defaults so round-trip accounting can fire.
	collector, err := backtest.NewCollector(bus, backtest.Config{InitialEquity: 100_000}, zerolog.Nop())
	require.NoError(t, err)
	require.NotNil(t, collector)

	// Bridge SimBroker -> bus via the same closure shape used by
	// bootstrap.BuildBacktestInfra.
	sim := simbroker.New(simbroker.Config{
		SlippageBPS:     5,
		DisableFillChan: true,
		OnExpiryFill: func(payload map[string]any) {
			symStr, _ := payload["symbol"].(string)
			filledAt, _ := payload["filled_at"].(time.Time)
			idem := fmt.Sprintf("option-expiry:%s:%d", symStr, filledAt.UnixNano())
			evt, evtErr := domain.NewEvent(domain.EventFillReceived, "default", domain.EnvModePaper, idem, payload)
			require.NoError(t, evtErr)
			require.NoError(t, bus.Publish(context.Background(), *evt))
		},
	}, zerolog.Nop())

	// Pre-seed: publish three opening BUY fills so the collector knows about
	// the positions; then directly install the same positions on the broker.
	// (SubmitOrder would re-enter the broker price path; pre-seeding is the
	// minimal setup that exercises the expiry sweep.)
	openTime := expiry.Add(-2 * time.Hour)
	openings := []struct {
		sym      string
		entryPx  float64
		qty      float64
		strategy string
	}{
		{callSym, 2.50, 1, "copytrade_v1"},
		{putSym1, 3.00, 1, "copytrade_v1"},
		{putSym2, 4.00, 1, "copytrade_v1"},
	}
	for i, o := range openings {
		payload := map[string]any{
			"symbol":          o.sym,
			"strategy":        o.strategy,
			"side":            "BUY",
			"direction":       string(domain.DirectionLong),
			"quantity":        o.qty,
			"price":           o.entryPx,
			"filled_at":       openTime,
			"instrument_type": "OPTION",
			"option_right":    occRight(o.sym),
			"option_expiry":   "2026-04-17",
			"broker_order_id": fmt.Sprintf("sim-open-%d", i),
		}
		evt, evtErr := domain.NewEvent(domain.EventFillReceived, "default", domain.EnvModePaper, fmt.Sprintf("open-%d", i), payload)
		require.NoError(t, evtErr)
		require.NoError(t, bus.Publish(context.Background(), *evt))
	}

	// Seed broker positions and underlying prices using the public surface.
	// SubmitOrder would need full quote/fill plumbing; injecting via the
	// recorded payloads + UpdatePrice + helper SubmitOrder on the real path
	// is unnecessary here — the public ExpireOptions contract only needs
	// positions and prices to be present.
	for _, o := range openings {
		intent := domain.OrderIntent{
			Symbol:    domain.Symbol(o.sym),
			Direction: domain.DirectionLong,
			Quantity:  o.qty,
			LimitPrice: o.entryPx,
			OrderType: "limit",
			Strategy:  o.strategy,
			Instrument: &domain.Instrument{
				Type:             domain.InstrumentTypeOption,
				Symbol:           domain.Symbol(o.sym),
				UnderlyingSymbol: domain.UnderlyingFromOCC(domain.Symbol(o.sym)),
			},
			Meta: map[string]string{},
		}
		// Seed underlying first so the option SubmitOrder path succeeds.
		sim.UpdatePrice(domain.UnderlyingFromOCC(domain.Symbol(o.sym)), 100.0, openTime)
		_, sErr := sim.SubmitOrder(context.Background(), intent)
		require.NoError(t, sErr, "seeding broker position for %s", o.sym)
	}

	// Set expiry-time underlying prices that put INTC call ITM and the puts OTM.
	sim.UpdatePrice(domain.UnderlyingFromOCC(domain.Symbol(callSym)), 50.0, expiry)  // ITM CALL: 50-43=7
	sim.UpdatePrice(domain.UnderlyingFromOCC(domain.Symbol(putSym1)), 200.0, expiry) // OTM PUT: 185-200<0 -> 0
	sim.UpdatePrice(domain.UnderlyingFromOCC(domain.Symbol(putSym2)), 420.0, expiry) // OTM PUT: 405-420<0 -> 0

	// Snapshot fills captured BEFORE the sweep so we only assert on what the
	// sweep emits.
	fillsMu.Lock()
	preSweep := len(fills)
	fillsMu.Unlock()

	// Run the sweep. ExpireOptions reads symbols' actual OCC expiries from
	// ParseOCC, so we time-travel barTime past every contract's session close.
	// Each contract expires on its own real date (2026-03-20 / 2026-02-13);
	// barTime here is well past all of them.
	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	sim.ExpireOptions(context.Background(), barTime)

	fillsMu.Lock()
	sweepFills := fills[preSweep:]
	fillsMu.Unlock()

	require.Len(t, sweepFills, 3, "ExpireOptions must emit one fill per non-expired option position")

	bySym := map[string]map[string]any{}
	for _, p := range sweepFills {
		bySym[p["symbol"].(string)] = p
		assert.Equal(t, "OPTION_EXPIRY", p["exit_reason"])
		assert.Equal(t, "SELL", p["side"])
		assert.Equal(t, string(domain.DirectionCloseLong), p["direction"])
		assert.Equal(t, "copytrade_v1", p["strategy"])
	}

	require.Contains(t, bySym, callSym)
	require.Contains(t, bySym, putSym1)
	require.Contains(t, bySym, putSym2)
	assert.InDelta(t, 7.0, bySym[callSym]["price"].(float64), 1e-9, "ITM call intrinsic")
	assert.Equal(t, 0.0, bySym[putSym1]["price"].(float64), "OTM put NVDA intrinsic")
	assert.Equal(t, 0.0, bySym[putSym2]["price"].(float64), "OTM put TSLA intrinsic")

	// Ledger wrote rows.
	require.NoError(t, ledger.Close())
	ledgerBytes, err := os.ReadFile(ledgerPath)
	require.NoError(t, err)
	ledgerLines := strings.Split(strings.TrimSpace(string(ledgerBytes)), "\n")
	// 1 header + 3 opens + 3 expiry closes = 7 lines
	require.GreaterOrEqual(t, len(ledgerLines), 7, "ledger must have header + opens + expiry closes")

	// Collector saw three round-trip trades.
	result := collector.Result()
	assert.Equal(t, 3, result.TradeCount, "three closed round trips after expiry sweep")
	assert.Equal(t, int64(0), sim.OptionsExpiredMissingUnderlying(), "all three underlyings were priced")
}

// occRight returns "C" or "P" from an OCC contract symbol.
func occRight(sym string) string {
	_, _, right, _, _ := domain.ParseOCC(domain.Symbol(sym))
	if right == "PUT" {
		return "P"
	}
	return "C"
}
