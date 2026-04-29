package execution

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
)

// stubPositionLookup is the minimal PositionStateLookup needed to drive the
// shouldBlockExitFill guard without standing up the full position monitor.
type stubPositionLookup struct {
	positions map[string]domain.MonitoredPosition
}

func (s *stubPositionLookup) LookupPosition(symbol string) (domain.MonitoredPosition, bool) {
	p, ok := s.positions[symbol]
	return p, ok
}

func TestShouldBlockExitFill_NoLookupConfigured_AllowsAll(t *testing.T) {
	svc := &Service{} // positionLookup nil
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:    "RIVN260508C00017500",
		Direction: domain.DirectionCloseLong,
	}}
	_, blocked := svc.shouldBlockExitFill(po, 47)
	assert.False(t, blocked, "guard must be a no-op when positionLookup isn't wired")
}

func TestShouldBlockExitFill_EntryFill_AlwaysAllowed(t *testing.T) {
	svc := &Service{positionLookup: &stubPositionLookup{}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:    "RIVN260508C00017500",
		Direction: domain.DirectionLong,
	}}
	_, blocked := svc.shouldBlockExitFill(po, 47)
	assert.False(t, blocked, "entry fills must never be blocked by the open-qty guard")
}

func TestShouldBlockExitFill_ExitWithinOpenQty_Allowed(t *testing.T) {
	svc := &Service{positionLookup: &stubPositionLookup{
		positions: map[string]domain.MonitoredPosition{
			"RIVN260508C00017500": {Symbol: "RIVN260508C00017500", Quantity: 47},
		},
	}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:    "RIVN260508C00017500",
		Direction: domain.DirectionCloseLong,
	}}
	_, blocked := svc.shouldBlockExitFill(po, 47)
	assert.False(t, blocked, "fill matching open qty exactly must persist")
}

func TestShouldBlockExitFill_ExitExceedsOpenQty_Blocked(t *testing.T) {
	svc := &Service{positionLookup: &stubPositionLookup{
		positions: map[string]domain.MonitoredPosition{
			"RIVN260508C00017500": {Symbol: "RIVN260508C00017500", Quantity: 0},
		},
	}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:    "RIVN260508C00017500",
		Direction: domain.DirectionCloseLong,
	}}
	reason, blocked := svc.shouldBlockExitFill(po, 47)
	assert.True(t, blocked, "duplicate exit fill (open qty already zero) must be blocked")
	assert.Contains(t, reason, "leg_qty=47")
	assert.Contains(t, reason, "open_qty=0")
}

func TestShouldBlockExitFill_ExitNoTrackedPosition_Blocked(t *testing.T) {
	svc := &Service{positionLookup: &stubPositionLookup{positions: map[string]domain.MonitoredPosition{}}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:    "RIVN260508C00017500",
		Direction: domain.DirectionCloseLong,
	}}
	reason, blocked := svc.shouldBlockExitFill(po, 47)
	assert.True(t, blocked, "exit fill against an absent position must be blocked")
	assert.Contains(t, reason, "no open position")
}

func TestShouldBlockExitFill_ExitFloatTolerance(t *testing.T) {
	svc := &Service{positionLookup: &stubPositionLookup{
		positions: map[string]domain.MonitoredPosition{
			"AAPL": {Symbol: "AAPL", Quantity: 100.0},
		},
	}}
	po := &pendingOrder{intent: domain.OrderIntent{
		Symbol:    "AAPL",
		Direction: domain.DirectionCloseShort,
	}}
	// fp drift below the 1e-9 epsilon must not falsely block.
	_, blocked := svc.shouldBlockExitFill(po, 100.0+1e-12)
	assert.False(t, blocked, "sub-epsilon drift must not block a legitimate full-close fill")
}
