package symbolrouter_test

import (
	"testing"

	"github.com/oh-my-opentrade/backend/internal/app/symbolrouter"
	"github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/stretchr/testify/assert"
)

func TestResolveEffectiveSymbols(t *testing.T) {
	ranked := []screener.RankedSymbol{
		{Symbol: "AAPL", TotalScore: 90},
		{Symbol: "TSLA", TotalScore: 80},
		{Symbol: "MSFT", TotalScore: 70},
	}
	base := []string{"AAPL", "GOOGL", "MSFT"}

	tests := []struct {
		name     string
		mode     string
		base     []string
		ranked   []screener.RankedSymbol
		wantSyms []string
		wantSrc  string
	}{
		{
			name:     "static ignores screener",
			mode:     "static",
			base:     base,
			ranked:   ranked,
			wantSyms: []string{"AAPL", "GOOGL", "MSFT"},
			wantSrc:  "static",
		},
		{
			name:     "replace uses screener only",
			mode:     "replace",
			base:     base,
			ranked:   ranked,
			wantSyms: []string{"AAPL", "TSLA", "MSFT"},
			wantSrc:  "screener",
		},
		{
			name:     "intersection keeps common in screener order",
			mode:     "intersection",
			base:     base,
			ranked:   ranked,
			wantSyms: []string{"AAPL", "MSFT"},
			wantSrc:  "intersection",
		},
		{
			name:     "union merges screener first then base remainder",
			mode:     "union",
			base:     base,
			ranked:   ranked,
			wantSyms: []string{"AAPL", "TSLA", "MSFT", "GOOGL"},
			wantSrc:  "union",
		},
		{
			name:     "empty screener falls back to base",
			mode:     "intersection",
			base:     base,
			ranked:   nil,
			wantSyms: []string{"AAPL", "GOOGL", "MSFT"},
			wantSrc:  "fallback:no_screener_results",
		},
		{
			name:     "empty base returns empty for intersection",
			mode:     "intersection",
			base:     nil,
			ranked:   ranked,
			wantSyms: []string{},
			wantSrc:  "intersection",
		},
		{
			name:     "unknown mode defaults to intersection",
			mode:     "banana",
			base:     base,
			ranked:   ranked,
			wantSyms: []string{"AAPL", "MSFT"},
			wantSrc:  "intersection",
		},
		{
			name:     "empty base and empty screener",
			mode:     "intersection",
			base:     nil,
			ranked:   nil,
			wantSyms: []string{},
			wantSrc:  "fallback:no_screener_results",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, src := symbolrouter.ResolveEffectiveSymbols(tt.mode, tt.base, tt.ranked)
			assert.Equal(t, tt.wantSyms, got)
			assert.Equal(t, tt.wantSrc, src)
		})
	}
}
