package debate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubLiveData struct {
	greeks ports.OptionGreeks
	err    error
	calls  int
}

func (s *stubLiveData) Quote(_ context.Context, _ string, _ time.Time, _ float64, _ string) (ports.OptionQuote, error) {
	return ports.OptionQuote{}, errors.New("not used")
}

func (s *stubLiveData) Greeks(_ context.Context, _ string, _ time.Time, _ float64, _ string) (ports.OptionGreeks, error) {
	s.calls++
	return s.greeks, s.err
}

func TestFetchLiveIV_ReturnsLivePortValue(t *testing.T) {
	s := &Service{}
	s.SetOptionLiveData(&stubLiveData{greeks: ports.OptionGreeks{IV: 0.42}})

	iv, err := s.fetchLiveIV(context.Background(), "AAPL", time.Now(), 150, "C")
	require.NoError(t, err)
	assert.InDelta(t, 0.42, iv, 1e-9)
}

func TestFetchLiveIV_NoPortReturnsNotConfigured(t *testing.T) {
	s := &Service{}
	_, err := s.fetchLiveIV(context.Background(), "AAPL", time.Now(), 150, "C")
	assert.ErrorIs(t, err, ports.ErrOptionDataNotConfigured)
}

func TestFetchLiveIV_PortErrorPropagates(t *testing.T) {
	s := &Service{}
	want := errors.New("upstream timeout")
	s.SetOptionLiveData(&stubLiveData{err: want})
	_, err := s.fetchLiveIV(context.Background(), "AAPL", time.Now(), 150, "C")
	assert.ErrorIs(t, err, want)
}
