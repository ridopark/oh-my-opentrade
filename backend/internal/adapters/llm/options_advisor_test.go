package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oh-my-opentrade/backend/internal/adapters/llm"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testOptionChain returns a sample OptionChainSummary for use in tests.
func testOptionChain() llm.OptionChainSummary {
	return llm.OptionChainSummary{
		Candidates: []llm.OptionCandidate{
			{
				ContractSymbol: "AAPL240119C00190000",
				Delta:          0.52,
				IV:             32.0,
				Bid:            3.10,
				Ask:            3.20,
				OpenInterest:   1250,
				DTE:            38,
			},
			{
				ContractSymbol: "AAPL240126C00185000",
				Delta:          0.47,
				IV:             30.5,
				Bid:            4.00,
				Ask:            4.15,
				OpenInterest:   980,
				DTE:            45,
			},
		},
	}
}

// validOptionsCompletionResponse writes a full options debate JSON response.
func validOptionsCompletionResponse(w http.ResponseWriter, contractSymbol string, maxLossUSD float64, exitRules string) {
	inner := map[string]interface{}{
		"direction":       "LONG",
		"confidence":      0.82,
		"rationale":       "Options play on upward momentum",
		"bull_argument":   "Strong breakout above resistance",
		"bear_argument":   "Elevated IV may decay quickly",
		"judge_reasoning": "Bull case is stronger given momentum",
		"contract_symbol": contractSymbol,
		"max_loss_usd":    maxLossUSD,
		"exit_rules":      exitRules,
	}
	innerJSON, _ := json.Marshal(inner)

	resp := map[string]interface{}{
		"choices": []map[string]interface{}{
			{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": string(innerJSON),
				},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// TestAdvisor_RequestDebate_WithOptionChain_Success verifies that a valid options
// debate response is correctly parsed into AdvisoryDecision with all three option fields.
func TestAdvisor_RequestDebate_WithOptionChain_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validOptionsCompletionResponse(w,
			"AAPL240119C00190000",
			320.0,
			"Exit at 2x premium ($640) or 21 days before expiry, whichever comes first",
		)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)
	chain := testOptionChain()

	decision, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("AAPL"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithOptionChain(chain),
	)

	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, domain.DirectionLong, decision.Direction)
	assert.InDelta(t, 0.82, decision.Confidence, 0.001)
	assert.Equal(t, "AAPL240119C00190000", decision.ContractSymbol)
	assert.InDelta(t, 320.0, decision.MaxLossUSD, 0.001)
	assert.Equal(t, "Exit at 2x premium ($640) or 21 days before expiry, whichever comes first", decision.ExitRules)
}

// TestAdvisor_RequestDebate_WithOptionChain_PromptContainsChainData verifies the prompt
// includes candidate contract data and the "MUST select exactly one contract" instruction.
func TestAdvisor_RequestDebate_WithOptionChain_PromptContainsChainData(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validOptionsCompletionResponse(w,
			"AAPL240119C00190000",
			320.0,
			"Exit at 2x premium ($640) or 21 days before expiry, whichever comes first",
		)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)
	chain := testOptionChain()

	_, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("AAPL"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithOptionChain(chain),
	)
	require.NoError(t, err)
	require.NotNil(t, capturedBody)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok, "request body must contain a 'messages' array")
	require.GreaterOrEqual(t, len(messages), 2)

	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	// Must contain candidate contract symbols
	assert.Contains(t, content, "AAPL240119C00190000")
	assert.Contains(t, content, "AAPL240126C00185000")

	// Must contain Greeks / market data fields
	assert.Contains(t, content, "delta=")
	assert.Contains(t, content, "IV=")
	assert.Contains(t, content, "DTE=")

	// Must contain the mandatory selection instruction
	assert.Contains(t, content, "MUST select exactly one contract")

	// Must contain the extra JSON fields in response template
	assert.Contains(t, content, "contract_symbol")
	assert.Contains(t, content, "max_loss_usd")
	assert.Contains(t, content, "exit_rules")
}

// TestAdvisor_RequestDebate_WithOptionChain_MissingContractSymbol ensures an error
// is returned when the LLM omits contract_symbol from its response.
func TestAdvisor_RequestDebate_WithOptionChain_MissingContractSymbol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Returns valid JSON but without contract_symbol (empty string)
		inner := map[string]interface{}{
			"direction":       "LONG",
			"confidence":      0.82,
			"rationale":       "test",
			"bull_argument":   "bull",
			"bear_argument":   "bear",
			"judge_reasoning": "judge",
			// contract_symbol intentionally omitted (zero value "")
			"max_loss_usd": 320.0,
			"exit_rules":   "some exit rules",
		}
		innerJSON, _ := json.Marshal(inner)
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": string(innerJSON)}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("AAPL"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithOptionChain(testOptionChain()),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing contract_symbol")
}

// TestAdvisor_RequestDebate_WithOptionChain_MissingMaxLoss ensures an error
// is returned when max_loss_usd is zero.
func TestAdvisor_RequestDebate_WithOptionChain_MissingMaxLoss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner := map[string]interface{}{
			"direction":       "LONG",
			"confidence":      0.82,
			"rationale":       "test",
			"bull_argument":   "bull",
			"bear_argument":   "bear",
			"judge_reasoning": "judge",
			"contract_symbol": "AAPL240119C00190000",
			// max_loss_usd intentionally omitted (zero value 0)
			"exit_rules": "some exit rules",
		}
		innerJSON, _ := json.Marshal(inner)
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": string(innerJSON)}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("AAPL"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithOptionChain(testOptionChain()),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing max_loss_usd")
}

// TestAdvisor_RequestDebate_WithOptionChain_MissingExitRules ensures an error
// is returned when exit_rules is empty.
func TestAdvisor_RequestDebate_WithOptionChain_MissingExitRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner := map[string]interface{}{
			"direction":       "LONG",
			"confidence":      0.82,
			"rationale":       "test",
			"bull_argument":   "bull",
			"bear_argument":   "bear",
			"judge_reasoning": "judge",
			"contract_symbol": "AAPL240119C00190000",
			"max_loss_usd":    320.0,
			// exit_rules intentionally omitted (zero value "")
		}
		innerJSON, _ := json.Marshal(inner)
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": string(innerJSON)}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("AAPL"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithOptionChain(testOptionChain()),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing exit_rules")
}

// TestAdvisor_RequestDebate_WithOptionChain_ShortRejected ensures an error
// is returned when the LLM proposes a SHORT direction in an options context.
func TestAdvisor_RequestDebate_WithOptionChain_ShortRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner := map[string]interface{}{
			"direction":       "SHORT",
			"confidence":      0.75,
			"rationale":       "test",
			"bull_argument":   "bull",
			"bear_argument":   "bear",
			"judge_reasoning": "judge",
			"contract_symbol": "AAPL240119C00190000",
			"max_loss_usd":    320.0,
			"exit_rules":      "some exit rules",
		}
		innerJSON, _ := json.Marshal(inner)
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": string(innerJSON)}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("AAPL"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithOptionChain(testOptionChain()),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "short position")
}

// TestAdvisor_RequestDebate_BackwardCompat_NoOpts verifies that calling RequestDebate
// without any opts still works as before (equity style), ContractSymbol is empty.
func TestAdvisor_RequestDebate_BackwardCompat_NoOpts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Standard equity response — no options fields
		inner := map[string]interface{}{
			"direction":       "LONG",
			"confidence":      0.80,
			"rationale":       "Strong trend",
			"bull_argument":   "Momentum",
			"bear_argument":   "Overhead supply",
			"judge_reasoning": "Bull wins",
		}
		innerJSON, _ := json.Marshal(inner)
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": string(innerJSON)}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	// No opts — backward-compatible call
	decision, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("BTCUSD"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
	)

	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, domain.DirectionLong, decision.Direction)
	assert.Equal(t, "", decision.ContractSymbol, "equity debate must have empty ContractSymbol")
	assert.Equal(t, 0.0, decision.MaxLossUSD, "equity debate must have zero MaxLossUSD")
	assert.Equal(t, "", decision.ExitRules, "equity debate must have empty ExitRules")
}
