package backtest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestLessEvent_StableOrdering(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	cases := []struct {
		name string
		a, b sliceEvent
		want bool // want a < b
	}{
		{
			name: "earlier tick wins",
			a:    sliceEvent{tickTime: t0, shardIdx: 3, seq: 99},
			b:    sliceEvent{tickTime: t1, shardIdx: 0, seq: 0},
			want: true,
		},
		{
			name: "later tick loses",
			a:    sliceEvent{tickTime: t1, shardIdx: 0, seq: 0},
			b:    sliceEvent{tickTime: t0, shardIdx: 3, seq: 99},
			want: false,
		},
		{
			name: "same tick, lower shardIdx wins",
			a:    sliceEvent{tickTime: t0, shardIdx: 1, seq: 100},
			b:    sliceEvent{tickTime: t0, shardIdx: 2, seq: 0},
			want: true,
		},
		{
			name: "same tick + shard, lower seq wins",
			a:    sliceEvent{tickTime: t0, shardIdx: 2, seq: 1},
			b:    sliceEvent{tickTime: t0, shardIdx: 2, seq: 2},
			want: true,
		},
		{
			name: "identical events are not less-than either direction",
			a:    sliceEvent{tickTime: t0, shardIdx: 2, seq: 5},
			b:    sliceEvent{tickTime: t0, shardIdx: 2, seq: 5},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lessEvent(tc.a, tc.b); got != tc.want {
				t.Fatalf("lessEvent(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestSortSliceEventsForTest_StableMerge(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 30, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	// Three shards, mixed tick times. The expected merged order
	// interleaves by tick, then shardIdx, then seq.
	events := []sliceEvent{
		{tickTime: t1, shardIdx: 0, seq: 0},
		{tickTime: t0, shardIdx: 2, seq: 0},
		{tickTime: t0, shardIdx: 1, seq: 1},
		{tickTime: t0, shardIdx: 1, seq: 0},
		{tickTime: t1, shardIdx: 2, seq: 0},
		{tickTime: t0, shardIdx: 0, seq: 0},
	}
	sortSliceEventsForTest(events)

	want := []struct {
		tick time.Time
		sh   int
		seq  uint64
	}{
		{t0, 0, 0},
		{t0, 1, 0},
		{t0, 1, 1},
		{t0, 2, 0},
		{t1, 0, 0},
		{t1, 2, 0},
	}
	if len(events) != len(want) {
		t.Fatalf("len mismatch: got %d want %d", len(events), len(want))
	}
	for i, w := range want {
		ev := events[i]
		if !ev.tickTime.Equal(w.tick) || ev.shardIdx != w.sh || ev.seq != w.seq {
			t.Fatalf("event[%d] = {%s, shard %d, seq %d}, want {%s, shard %d, seq %d}",
				i, ev.tickTime, ev.shardIdx, ev.seq, w.tick, w.sh, w.seq)
		}
	}
}

func TestRunSliceToCompletion_RejectsNilCoordinator(t *testing.T) {
	sp, err := NewShardedPipeline(2, []domain.Symbol{"AAPL", "MSFT"}, ShardedInfra{Factory: stubShardFactory(t)})
	if err != nil {
		t.Fatalf("NewShardedPipeline: %v", err)
	}
	bars := []SliceBar{{TickTime: time.Now(), Event: domain.Event{Payload: domain.MarketBar{Symbol: "AAPL"}}}}
	if err := sp.RunSliceToCompletion(context.Background(), bars, nil); err == nil {
		t.Fatal("expected error for nil coordinator")
	}
}

func TestRunSliceToCompletion_EmptyBarsIsNoop(t *testing.T) {
	sp, err := NewShardedPipeline(2, []domain.Symbol{"AAPL", "MSFT"}, ShardedInfra{Factory: stubShardFactory(t)})
	if err != nil {
		t.Fatalf("NewShardedPipeline: %v", err)
	}
	coord := &recordingCoordinator{}
	if err := sp.RunSliceToCompletion(context.Background(), nil, coord); err != nil {
		t.Fatalf("RunSliceToCompletion(nil bars): %v", err)
	}
	if coord.tickCount != 0 {
		t.Fatalf("coordinator OnTick invoked %d times on empty run", coord.tickCount)
	}
}

// recordingCoordinator is a test-only SliceCoordinator that tracks
// how many times OnTick has been called and optionally records the
// tick times seen. The PosLookup stub returns no positions so
// reconciliation is a passthrough.
type recordingCoordinator struct {
	tickCount int
	ticks     []time.Time
}

func (r *recordingCoordinator) OnTick(_ context.Context, t time.Time) error {
	r.tickCount++
	r.ticks = append(r.ticks, t)
	return nil
}

func (r *recordingCoordinator) PosLookup(_ string) (domain.MonitoredPosition, bool) {
	return domain.MonitoredPosition{}, false
}

func (r *recordingCoordinator) Logger() *slog.Logger { return nil }
