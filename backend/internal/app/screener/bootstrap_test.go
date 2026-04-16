package screener

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	screenerdomain "github.com/oh-my-opentrade/backend/internal/domain/screener"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	strategyports "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/rs/zerolog"
)

// --- mock AI screener repo ---

type mockAIScreenerRepo struct {
	results map[string][]screenerdomain.AIScreenerResult
}

func (m *mockAIScreenerRepo) SaveAIResults(_ context.Context, _ []screenerdomain.AIScreenerResult) error {
	return nil
}

func (m *mockAIScreenerRepo) GetLatestAIResults(_ context.Context, _, _, strategyKey string) ([]screenerdomain.AIScreenerResult, error) {
	return m.results[strategyKey], nil
}

// --- mock spec store ---

type mockSpecStore struct {
	specs []strategyports.Spec
}

func (m *mockSpecStore) List(_ context.Context, _ *strategyports.SpecFilter) ([]strategyports.Spec, error) {
	return m.specs, nil
}

func (m *mockSpecStore) Get(_ context.Context, _ domstrategy.StrategyID, _ domstrategy.Version) (*strategyports.Spec, error) {
	return nil, nil
}

func (m *mockSpecStore) GetLatest(_ context.Context, _ domstrategy.StrategyID) (*strategyports.Spec, error) {
	return nil, nil
}

func (m *mockSpecStore) Save(_ context.Context, _ strategyports.Spec) error {
	return nil
}

func (m *mockSpecStore) Watch(_ context.Context) (<-chan domstrategy.StrategyID, error) {
	return nil, nil
}

// --- tests ---

func TestBootstrapFromDB_PublishesEvents(t *testing.T) {
	repo := &mockAIScreenerRepo{
		results: map[string][]screenerdomain.AIScreenerResult{
			"strat_a": {
				{
					RunID:       "run-1",
					AsOf:        time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
					StrategyKey: "strat_a",
					Symbol:      "AAPL",
					Score:       4,
					Rationale:   "strong gap",
					Model:       "test-model",
				},
				{
					RunID:       "run-1",
					AsOf:        time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
					StrategyKey: "strat_a",
					Symbol:      "TSLA",
					Score:       3,
					Rationale:   "ok gap",
					Model:       "test-model",
				},
			},
		},
	}

	specStore := &mockSpecStore{
		specs: []strategyports.Spec{
			{
				ID:        "strat_a",
				Screening: strategyports.ScreeningConfig{Description: "momentum breakout"},
			},
			{
				ID: "strat_b",
			},
		},
	}

	bus := &mockBus{}
	log := zerolog.Nop()

	covered := BootstrapFromDB(context.Background(), repo, specStore, "default", "paper", bus, log)

	if len(covered) != 1 {
		t.Fatalf("expected 1 covered strategy, got %d", len(covered))
	}
	if !covered["strat_a"] {
		t.Fatal("expected strat_a to be covered")
	}
	if len(bus.published) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(bus.published))
	}
	if bus.published[0].Type != domain.EventAIScreenerCompleted {
		t.Fatalf("expected EventAIScreenerCompleted, got %s", bus.published[0].Type)
	}
}

func TestBootstrapFromDB_NilBus(t *testing.T) {
	repo := &mockAIScreenerRepo{
		results: map[string][]screenerdomain.AIScreenerResult{
			"strat_a": {
				{
					RunID:       "run-1",
					AsOf:        time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
					StrategyKey: "strat_a",
					Symbol:      "AAPL",
					Score:       4,
					Rationale:   "strong",
					Model:       "test",
				},
			},
		},
	}
	specStore := &mockSpecStore{
		specs: []strategyports.Spec{
			{
				ID:        "strat_a",
				Screening: strategyports.ScreeningConfig{Description: "test"},
			},
		},
	}

	covered := BootstrapFromDB(context.Background(), repo, specStore, "default", "paper", nil, zerolog.Nop())

	if len(covered) != 1 {
		t.Fatalf("expected 1 covered strategy with nil bus, got %d", len(covered))
	}
}

func TestAIService_RunAIScreen_NilBus(t *testing.T) {
	svc, err := NewAIService(
		zerolog.Nop(),
		config.AIScreenerConfig{
			Enabled: true,
			Models:  []string{"test-model"},
		},
		config.AIConfig{BaseURL: "http://localhost:9999"},
		"default",
		"paper",
		nil,
		&mockSnapshots{},
		&mockMarketData{},
		nil,
		&mockAIScreenerRepo{results: map[string][]screenerdomain.AIScreenerResult{}},
		&mockSpecStore{specs: nil},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error creating AIService with nil bus: %v", err)
	}

	err = svc.RunAIScreen(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("unexpected error from RunAIScreen with nil bus: %v", err)
	}
}

func TestAIService_NilBus_Allowed(t *testing.T) {
	_, err := NewAIService(
		zerolog.Nop(),
		config.AIScreenerConfig{},
		config.AIConfig{},
		"default",
		"paper",
		nil,
		&mockSnapshots{},
		&mockMarketData{},
		nil,
		&mockAIScreenerRepo{results: map[string][]screenerdomain.AIScreenerResult{}},
		&mockSpecStore{},
		nil,
	)
	if err != nil {
		t.Fatalf("NewAIService should accept nil bus, got: %v", err)
	}
}
