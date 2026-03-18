package sweep

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/backtest"
)

func TestGenerateGrid_SingleParam(t *testing.T) {
	grid := GenerateGrid([]ParamRange{
		{Key: "hold_bars", Min: 4, Max: 8, Step: 2},
	})
	if len(grid) != 3 {
		t.Fatalf("expected 3 combos, got %d", len(grid))
	}
	vals := []float64{4, 6, 8}
	for i, want := range vals {
		got, ok := grid[i]["hold_bars"].(float64)
		if !ok || got != want {
			t.Errorf("grid[%d][hold_bars] = %v, want %v", i, grid[i]["hold_bars"], want)
		}
	}
}

func TestGenerateGrid_TwoParams(t *testing.T) {
	grid := GenerateGrid([]ParamRange{
		{Key: "a", Min: 1, Max: 2, Step: 1},
		{Key: "b", Min: 10, Max: 20, Step: 10},
	})
	if len(grid) != 4 {
		t.Fatalf("expected 2x2=4 combos, got %d", len(grid))
	}
}

func TestGenerateGrid_Empty(t *testing.T) {
	grid := GenerateGrid(nil)
	if len(grid) != 1 {
		t.Fatalf("expected 1 empty combo, got %d", len(grid))
	}
	if len(grid[0]) != 0 {
		t.Errorf("expected empty map, got %v", grid[0])
	}
}

func TestGenerateGrid_FloatStep(t *testing.T) {
	grid := GenerateGrid([]ParamRange{
		{Key: "atr", Min: 2.0, Max: 3.0, Step: 0.5},
	})
	if len(grid) != 3 {
		t.Fatalf("expected 3 combos (2.0, 2.5, 3.0), got %d", len(grid))
	}
}

func TestTotalRuns(t *testing.T) {
	tests := []struct {
		name   string
		ranges []ParamRange
		want   int
	}{
		{"empty", nil, 1},
		{"single", []ParamRange{{Key: "a", Min: 1, Max: 5, Step: 1}}, 5},
		{"two", []ParamRange{{Key: "a", Min: 1, Max: 3, Step: 1}, {Key: "b", Min: 10, Max: 20, Step: 5}}, 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TotalRuns(tt.ranges)
			if got != tt.want {
				t.Errorf("TotalRuns = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRankRuns(t *testing.T) {
	runs := []SweepRunResult{
		{Index: 0, Metrics: backtest.Result{SharpeRatio: 1.0}},
		{Index: 1, Metrics: backtest.Result{SharpeRatio: 3.0}},
		{Index: 2, Metrics: backtest.Result{SharpeRatio: 2.0}},
	}

	ranked := RankRuns(runs, "sharpe_ratio", false)
	if ranked[0].Index != 1 || ranked[1].Index != 2 || ranked[2].Index != 0 {
		t.Errorf("ranking wrong: got indices %d,%d,%d want 1,2,0",
			ranked[0].Index, ranked[1].Index, ranked[2].Index)
	}

	asc := RankRuns(runs, "sharpe_ratio", true)
	if asc[0].Index != 0 {
		t.Errorf("ascending: first should be index 0, got %d", asc[0].Index)
	}
}
