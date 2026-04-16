package options

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubPort struct {
	quote  ports.OptionQuote
	greeks ports.OptionGreeks
	err    error
	calls  int
}

func (s *stubPort) Quote(_ context.Context, _ string, _ time.Time, _ float64, _ string) (ports.OptionQuote, error) {
	s.calls++
	return s.quote, s.err
}

func (s *stubPort) Greeks(_ context.Context, _ string, _ time.Time, _ float64, _ string) (ports.OptionGreeks, error) {
	s.calls++
	return s.greeks, s.err
}

func TestComposite_PrimaryServes(t *testing.T) {
	primary := &stubPort{quote: ports.OptionQuote{Bid: 1.0, Ask: 1.1}}
	fb := &stubPort{err: errors.New("should not be called")}
	c := NewComposite(primary, fb)

	q, err := c.Quote(context.Background(), "AAPL", time.Now(), 150, "C")
	require.NoError(t, err)
	assert.Equal(t, 1.0, q.Bid)
	assert.Equal(t, 1, primary.calls)
	assert.Equal(t, 0, fb.calls)
}

func TestComposite_FallbackUsedOnError(t *testing.T) {
	primary := &stubPort{err: ports.ErrOptionDataNotConfigured}
	fb := &stubPort{greeks: ports.OptionGreeks{IV: 0.42}}
	c := NewComposite(primary, fb)

	g, err := c.Greeks(context.Background(), "AAPL", time.Now(), 150, "C")
	require.NoError(t, err)
	assert.Equal(t, 0.42, g.IV)
	assert.Equal(t, 1, primary.calls)
	assert.Equal(t, 1, fb.calls)
}

func TestComposite_AllFailReturnsAggregate(t *testing.T) {
	a := &stubPort{err: errors.New("a-down")}
	b := &stubPort{err: errors.New("b-down")}
	c := NewComposite(a, b)

	_, err := c.Quote(context.Background(), "AAPL", time.Now(), 150, "C")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a-down")
	assert.Contains(t, err.Error(), "b-down")
}

func TestComposite_NilSourcesSkipped(t *testing.T) {
	primary := &stubPort{greeks: ports.OptionGreeks{IV: 0.30}}
	c := NewComposite(nil, nil, primary, nil)

	g, err := c.Greeks(context.Background(), "AAPL", time.Now(), 150, "C")
	require.NoError(t, err)
	assert.Equal(t, 0.30, g.IV)
}

func TestComposite_EmptyChainReturnsNotConfigured(t *testing.T) {
	c := NewComposite(nil)
	_, err := c.Quote(context.Background(), "AAPL", time.Now(), 150, "C")
	assert.ErrorIs(t, err, ports.ErrOptionDataNotConfigured)
	_, err = c.Greeks(context.Background(), "AAPL", time.Now(), 150, "C")
	assert.ErrorIs(t, err, ports.ErrOptionDataNotConfigured)
}
