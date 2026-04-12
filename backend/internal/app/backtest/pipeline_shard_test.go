package backtest

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// Use a stub factory that returns mostly-nil services. NewPipeline tolerates
// nil monitor/runner/priceCache/collector at construction — ProcessBar isn't
// called in these tests.
func stubShardFactory(t *testing.T) ShardFactory {
	t.Helper()
	return func(slab []domain.Symbol) (ShardServices, error) {
		return ShardServices{
			Ingestion: ingestion.NewService(nil, nil, ingestion.NewAdaptiveFilter(20, 4.0), zerolog.Nop()),
		}, nil
	}
}

func TestNewShardedPipeline_PartitionsAllSymbols(t *testing.T) {
	symbols := []domain.Symbol{"AAPL", "MSFT", "GOOGL", "AMZN", "TSLA", "NVDA", "SPY", "QQQ"}
	sp, err := NewShardedPipeline(4, symbols, ShardedInfra{Factory: stubShardFactory(t)})
	if err != nil {
		t.Fatalf("NewShardedPipeline: %v", err)
	}
	if sp.ShardCount() != 4 {
		t.Fatalf("ShardCount=%d want 4", sp.ShardCount())
	}

	seen := make(map[string]bool)
	total := 0
	for i := 0; i < sp.ShardCount(); i++ {
		for _, s := range sp.Slab(i) {
			if seen[s.String()] {
				t.Fatalf("symbol %s appears in multiple shards", s)
			}
			seen[s.String()] = true
			total++
		}
	}
	if total != len(symbols) {
		t.Fatalf("got %d symbols across shards, want %d", total, len(symbols))
	}
	for _, s := range symbols {
		if !seen[s.String()] {
			t.Fatalf("symbol %s not assigned to any shard", s)
		}
	}
}

func TestNewShardedPipeline_ClampsWorkers(t *testing.T) {
	symbols := []domain.Symbol{"A", "B", "C"}
	sp, err := NewShardedPipeline(16, symbols, ShardedInfra{Factory: stubShardFactory(t)})
	if err != nil {
		t.Fatalf("NewShardedPipeline: %v", err)
	}
	if sp.ShardCount() != 3 {
		t.Fatalf("ShardCount=%d want 3 (clamped to len(symbols))", sp.ShardCount())
	}
}

func TestNewShardedPipeline_ZeroWorkersClampsToOne(t *testing.T) {
	symbols := []domain.Symbol{"AAPL", "MSFT"}
	sp, err := NewShardedPipeline(0, symbols, ShardedInfra{Factory: stubShardFactory(t)})
	if err != nil {
		t.Fatalf("NewShardedPipeline: %v", err)
	}
	if sp.ShardCount() != 1 {
		t.Fatalf("ShardCount=%d want 1", sp.ShardCount())
	}
	if len(sp.Slab(0)) != 2 {
		t.Fatalf("single shard should own all symbols, got %v", sp.Slab(0))
	}
}

func TestNewShardedPipeline_RejectsNilFactory(t *testing.T) {
	_, err := NewShardedPipeline(2, []domain.Symbol{"AAPL"}, ShardedInfra{})
	if err == nil {
		t.Fatal("expected error for nil factory")
	}
}

func TestShardIndex_DeterministicAcrossCalls(t *testing.T) {
	for _, sym := range []string{"AAPL", "SPY", "QQQ", "TSLA", "NVDA", "MSFT"} {
		a := shardIndex(sym, 8)
		b := shardIndex(sym, 8)
		if a != b {
			t.Fatalf("shardIndex(%s, 8) non-deterministic: %d vs %d", sym, a, b)
		}
		if a < 0 || a >= 8 {
			t.Fatalf("shardIndex(%s, 8)=%d out of range", sym, a)
		}
	}
}

func TestShardForSymbol_UnknownReturnsNil(t *testing.T) {
	sp, err := NewShardedPipeline(2, []domain.Symbol{"AAPL", "MSFT"}, ShardedInfra{Factory: stubShardFactory(t)})
	if err != nil {
		t.Fatalf("NewShardedPipeline: %v", err)
	}
	if sp.ShardForSymbol("UNKNOWN") != nil {
		t.Fatal("expected nil for unknown symbol")
	}
	if sp.ShardIndexFor("UNKNOWN") != -1 {
		t.Fatal("expected -1 for unknown symbol")
	}
	if sp.ShardForSymbol("AAPL") == nil {
		t.Fatal("expected non-nil for known symbol")
	}
}
