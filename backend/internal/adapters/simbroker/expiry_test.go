package simbroker

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expiryFixture builds a Broker with a captured OnExpiryFill sink so tests
// can assert on every emitted payload without wiring an EventBus.
type expiryFixture struct {
	broker *Broker
	fills  []map[string]any
}

func newExpiryFixture(t *testing.T) *expiryFixture {
	t.Helper()
	f := &expiryFixture{}
	f.broker = New(Config{
		SlippageBPS:     5,
		DisableFillChan: true,
		OnExpiryFill: func(payload map[string]any) {
			f.fills = append(f.fills, payload)
		},
	}, zerolog.Nop())
	return f
}

// seedOptionLong installs a long option position directly into the broker's
// position map, mirroring what SubmitOrder would have produced after a BUY
// open. occSymbol must be a valid OCC contract.
func (f *expiryFixture) seedOptionLong(t *testing.T, occSymbol string, qty, avgCost float64, strategy string) {
	t.Helper()
	sym := domain.Symbol(occSymbol)
	f.broker.positions[positionKey(sym, domain.VenueUnspecified)] = &position{
		symbol:   sym,
		side:     "buy",
		quantity: qty,
		avgCost:  avgCost,
		strategy: strategy,
	}
}

func (f *expiryFixture) setUnderlyingPrice(sym string, price float64, barTime time.Time) {
	f.broker.UpdatePrice(domain.Symbol(sym), price, barTime)
}

// nyET shortcut for ET-local times used in expiry boundary tests.
func nyET(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	return loc
}

func TestExpireOptions_ITMCall_EmitsSellAtIntrinsic(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	// AAPL 100 CALL expiring 2026-04-17 (regular Friday, close 16:00 ET)
	const sym = "AAPL260417C00100000"
	f.seedOptionLong(t, sym, 2, 5.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 110.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	require.Len(t, f.fills, 1)
	p := f.fills[0]
	assert.Equal(t, sym, p["symbol"])
	assert.Equal(t, "SELL", p["side"])
	assert.Equal(t, string(domain.DirectionCloseLong), p["direction"])
	assert.InDelta(t, 10.0, p["price"].(float64), 1e-9)
	assert.InDelta(t, 2.0, p["quantity"].(float64), 1e-9)
	assert.Equal(t, "copytrade_v1", p["strategy"])
	assert.Equal(t, "OPTION_EXPIRY", p["exit_reason"])
	assert.Equal(t, "OPTION", p["instrument_type"])
	assert.Equal(t, "OPTION", p["asset_class"])
	assert.Equal(t, "C", p["option_right"])
	assert.Equal(t, "2026-04-17", p["option_expiry"])
	assert.Equal(t, barTime, p["filled_at"])
	assert.NotEmpty(t, p["broker_order_id"])

	pos := f.broker.positions[positionKey(domain.Symbol(sym), domain.VenueUnspecified)]
	require.NotNil(t, pos)
	assert.Equal(t, 0.0, pos.quantity)
	assert.Equal(t, "", pos.strategy)
}

func TestExpireOptions_OTMCall_EmitsSellAtZero(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "AAPL260417C00100000"
	f.seedOptionLong(t, sym, 1, 3.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 95.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	require.Len(t, f.fills, 1)
	assert.Equal(t, 0.0, f.fills[0]["price"].(float64))
}

func TestExpireOptions_ITMPut_EmitsSellAtIntrinsic(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "AAPL260417P00100000"
	f.seedOptionLong(t, sym, 3, 4.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 90.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	require.Len(t, f.fills, 1)
	assert.InDelta(t, 10.0, f.fills[0]["price"].(float64), 1e-9)
	assert.Equal(t, "P", f.fills[0]["option_right"])
}

func TestExpireOptions_OTMPut_EmitsSellAtZero(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "AAPL260417P00100000"
	f.seedOptionLong(t, sym, 1, 4.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 105.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	require.Len(t, f.fills, 1)
	assert.Equal(t, 0.0, f.fills[0]["price"].(float64))
}

func TestExpireOptions_PreExpiry_NoFill(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "AAPL260417C00100000"
	f.seedOptionLong(t, sym, 1, 5.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 110.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	// Bar one second BEFORE session close on expiry date.
	barTime := time.Date(2026, 4, 17, 15, 59, 59, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	assert.Len(t, f.fills, 0)
	pos := f.broker.positions[positionKey(domain.Symbol(sym), domain.VenueUnspecified)]
	require.NotNil(t, pos)
	assert.Equal(t, 1.0, pos.quantity, "position must remain open before expiry close")
}

func TestExpireOptions_Idempotent(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "AAPL260417C00100000"
	f.seedOptionLong(t, sym, 1, 5.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 110.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)
	f.broker.ExpireOptions(context.Background(), barTime)

	assert.Len(t, f.fills, 1, "second call must be a no-op")
}

func TestExpireOptions_UnknownUnderlyingPrice_FillsAtZero(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "WIDGET260417C00050000"
	f.seedOptionLong(t, sym, 1, 1.00, "copytrade_v1")
	// No UpdatePrice call for WIDGET.

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	require.Len(t, f.fills, 1)
	assert.Equal(t, 0.0, f.fills[0]["price"].(float64))
	assert.Equal(t, int64(1), f.broker.OptionsExpiredMissingUnderlying())
}

func TestExpireOptions_NonOption_Untouched(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "AAPL"
	f.broker.positions[positionKey(domain.Symbol(sym), domain.VenueUnspecified)] = &position{
		symbol:   domain.Symbol(sym),
		side:     "buy",
		quantity: 100,
		avgCost:  150.0,
		strategy: "tradingthetrend_v1",
	}

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	assert.Len(t, f.fills, 0)
	pos := f.broker.positions[positionKey(domain.Symbol(sym), domain.VenueUnspecified)]
	require.NotNil(t, pos)
	assert.Equal(t, 100.0, pos.quantity)
}

func TestExpireOptions_HalfDaySession(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	// 2026-11-27 is a registered NYSE early close day (13:00 ET).
	const sym = "AAPL261127C00100000"
	f.seedOptionLong(t, sym, 1, 5.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 110.0, time.Date(2026, 11, 27, 11, 0, 0, 0, loc))

	// 12:59 ET: before 13:00 close — no fire.
	barEarly := time.Date(2026, 11, 27, 12, 59, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barEarly)
	assert.Len(t, f.fills, 0, "12:59 ET on half-day must not trigger sweep")

	// 13:00 ET: at the early close — fire.
	barClose := time.Date(2026, 11, 27, 13, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barClose)
	require.Len(t, f.fills, 1)
	assert.InDelta(t, 10.0, f.fills[0]["price"].(float64), 1e-9)
}

func TestExpireOptions_BoundaryInclusive(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "AAPL260417C00100000"
	f.seedOptionLong(t, sym, 1, 5.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 110.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	// barTime EXACTLY at the session close fires (>=, not >).
	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)
	assert.Len(t, f.fills, 1)
}

func TestExpireOptions_DualLegSameDay(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const callSym = "AAPL260417C00100000"
	const putSym = "AAPL260417P00100000"
	f.seedOptionLong(t, callSym, 1, 5.00, "copytrade_v1")
	f.seedOptionLong(t, putSym, 1, 4.00, "copytrade_v1")
	f.setUnderlyingPrice("AAPL", 105.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	require.Len(t, f.fills, 2)
	bySym := map[string]map[string]any{}
	for _, p := range f.fills {
		bySym[p["symbol"].(string)] = p
	}
	require.Contains(t, bySym, callSym)
	require.Contains(t, bySym, putSym)
	assert.InDelta(t, 5.0, bySym[callSym]["price"].(float64), 1e-9, "call intrinsic = 105-100")
	assert.Equal(t, 0.0, bySym[putSym]["price"].(float64), "put OTM at S=105 K=100")
}

func TestExpireOptions_AfterSTC_NoDoubleClose(t *testing.T) {
	f := newExpiryFixture(t)
	loc := nyET(t)

	const sym = "AAPL260417C00100000"
	// Simulate a position that was fully closed by a prior STC: qty=0.
	f.broker.positions[positionKey(domain.Symbol(sym), domain.VenueUnspecified)] = &position{
		symbol:   domain.Symbol(sym),
		side:     "buy",
		quantity: 0,
		avgCost:  5.00,
	}
	f.setUnderlyingPrice("AAPL", 110.0, time.Date(2026, 4, 17, 12, 0, 0, 0, loc))

	barTime := time.Date(2026, 4, 17, 16, 0, 0, 0, loc)
	f.broker.ExpireOptions(context.Background(), barTime)

	assert.Len(t, f.fills, 0)
}
