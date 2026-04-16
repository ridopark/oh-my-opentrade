package ibkr

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAAPLCallSpread returns a 2-leg AAPL vertical call spread: +150C / -155C.
func newAAPLCallSpread() []domain.ComboLeg {
	expiry := time.Date(2026, 5, 15, 16, 0, 0, 0, time.UTC)
	return []domain.ComboLeg{
		{Symbol: "AAPL", Right: domain.OptionRightCall, Strike: 150, Expiry: expiry, Ratio: 1, AssetType: domain.InstrumentTypeOption},
		{Symbol: "AAPL", Right: domain.OptionRightCall, Strike: 155, Expiry: expiry, Ratio: -1, AssetType: domain.InstrumentTypeOption},
	}
}

func TestBuildBAGContract_VerticalCallSpread(t *testing.T) {
	callCount := int32(0)
	mock := &mockIB{connected: true}
	mock.reqContractDetailsFn = func(c *ibsync.Contract) ([]ibsync.ContractDetails, error) {
		atomic.AddInt32(&callCount, 1)
		// Fake conID: 1000 + strike to make them distinct.
		cd := ibsync.NewContractDetails()
		cd.Contract = *c
		cd.Contract.ConID = int64(1000 + int(c.Strike))
		return []ibsync.ContractDetails{*cd}, nil
	}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	bag, err := a.BuildBAGContract(context.Background(), newAAPLCallSpread())
	require.NoError(t, err)
	require.NotNil(t, bag)
	assert.Equal(t, "BAG", bag.SecType)
	assert.Equal(t, "AAPL", bag.Symbol)
	assert.Equal(t, "SMART", bag.Exchange)
	assert.Equal(t, "USD", bag.Currency)
	require.Len(t, bag.ComboLegs, 2)

	assert.Equal(t, "BUY", bag.ComboLegs[0].Action)
	assert.Equal(t, int64(1), bag.ComboLegs[0].Ratio)
	assert.Equal(t, int64(1150), bag.ComboLegs[0].ConID)

	assert.Equal(t, "SELL", bag.ComboLegs[1].Action)
	assert.Equal(t, int64(1), bag.ComboLegs[1].Ratio)
	assert.Equal(t, int64(1155), bag.ComboLegs[1].ConID)

	// Cache hit on second build for same legs: no additional reqContractDetails.
	_, err = a.BuildBAGContract(context.Background(), newAAPLCallSpread())
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount), "expected conID cache to prevent re-lookup")
}

func TestBuildBAGContract_RejectsNonTwoLegs(t *testing.T) {
	mock := &mockIB{connected: true}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	_, err := a.BuildBAGContract(context.Background(), newAAPLCallSpread()[:1])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly 2 legs")
}

func TestBuildBAGContract_PropagatesLookupError(t *testing.T) {
	mock := &mockIB{connected: true}
	mock.reqContractDetailsFn = func(_ *ibsync.Contract) ([]ibsync.ContractDetails, error) {
		return nil, errors.New("boom")
	}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	_, err := a.BuildBAGContract(context.Background(), newAAPLCallSpread())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestSubmitComboOrder_RequiresCombo(t *testing.T) {
	mock := &mockIB{connected: true}
	a := NewAdapterWithClient(mock, zerolog.Nop())

	// Plain equity intent has no legs -> rejected.
	_, err := a.SubmitComboOrder(context.Background(), domain.OrderIntent{Symbol: "AAPL", Quantity: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a combo")
}
