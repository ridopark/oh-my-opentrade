package positionmonitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type modifyingNotifier struct {
	recordingRepegNotifier
	modifyCalls []modifyCall
	modifiedOK  bool
	modifyErr   error
}

type modifyCall struct {
	orderID  string
	newLimit float64
}

func (n *modifyingNotifier) RepegOrderInPlace(_ context.Context, orderID string, newLimit float64) (bool, error) {
	n.modifyCalls = append(n.modifyCalls, modifyCall{orderID: orderID, newLimit: newLimit})
	return n.modifiedOK, n.modifyErr
}

type stubOptionsPricePort struct {
	quotes map[domain.Symbol]domain.OptionQuote
	err    error
}

func (p *stubOptionsPricePort) GetOptionPrices(_ context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
	if p.err != nil {
		return nil, p.err
	}
	out := make(map[domain.Symbol]domain.OptionQuote, len(symbols))
	for _, s := range symbols {
		if q, ok := p.quotes[s]; ok {
			out[s] = q
		}
	}
	return out, nil
}

func newModifyTestService(t *testing.T, broker *trackingBroker, notifier *modifyingNotifier, sym domain.Symbol, quote domain.OptionQuote) *Service {
	t.Helper()
	repo := &capturingRepo{}
	svc := newTestServiceWithBrokerAndRepo(&broker.mockBroker, repo)
	svc.broker = broker
	svc.SetRepegNotifier(notifier)
	svc.SetRepegModifyInPlace(true)
	svc.optionsPricePort = &stubOptionsPricePort{
		quotes: map[domain.Symbol]domain.OptionQuote{sym: quote},
	}
	return svc
}

// On a successful modify the broker MUST NOT receive any cancel and
// ExitOrderID MUST be preserved across the repeg.
func TestHandleExitTimeout_ModifyInPlace_Success(t *testing.T) {
	broker := &trackingBroker{}
	notifier := &modifyingNotifier{modifiedOK: true}
	sym := domain.Symbol("AAPL_OPT_MOD_OK")
	svc := newModifyTestService(t, broker, notifier, sym, domain.OptionQuote{Bid: 1.40, Ask: 1.50, BidSize: 10, AskSize: 10})

	pos := seedOptionPendingExit(t, svc, string(sym), domain.ExitRulePremiumTarget, time.Second)
	pos.ExitWallStartedAt = pos.ExitPendingAt
	priorOrderID := pos.ExitOrderID

	svc.tick()

	require.Len(t, notifier.modifyCalls, 1, "RepegOrderInPlace must fire exactly once")
	assert.Equal(t, priorOrderID, notifier.modifyCalls[0].orderID)
	assert.Greater(t, notifier.modifyCalls[0].newLimit, 0.0)
	assert.Empty(t, broker.cancelCalls, "no broker cancel may fire on the modify path")
	assert.Empty(t, notifier.calls, "MarkRepegCancel must not be called when modify succeeds")
	assert.Equal(t, priorOrderID, pos.ExitOrderID,
		"ExitOrderID must be preserved across an in-place modify")
	assert.Equal(t, 1, pos.ExitRepegCount, "ExitRepegCount must bump exactly once")
	assert.InDelta(t, notifier.modifyCalls[0].newLimit, pos.ExitLastSentPrice, 1e-9)
	assert.False(t, pos.ExitManaging, "ExitManaging must clear after a successful modify")
}

// ErrUnsupportedModify (simbroker, or order already terminal) MUST fall
// through to the legacy cancel+place flow unchanged.
func TestHandleExitTimeout_ModifyInPlace_Unsupported_FallsThrough(t *testing.T) {
	broker := &trackingBroker{detailsStatus: "canceled"}
	notifier := &modifyingNotifier{modifiedOK: false, modifyErr: ports.ErrUnsupportedModify}
	sym := domain.Symbol("AAPL_OPT_MOD_FALLBACK")
	svc := newModifyTestService(t, broker, notifier, sym, domain.OptionQuote{Bid: 1.40, Ask: 1.50, BidSize: 10, AskSize: 10})

	pos := seedOptionPendingExit(t, svc, string(sym), domain.ExitRulePremiumTarget, time.Second)
	pos.ExitWallStartedAt = pos.ExitPendingAt

	svc.tick()

	require.Len(t, notifier.modifyCalls, 1, "RepegOrderInPlace must be tried first")
	assert.Contains(t, broker.cancelCalls, "live-order-1",
		"on ErrUnsupportedModify the legacy cancel+place flow must engage")
	assert.Contains(t, notifier.calls, "live-order-1",
		"MarkRepegCancel must precede the cancel on the legacy fallback")
}

// A hard adapter error (orderID unknown to broker) MUST refuse fall-through
// to cancel+place; cancel against an unknown order followed by a place
// would create a duplicate position.
func TestHandleExitTimeout_ModifyInPlace_HardError_RefusesFallThrough(t *testing.T) {
	broker := &trackingBroker{}
	notifier := &modifyingNotifier{modifiedOK: false, modifyErr: errors.New("ibkr: ModifyOrder: order 4118 unknown to broker")}
	sym := domain.Symbol("AAPL_OPT_MOD_HARD")
	svc := newModifyTestService(t, broker, notifier, sym, domain.OptionQuote{Bid: 1.40, Ask: 1.50, BidSize: 10, AskSize: 10})

	pos := seedOptionPendingExit(t, svc, string(sym), domain.ExitRulePremiumTarget, time.Second)
	pos.ExitWallStartedAt = pos.ExitPendingAt

	svc.tick()

	require.Len(t, notifier.modifyCalls, 1)
	assert.Empty(t, broker.cancelCalls, "no cancel may fire after a hard modify error")
	assert.Empty(t, notifier.calls, "MarkRepegCancel must not run on the hard-error path")
	assert.False(t, pos.ExitManaging, "ExitManaging must clear so the next tick can re-evaluate")
	assert.Equal(t, 0, pos.ExitRepegCount, "ExitRepegCount must NOT advance on a hard error")
}
