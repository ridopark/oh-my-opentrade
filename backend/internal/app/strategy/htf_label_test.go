package strategy

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

func TestRunner_TagBacktest_SetsHTFLabelSuffix(t *testing.T) {
	bus := memory.NewBus()
	router := NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	r := NewRunner(bus, router, "test-tenant", envMode, nil)

	if r.htfLabelSuffix != "" {
		t.Fatalf("default suffix must be empty, got %q", r.htfLabelSuffix)
	}

	r.TagBacktest("abc")
	if got, want := r.htfLabelSuffix, "_backtest_abc"; got != want {
		t.Fatalf("htfLabelSuffix=%q, want %q", got, want)
	}

	calc := monitor.NewIndicatorCalculator()
	calc.Label = "runner_htf" + r.htfLabelSuffix
	if got, want := calc.Label, "runner_htf_backtest_abc"; got != want {
		t.Fatalf("calc label=%q, want %q", got, want)
	}
}

func TestRunner_NoTagBacktest_KeepsLiveLabel(t *testing.T) {
	bus := memory.NewBus()
	router := NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	r := NewRunner(bus, router, "test-tenant", envMode, nil)

	calc := monitor.NewIndicatorCalculator()
	calc.Label = "runner_htf" + r.htfLabelSuffix
	if got, want := calc.Label, "runner_htf"; got != want {
		t.Fatalf("calc label=%q, want %q (live path must keep base label)", got, want)
	}
}
