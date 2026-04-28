package positionmonitor

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fix 2 regression: the LLY 850P incident happened because bootstrap
// restored only option_premium, leaving EstimatedPremium without BSM
// inputs. After Fix 2, the OCC parse + IV calibration must populate
// strike/expiry_unix/is_call/iv_at_entry on every restored option
// position so EstimatedPremium and HasBSMInputs() both work.
func TestService_bootstrapPositions_RestoresBSMInputsForOption(t *testing.T) {
	bus := &mockEventBus{}
	pc := NewPriceCache(zerolog.Nop())

	contract := domain.Symbol("LLY260508P00850000")
	underlying := domain.Symbol("LLY")

	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	entryTime := now.Add(-4 * time.Minute)

	// Seed priceCache with a current underlying snapshot so the bootstrap
	// path resolves underlyingPrice and triggers restoreOptionBSMInputs.
	pc.UpdatePrice(underlying, 869.37, now)

	broker := &mockBroker{positions: []domain.Trade{{
		Symbol:         contract,
		Quantity:       1,
		Price:          25.66,
		AssetClass:     domain.AssetClassEquity,
		TenantID:       "tenant-1",
		EnvMode:        domain.EnvModePaper,
		InstrumentType: domain.InstrumentTypeOption,
		OptionRight:    "PUT",
	}}}
	pg := execution.NewPositionGate(broker, zerolog.Nop())

	repo := &mockRepo{trades: []domain.Trade{{
		Symbol:     contract,
		Side:       "BUY",
		Price:      25.66,
		Quantity:   1,
		Strategy:   "avwap_v4",
		AssetClass: domain.AssetClassEquity,
		Time:       entryTime,
		TenantID:   "tenant-1",
		EnvMode:    domain.EnvModePaper,
	}}}

	svc := NewService(bus, pc, pg, "tenant-1", domain.EnvModePaper, zerolog.Nop(),
		WithBroker(broker),
		WithRepo(repo),
		WithNowFunc(func() time.Time { return now }),
	)

	svc.bootstrapPositions(context.Background())

	require.Equal(t, 1, svc.PositionCount(), "bootstrap should restore the LLY option position")
	pos, ok := svc.positions["tenant-1:Paper:"+string(contract)]
	require.True(t, ok)

	require.NotNil(t, pos.CustomState)
	assert.InDelta(t, 850.0, pos.CustomState["strike"], 0.001, "strike should match OCC suffix")
	assert.Greater(t, pos.CustomState["expiry_unix"], float64(0), "expiry_unix must be set from OCC date")
	assert.Equal(t, 0.0, pos.CustomState["is_call"], "PUT must record is_call=0")
	assert.Greater(t, pos.CustomState["iv_at_entry"], 0.0, "iv_at_entry must be calibrated or defaulted")
	assert.Less(t, pos.CustomState["iv_at_entry"], 5.0, "iv_at_entry must be in a sane range")
	assert.Equal(t, 1.0, pos.CustomState["bsm_inputs_restored_at_boot"], "marker flag must be set")
	assert.InDelta(t, 25.66, pos.CustomState["option_premium"], 0.001, "entry premium must still be restored")

	// HasBSMInputs is the property evaluatePremiumStop relies on to suppress
	// the post-restart false-fire. Verify it returns true after bootstrap.
	assert.True(t, pos.HasBSMInputs(), "post-bootstrap position must satisfy HasBSMInputs()")
}

// Fix 3 regression: when triggerExit is asked to exit an option position
// without BSM inputs, without a live quote, and without option_premium,
// the underlying spot must NOT leak through as the limit price. Refuse
// the attempt and clear ExitPending so the next tick can retry.
func TestTriggerExit_OptionMissingAllAnchors_RefusesAndClearsExitPending(t *testing.T) {
	svc := newTestServiceWithBrokerAndRepo(&mockBroker{}, &capturingRepo{})
	contract := domain.Symbol("LLY260508P00850000")

	pos := &domain.MonitoredPosition{
		TenantID:       svc.tenantID,
		EnvMode:        svc.envMode,
		Symbol:         contract,
		InstrumentType: domain.InstrumentTypeOption,
		OptionRight:    "PUT",
		Strategy:       "avwap_v4",
		Quantity:       1,
		EntryPrice:     869.37, // underlying entry, not premium
		EntryTime:      time.Now().Add(-4 * time.Minute),
		Side:           "BUY",
		ExitRules:      []domain.ExitRule{},
		CustomState:    map[string]float64{}, // no option_premium, no BSM inputs
	}
	key := pos.PositionKey()
	svc.positions[key] = pos

	rule := domain.ExitRule{Type: domain.ExitRulePremiumStop, Params: map[string]float64{"threshold": 0.10}}
	svc.triggerExit(pos, rule, "premium_stop: premium exhausted (entry=25.66, est=0.00)", 869.37, time.Now())

	assert.False(t, pos.ExitPending, "triggerExit must clear ExitPending when no anchor resolves")
	select {
	case msg := <-svc.outbox:
		t.Fatalf("no intent must be queued when option price cannot be resolved, got %v", msg.Intent.LimitPrice)
	default:
	}
}

// Counter-regression: when option_premium IS present in CustomState (the
// usual post-fill or post-bootstrap state), triggerExit must succeed —
// using option_premium as the magnitude anchor and forcing market routing
// when BSM and quote are both unavailable.
func TestTriggerExit_OptionWithEntryPremium_ForcesMarketWithAnchor(t *testing.T) {
	svc := newTestServiceWithBrokerAndRepo(&mockBroker{}, &capturingRepo{})
	contract := domain.Symbol("LLY260508P00850000")

	pos := &domain.MonitoredPosition{
		TenantID:       svc.tenantID,
		EnvMode:        svc.envMode,
		Symbol:         contract,
		InstrumentType: domain.InstrumentTypeOption,
		OptionRight:    "PUT",
		Strategy:       "avwap_v4",
		Quantity:       1,
		EntryPrice:     869.37,
		EntryTime:      time.Now().Add(-4 * time.Minute),
		Side:           "BUY",
		ExitRules:      []domain.ExitRule{},
		CustomState: map[string]float64{
			"option_premium": 25.66,
		},
	}
	key := pos.PositionKey()
	svc.positions[key] = pos

	rule := domain.ExitRule{Type: domain.ExitRulePremiumStop, Params: map[string]float64{"threshold": 0.10}}
	svc.triggerExit(pos, rule, "premium_stop: premium exhausted", 869.37, time.Now())

	require.True(t, pos.ExitPending, "ExitPending must be set when anchor resolves")
	select {
	case msg := <-svc.outbox:
		assert.InDelta(t, 25.66, msg.Intent.LimitPrice, 0.001,
			"limit stamp must be option_premium magnitude, not underlying spot")
		assert.Equal(t, "market", msg.Intent.OrderType, "no-anchor path must force MARKET routing")
	default:
		t.Fatal("an exit intent must be queued when option_premium anchors the price")
	}
}
