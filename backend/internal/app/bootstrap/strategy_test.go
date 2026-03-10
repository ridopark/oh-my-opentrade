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
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type stubAIAdvisor struct{}

func (stubAIAdvisor) RequestDebate(_ context.Context, _ domain.Symbol, _ domain.MarketRegime, _ domain.IndicatorSnapshot, _ ...ports.DebateOption) (*domain.AdvisoryDecision, error) {
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
		t.Fatal("Enricher should be non-nil when DisableEnricher=false")
	}
	if len(pipeline.BaseSymbols) == 0 {
		t.Fatal("BaseSymbols should not be empty")
	}
}

func TestBuildStrategyPipeline_NoAI(t *testing.T) {
	specStore := specStoreFromConfigs(t)

	pipeline, err := BuildStrategyPipeline(StrategyDeps{
		EventBus:        stubEventBus{},
		SpecStore:       specStore,
		AIAdvisor:       stubAIAdvisor{},
		TenantID:        "test",
		EnvMode:         domain.EnvModePaper,
		Equity:          100_000,
		Clock:           func() time.Time { return time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC) },
		DisableEnricher: true,
		Logger:          zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("BuildStrategyPipeline returned error: %v", err)
	}

	if pipeline.Enricher != nil {
		t.Fatal("Enricher should be nil when DisableEnricher=true")
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
