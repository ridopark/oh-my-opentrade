// Tests for the gap-detect Service orchestrator using fake collaborators.
package gapdetect

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

type fakeDetector struct {
	calls int
	gaps  []ports.GapRange
	err   error
}

func (f *fakeDetector) FindMissingBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]ports.GapRange, error) {
	f.calls++
	return f.gaps, f.err
}

type fakeReader struct {
	t *time.Time
}

func (f *fakeReader) GetLatestMarketBarTime(_ context.Context, _ domain.Symbol, _ domain.Timeframe) (*time.Time, error) {
	return f.t, nil
}

func TestService_RunOnce_AggregatesAcrossTimeframes(t *testing.T) {
	now := time.Date(2026, time.April, 14, 12, 0, 0, 0, time.UTC)
	latest := now.Add(-5 * time.Minute)

	det := &fakeDetector{
		gaps: []ports.GapRange{
			{ExpectedCount: 100, ActualCount: 95},
		},
	}
	svc := NewService(det, &fakeReader{t: &latest}, zerolog.Nop(), func() time.Time { return now })

	total := svc.RunOnce(context.Background(), []domain.Symbol{"AAPL", "MSFT"})
	assert.Equal(t, 2*len(scanWindows), total, "one gap per (symbol,tf) combination")
	assert.Equal(t, 2*len(scanWindows), det.calls)
}

func TestService_RunOnce_NoSymbols(t *testing.T) {
	svc := NewService(&fakeDetector{}, &fakeReader{}, zerolog.Nop(), func() time.Time { return time.Now() })
	assert.Equal(t, 0, svc.RunOnce(context.Background(), nil))
}
