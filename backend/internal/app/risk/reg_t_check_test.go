package risk

import (
	"context"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubAccount struct {
	bp  ports.BuyingPower
	err error
}

func (s *stubAccount) GetAccountBuyingPower(_ context.Context) (ports.BuyingPower, error) {
	return s.bp, s.err
}

func TestRegTCheck_UnderLimitPasses(t *testing.T) {
	acct := &stubAccount{bp: ports.BuyingPower{EffectiveBuyingPower: 10_000}}
	c := NewRegTCheck(acct, zerolog.Nop())
	intent := domain.OrderIntent{
		Direction:  domain.DirectionLong,
		LimitPrice: 100,
		Quantity:   100, // notional 10k -> required 5k
		AssetClass: domain.AssetClassEquity,
	}
	assert.NoError(t, c.Check(context.Background(), intent))
}

func TestRegTCheck_AtLimitPasses(t *testing.T) {
	acct := &stubAccount{bp: ports.BuyingPower{EffectiveBuyingPower: 5_000}}
	c := NewRegTCheck(acct, zerolog.Nop())
	intent := domain.OrderIntent{
		Direction:  domain.DirectionLong,
		LimitPrice: 100,
		Quantity:   100, // required 5k = bp
		AssetClass: domain.AssetClassEquity,
	}
	assert.NoError(t, c.Check(context.Background(), intent))
}

func TestRegTCheck_OverLimitRejected(t *testing.T) {
	acct := &stubAccount{bp: ports.BuyingPower{EffectiveBuyingPower: 4_000}}
	c := NewRegTCheck(acct, zerolog.Nop())
	intent := domain.OrderIntent{
		Direction:  domain.DirectionLong,
		LimitPrice: 100,
		Quantity:   100, // required 5k > 4k bp
		AssetClass: domain.AssetClassEquity,
	}
	err := c.Check(context.Background(), intent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reg_t")
}

func TestRegTCheck_OptionsSkipped(t *testing.T) {
	acct := &stubAccount{bp: ports.BuyingPower{EffectiveBuyingPower: 0}}
	c := NewRegTCheck(acct, zerolog.Nop())
	inst, err := domain.NewInstrument(domain.InstrumentTypeOption, "AAPL250117C00180000", "AAPL")
	require.NoError(t, err)
	intent := domain.OrderIntent{
		Direction:  domain.DirectionLong,
		LimitPrice: 100,
		Quantity:   100,
		AssetClass: domain.AssetClassEquity,
		Instrument: &inst,
	}
	assert.NoError(t, c.Check(context.Background(), intent))
}

func TestRegTCheck_ExitSkipped(t *testing.T) {
	acct := &stubAccount{bp: ports.BuyingPower{EffectiveBuyingPower: 0}}
	c := NewRegTCheck(acct, zerolog.Nop())
	intent := domain.OrderIntent{
		Direction:  domain.DirectionCloseLong,
		LimitPrice: 100,
		Quantity:   100,
		AssetClass: domain.AssetClassEquity,
	}
	assert.NoError(t, c.Check(context.Background(), intent))
}

func TestRegTCheck_NilAccountDisabled(t *testing.T) {
	c := NewRegTCheck(nil, zerolog.Nop())
	intent := domain.OrderIntent{
		Direction:  domain.DirectionLong,
		LimitPrice: 100,
		Quantity:   1_000_000,
		AssetClass: domain.AssetClassEquity,
	}
	assert.NoError(t, c.Check(context.Background(), intent))
}
