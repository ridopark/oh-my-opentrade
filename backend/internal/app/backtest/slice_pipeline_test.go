package backtest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestRunSliceToCompletion_RejectsNilCoordinator(t *testing.T) {
	sp, err := NewShardedPipeline(2, []domain.Symbol{"AAPL", "MSFT"}, ShardedInfra{Factory: stubShardFactory(t)})
	if err != nil {
		t.Fatalf("NewShardedPipeline: %v", err)
	}
	bars := []SliceBar{{TickTime: time.Now(), Bar: domain.MarketBar{Symbol: "AAPL"}}}
	if err := sp.RunSliceToCompletion(context.Background(), bars, time.Time{}, nil); err == nil {
		t.Fatal("expected error for nil coordinator")
	}
}

func TestRunSliceToCompletion_EmptyBarsIsNoop(t *testing.T) {
	sp, err := NewShardedPipeline(2, []domain.Symbol{"AAPL", "MSFT"}, ShardedInfra{Factory: stubShardFactory(t)})
	if err != nil {
		t.Fatalf("NewShardedPipeline: %v", err)
	}
	coord := &recordingCoordinator{}
	if err := sp.RunSliceToCompletion(context.Background(), nil, time.Time{}, coord); err != nil {
		t.Fatalf("RunSliceToCompletion(nil bars): %v", err)
	}
	if coord.tickCount != 0 {
		t.Fatalf("coordinator OnTickEnd invoked %d times on empty run", coord.tickCount)
	}
}

// recordingCoordinator is a test-only SliceCoordinator that counts
// OnTickEnd callbacks and records tick times seen. The PosLookup
// stub returns no positions so reconciliation is a passthrough.
type recordingCoordinator struct {
	tickCount  int
	beginTicks []time.Time
	endTicks   []time.Time
}

func (r *recordingCoordinator) OnPhaseATickAdvance(_ context.Context, _ time.Time) error {
	return nil
}

func (r *recordingCoordinator) OnTickBegin(_ context.Context, t time.Time) error {
	r.beginTicks = append(r.beginTicks, t)
	return nil
}

func (r *recordingCoordinator) OnTickEnd(_ context.Context, t time.Time) error {
	r.tickCount++
	r.endTicks = append(r.endTicks, t)
	return nil
}

func (r *recordingCoordinator) OnBar(_ context.Context, _ domain.MarketBar) error { return nil }

func (r *recordingCoordinator) PosLookup(_ string) (domain.MonitoredPosition, bool) {
	return domain.MonitoredPosition{}, false
}

func (r *recordingCoordinator) Logger() *slog.Logger { return nil }
