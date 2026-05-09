package bootstrap

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type stubAIAdvisor struct{}

func (stubAIAdvisor) RequestDebate(_ context.Context, _ domain.Symbol, _ domain.MarketRegime, _ domain.IndicatorSnapshot, _ ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
	return nil, nil
}

func (stubAIAdvisor) SelectAnchors(_ context.Context, _ ports.AnchorSelectionRequest) (*strat.AnchorSelection, error) {
	return nil, nil
}

func specStoreFromConfigs(t *testing.T) *store_fs.Store {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	backendRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
	specDir := filepath.Join(backendRoot, "configs", "strategies")
	return store_fs.NewStore(specDir, strategy.LoadSpecFile)
}

func TestBuildStrategyPipeline(t *testing.T) {
	specStore := specStoreFromConfigs(t)

	pipeline, err := BuildStrategyPipeline(StrategyDeps{
		EventBus:  stubEventBus{},
		SpecStore: specStore,
		AIAdvisor: stubAIAdvisor{},
		TenantID:  "test",
		EnvMode:   domain.EnvModePaper,
		Equity:    100_000,
		Clock:     func() time.Time { return time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC) },
		Logger:    zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildStrategyPipeline returned error: %v", err)
	}

	if pipeline.Runner == nil {
		t.Fatal("Runner is nil")
	}
	if pipeline.Router == nil {
		t.Fatal("Router is nil")
	}
	if pipeline.RiskSizer == nil {
		t.Fatal("RiskSizer is nil")
	}
	if pipeline.LifecycleSvc == nil {
		t.Fatal("LifecycleSvc is nil")
	}
	if pipeline.Enricher == nil {
		t.Fatal("Enricher should be non-nil when DisableAI=false")
	}
	if len(pipeline.BaseSymbols) == 0 {
		t.Fatal("BaseSymbols should not be empty")
	}
}

func TestBuildStrategyShared(t *testing.T) {
	specStore := specStoreFromConfigs(t)
	shared, err := BuildStrategyShared(StrategyDeps{
		EventBus:  stubEventBus{},
		SpecStore: specStore,
		AIAdvisor: stubAIAdvisor{},
		TenantID:  "test",
		EnvMode:   domain.EnvModePaper,
		Equity:    100_000,
		Clock:     func() time.Time { return time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC) },
		Logger:    zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildStrategyShared: %v", err)
	}
	if shared.Registry == nil {
		t.Fatal("Registry nil")
	}
	if len(shared.Specs) == 0 {
		t.Fatal("Specs empty")
	}
	if shared.RiskSizer == nil {
		t.Fatal("RiskSizer nil")
	}
	if shared.Enricher == nil {
		t.Fatal("Enricher should be non-nil when DisableAI=false")
	}
}

func TestBuildStrategyShard_SlabFilter(t *testing.T) {
	specStore := specStoreFromConfigs(t)
	deps := StrategyDeps{
		EventBus:        stubEventBus{},
		SpecStore:       specStore,
		AIAdvisor:       stubAIAdvisor{},
		TenantID:        "test",
		EnvMode:         domain.EnvModePaper,
		Equity:          100_000,
		Clock:     func() time.Time { return time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC) },
		DisableAI: true,
		Logger:    zerolog.Nop(),
	}
	shared, err := BuildStrategyShared(deps)
	if err != nil {
		t.Fatalf("BuildStrategyShared: %v", err)
	}

	// Full (nil slab) shard registers every active spec×symbol.
	full, err := BuildStrategyShard(shared, nil, deps)
	if err != nil {
		t.Fatalf("BuildStrategyShard(nil slab): %v", err)
	}
	if len(full.BaseSymbols) == 0 {
		t.Fatal("full shard: BaseSymbols empty")
	}

	// Pick the first base symbol and build a single-symbol slab shard.
	pick := full.BaseSymbols[0]
	slab := []domain.Symbol{domain.Symbol(pick)}
	restricted, err := BuildStrategyShard(shared, slab, deps)
	if err != nil {
		t.Fatalf("BuildStrategyShard(slab): %v", err)
	}
	if len(restricted.BaseSymbols) != 1 || restricted.BaseSymbols[0] != pick {
		t.Fatalf("restricted shard: want BaseSymbols=[%s], got %v", pick, restricted.BaseSymbols)
	}

	// Router must have instances for the picked symbol and NONE for any
	// other symbol in the full shard.
	if got := restricted.Router.InstancesForSymbol(pick); len(got) == 0 {
		t.Fatalf("restricted shard: no instances for %s", pick)
	}
	for _, other := range full.BaseSymbols {
		if other == pick {
			continue
		}
		if got := restricted.Router.InstancesForSymbol(other); len(got) != 0 {
			t.Fatalf("restricted shard: unexpected instances for %s (slab should have excluded it)", other)
		}
	}

	// Non-matching slab yields an empty shard (BaseSymbols empty, no
	// instances) but still returns a valid Runner/Router.
	empty, err := BuildStrategyShard(shared, []domain.Symbol{"___NOPE___"}, deps)
	if err != nil {
		t.Fatalf("BuildStrategyShard(empty-intersection slab): %v", err)
	}
	if len(empty.BaseSymbols) != 0 {
		t.Fatalf("empty slab: expected no base symbols, got %v", empty.BaseSymbols)
	}
	if empty.Runner == nil || empty.Router == nil {
		t.Fatal("empty slab: Runner/Router must still be non-nil")
	}
}

func TestBuildStrategyShard_SentinelExtraSymbols(t *testing.T) {
	specStore := specStoreFromConfigs(t)
	deps := StrategyDeps{
		EventBus:  stubEventBus{},
		SpecStore: specStore,
		AIAdvisor: stubAIAdvisor{},
		TenantID:  "test",
		EnvMode:   domain.EnvModePaper,
		Equity:    100_000,
		Clock:     func() time.Time { return time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC) },
		DisableAI: true,
		Logger:    zerolog.Nop(),
		SentinelExtraSymbols: map[string][]string{
			"copytrade_v1": {"FAKE1", "FAKE2"},
		},
	}
	shared, err := BuildStrategyShared(deps)
	if err != nil {
		t.Fatalf("BuildStrategyShared: %v", err)
	}

	shard, err := BuildStrategyShardWithSentinels(shared, nil, deps)
	if err != nil {
		t.Fatalf("BuildStrategyShardWithSentinels: %v", err)
	}

	for _, ticker := range []string{"FAKE1", "FAKE2"} {
		insts := shard.Router.InstancesForSymbol(ticker)
		if len(insts) == 0 {
			t.Fatalf("router missing pre-registered ticker %s", ticker)
		}
		var foundCopytrade bool
		for _, inst := range insts {
			if inst.Strategy().Meta().ID.String() == "copytrade_v1" {
				foundCopytrade = true
				if _, seeded := inst.GetState(ticker); !seeded {
					t.Fatalf("copytrade_v1 instance has no per-symbol state for pre-registered ticker %s", ticker)
				}
				break
			}
		}
		if !foundCopytrade {
			t.Fatalf("ticker %s did not resolve to copytrade_v1 instance", ticker)
		}
	}
}

func TestBuildStrategyPipeline_NoAI(t *testing.T) {
	specStore := specStoreFromConfigs(t)

	pipeline, err := BuildStrategyPipeline(StrategyDeps{
		EventBus:  stubEventBus{},
		SpecStore: specStore,
		AIAdvisor: stubAIAdvisor{},
		TenantID:  "test",
		EnvMode:   domain.EnvModePaper,
		Equity:    100_000,
		Clock:     func() time.Time { return time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC) },
		DisableAI: true,
		Logger:    zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildStrategyPipeline returned error: %v", err)
	}

	// With DisableAI=true the enricher is still constructed; it just
	// short-circuits the LLM call. This keeps the risk_sizer subscription
	// chain intact so no_ai backtests can emit trades.
	if pipeline.Enricher == nil {
		t.Fatal("Enricher should be non-nil even when DisableAI=true (skip-AI mode)")
	}
	if pipeline.Runner == nil {
		t.Fatal("Runner is nil")
	}
	if pipeline.Router == nil {
		t.Fatal("Router is nil")
	}
	if pipeline.RiskSizer == nil {
		t.Fatal("RiskSizer is nil")
	}
}
