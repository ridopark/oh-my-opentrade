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

// TestWithSignalContext_PromptContainsSignalData verifies that passing
// WithSignalContext injects signal metadata into the user prompt.
func TestWithSignalContext_PromptContainsSignalData(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "AI rationale", "bull", "bear", "judge", 0.85)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	tags := map[string]string{
		"setup":     "avwap_breakout",
		"ref_price": "450.25",
		"regime_5m": "BALANCE",
	}

	decision, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("SPY"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithSignalContext(tags, "entry", "buy", 0.70),
	)

	require.NoError(t, err)
	require.NotNil(t, decision)

	// Extract user prompt from captured request body.
	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages), 2)

	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	// Must contain signal context section.
	assert.Contains(t, content, "Signal Context")
	assert.Contains(t, content, "entry")
	assert.Contains(t, content, "buy")
	assert.Contains(t, content, "0.70")

	// Must contain signal tags.
	assert.Contains(t, content, "avwap_breakout")
	assert.Contains(t, content, "450.25")
	assert.Contains(t, content, "BALANCE")
}

// TestWithSignalContext_BackwardCompat_NoSignalContext verifies that not passing
// WithSignalContext produces a prompt WITHOUT the "Signal Context" section,
// preserving backward compatibility with existing callers.
func TestWithSignalContext_BackwardCompat_NoSignalContext(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("BTCUSD"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		// No WithSignalContext option — backward compatible call.
	)
	require.NoError(t, err)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	// Must NOT contain signal context section.
	assert.NotContains(t, content, "Signal Context")
}

// TestWithSignalContext_CombinedWithOptionChain verifies that WithSignalContext
// and WithOptionChain can be used together.
func TestWithSignalContext_CombinedWithOptionChain(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validOptionsCompletionResponse(w, "AAPL240119C00190000", 320.0, "exit at 2x premium")
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)
	chain := testOptionChain()

	tags := map[string]string{"setup": "avwap_breakout"}

	decision, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("AAPL"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithOptionChain(chain),
		llm.WithSignalContext(tags, "entry", "buy", 0.75),
	)

	require.NoError(t, err)
	require.NotNil(t, decision)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	// Must contain both signal context AND option chain data.
	assert.Contains(t, content, "Signal Context")
	assert.Contains(t, content, "avwap_breakout")
	assert.Contains(t, content, "AAPL240119C00190000")
	assert.Contains(t, content, "MUST select exactly one contract")
}

// TestWithSignalContext_EmptyTags verifies the option works with nil/empty tags.
func TestWithSignalContext_EmptyTags(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "SHORT", "sell signal", "bear momentum", "oversold bounce", "bear wins", 0.72)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(
		context.Background(),
		domain.Symbol("QQQ"),
		getMockMarketRegime(),
		getMockIndicatorSnapshot(),
		llm.WithSignalContext(nil, "entry", "sell", 0.65),
	)
	require.NoError(t, err)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	// Must contain signal context even with empty tags.
	assert.Contains(t, content, "Signal Context")
	assert.Contains(t, content, "entry")
	assert.Contains(t, content, "sell")
	assert.Contains(t, content, "0.65")
}
