package builtin

import (
	"log/slog"
	"testing"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/stretchr/testify/assert"
)

type stubCtx struct {
	backtest bool
}

func (c *stubCtx) Now() time.Time                 { return time.Time{} }
func (c *stubCtx) Logger() *slog.Logger           { return slog.Default() }
func (c *stubCtx) EmitDomainEvent(_ any) error    { return nil }
func (c *stubCtx) ProgressEventsSuppressed() bool { return false }
func (c *stubCtx) EnvMode() start.EnvMode         { return start.EnvModePaper }
func (c *stubCtx) IsBacktest() bool               { return c.backtest }

func TestArmPendingEntry_LiveMode_KeepsHandshake(t *testing.T) {
	var positionSide, pendingEntry start.Side
	var pendingAt time.Time
	now := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)

	armPendingEntry(&positionSide, &pendingEntry, &pendingAt, start.SideBuy, now, &stubCtx{backtest: false})

	assert.Empty(t, string(positionSide), "live mode must NOT set PositionSide on emit")
	assert.Equal(t, start.SideBuy, pendingEntry, "live mode sets PendingEntry until FillConfirmation")
	assert.Equal(t, now, pendingAt)
}

func TestArmPendingEntry_BacktestMode_OptimisticSet(t *testing.T) {
	var positionSide, pendingEntry start.Side
	var pendingAt time.Time
	now := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)

	armPendingEntry(&positionSide, &pendingEntry, &pendingAt, start.SideBuy, now, &stubCtx{backtest: true})

	assert.Equal(t, start.SideBuy, positionSide, "backtest mode sets PositionSide on emit")
	assert.Empty(t, string(pendingEntry), "backtest mode clears PendingEntry on emit")
	assert.True(t, pendingAt.IsZero(), "backtest mode clears PendingEntryAt")
}

func TestArmPendingEntry_NilContext_LiveDefault(t *testing.T) {
	var positionSide, pendingEntry start.Side
	var pendingAt time.Time
	armPendingEntry(&positionSide, &pendingEntry, &pendingAt, start.SideBuy, time.Now(), nil)

	assert.Empty(t, string(positionSide), "nil ctx defaults to live broker-confirmation handshake")
	assert.Equal(t, start.SideBuy, pendingEntry)
}

func TestRollbackPendingEntry_RefundsWhenArmedInBacktest(t *testing.T) {
	positionSide := start.SideBuy
	pendingEntry := start.Side("")
	pendingAt := time.Time{}
	tradesToday := 1

	rollbackPendingEntry(&positionSide, &pendingEntry, &pendingAt, &tradesToday)

	assert.Empty(t, string(positionSide))
	assert.Empty(t, string(pendingEntry))
	assert.True(t, pendingAt.IsZero())
	assert.Equal(t, 0, tradesToday, "backtest rejection refunds the daily cap counter")
}

func TestRollbackPendingEntry_RefundsWhenArmedInLive(t *testing.T) {
	positionSide := start.Side("")
	pendingEntry := start.SideBuy
	pendingAt := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)
	tradesToday := 1

	rollbackPendingEntry(&positionSide, &pendingEntry, &pendingAt, &tradesToday)

	assert.Empty(t, string(pendingEntry))
	assert.True(t, pendingAt.IsZero())
	assert.Equal(t, 0, tradesToday, "live rejection (PendingEntry set) also refunds")
}

func TestRollbackPendingEntry_NoRefundWhenNothingToRoll(t *testing.T) {
	positionSide := start.Side("")
	pendingEntry := start.Side("")
	pendingAt := time.Time{}
	tradesToday := 3

	rollbackPendingEntry(&positionSide, &pendingEntry, &pendingAt, &tradesToday)

	assert.Equal(t, 3, tradesToday, "rollback called with no armed entry must not refund")
}

func TestRollbackPendingEntry_NeverGoesNegative(t *testing.T) {
	positionSide := start.SideBuy
	pendingEntry := start.SideBuy
	pendingAt := time.Now()
	tradesToday := 0

	rollbackPendingEntry(&positionSide, &pendingEntry, &pendingAt, &tradesToday)

	assert.Equal(t, 0, tradesToday, "TradesToday must not go negative")
}
