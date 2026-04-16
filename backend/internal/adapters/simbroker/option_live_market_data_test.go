package simbroker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockOptionLiveData struct {
	bid float64
	err error
}

func (m *mockOptionLiveData) Quote(_ context.Context, _ string, _ time.Time, _ float64, _ string) (ports.OptionQuote, error) {
	if m.err != nil {
		return ports.OptionQuote{}, m.err
	}
	return ports.OptionQuote{Bid: m.bid, Ask: m.bid + 0.05}, nil
}

func (m *mockOptionLiveData) Greeks(_ context.Context, _ string, _ time.Time, _ float64, _ string) (ports.OptionGreeks, error) {
	return ports.OptionGreeks{}, errors.New("not used")
}

func TestComputeOptionExitPrice_UsesLivePortBid(t *testing.T) {
	b := newTestBroker()
	b.SetOptionLiveData(&mockOptionLiveData{bid: 7.42})

	barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")
	meta := map[string]string{
		"strike":           "150.0",
		"expiry":           "2026-04-10",
		"option_right":     "CALL",
		"iv_at_entry":      "0.30",
		"premium":          "5.00",
		"entry_underlying": "150.0",
		"entry_date":       "2026-04-06",
		"underlying":       "AAPL",
	}
	intent := makeOptionExitIntent("AAPL260410C00150000", meta)
	price := b.computeOptionExitPrice(intent, 153.0, barTime)
	assert.Equal(t, 7.42, price, "live bid must take priority over BSM approximation")
}

func TestComputeOptionExitPrice_FallsBackOnLiveError(t *testing.T) {
	b := newTestBroker()
	b.SetOptionLiveData(&mockOptionLiveData{err: ports.ErrOptionDataNotConfigured})

	barTime, _ := time.Parse("2006-01-02 15:04", "2026-04-06 14:30")
	meta := map[string]string{
		"strike":           "150.0",
		"expiry":           "2026-04-10",
		"option_right":     "CALL",
		"iv_at_entry":      "0.30",
		"premium":          "5.00",
		"entry_underlying": "150.0",
		"entry_date":       "2026-04-06",
		"underlying":       "AAPL",
	}
	intent := makeOptionExitIntent("AAPL260410C00150000", meta)
	price := b.computeOptionExitPrice(intent, 153.0, barTime)
	require.Greater(t, price, 0.0)
	// Same range as the existing same-day BSM test — proves we fell back.
	assert.Greater(t, price, 3.0)
	assert.Less(t, price, 10.0)
}
