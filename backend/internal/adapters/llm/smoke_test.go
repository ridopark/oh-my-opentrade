//go:build smoke

package llm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/require"
)

// TestSmoke_OpenRouterDebate calls the real OpenRouter API with a minimal
// debate prompt and verifies the response parses correctly.
//
// Run with: go test ./internal/adapters/llm/ -tags smoke -run TestSmoke_OpenRouterDebate -v -timeout 60s
//
// Requires: LLM_API_KEY and LLM_BASE_URL set in environment (or .env loaded).
func TestSmoke_OpenRouterDebate(t *testing.T) {
	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		t.Skip("LLM_API_KEY not set — skipping smoke test")
	}

	baseURL := os.Getenv("LLM_BASE_URL")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api"
	}

	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "anthropic/claude-sonnet-4"
	}

	advisor := NewAdvisor(baseURL, model, apiKey, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	regime := domain.MarketRegime{
		Symbol:    "AAPL",
		Timeframe: "1m",
		Type:      domain.RegimeTrend,
		Since:     time.Now().Add(-30 * time.Minute),
		Strength:  0.75,
	}

	indicators := domain.IndicatorSnapshot{
		Time:      time.Now(),
		Symbol:    "AAPL",
		Timeframe: "1m",
		RSI:       62.5,
		StochK:    71.3,
		StochD:    68.1,
		EMA9:      189.45,
		EMA21:     188.30,
		VWAP:      189.10,
		Volume:    1500000,
		VolumeSMA: 1000000,
	}

	decision, err := advisor.RequestDebate(ctx, "AAPL", regime, indicators)

	// Core assertions
	require.NoError(t, err, "OpenRouter debate call failed")
	require.NotNil(t, decision, "decision is nil")

	t.Logf("Direction:    %s", decision.Direction)
	t.Logf("Confidence:   %.2f", decision.Confidence)
	t.Logf("Rationale:    %s", decision.Rationale)
	t.Logf("Bull:         %s", decision.BullArgument)
	t.Logf("Bear:         %s", decision.BearArgument)
	t.Logf("Judge:        %s", decision.JudgeReasoning)

	// Validate structured fields
	require.Contains(t, []string{"LONG", "SHORT"}, string(decision.Direction), "invalid direction")
	require.GreaterOrEqual(t, decision.Confidence, 0.0)
	require.LessOrEqual(t, decision.Confidence, 1.0)
	require.NotEmpty(t, decision.Rationale, "rationale empty")
	require.NotEmpty(t, decision.BullArgument, "bull argument empty")
	require.NotEmpty(t, decision.BearArgument, "bear argument empty")
	require.NotEmpty(t, decision.JudgeReasoning, "judge reasoning empty")
}
