package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/llm"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getMockIndicatorSnapshot returns a test IndicatorSnapshot with sensible defaults.
func getMockIndicatorSnapshot(overrides ...func(*domain.IndicatorSnapshot)) domain.IndicatorSnapshot {
	snap := domain.IndicatorSnapshot{
		Time:      time.Now(),
		Symbol:    "BTCUSD",
		Timeframe: "1h",
		RSI:       42.5,
		StochK:    30.0,
		StochD:    28.5,
		EMA9:      50100.0,
		EMA21:     49900.0,
		VWAP:      50000.0,
		Volume:    1200.0,
		VolumeSMA: 900.0,
	}
	for _, fn := range overrides {
		fn(&snap)
	}
	return snap
}

// getMockMarketRegime returns a test MarketRegime with sensible defaults.
func getMockMarketRegime(overrides ...func(*domain.MarketRegime)) domain.MarketRegime {
	regime := domain.MarketRegime{
		Symbol:    "BTCUSD",
		Timeframe: "1h",
		Type:      domain.RegimeTrend,
		Since:     time.Now().Add(-time.Hour),
		Strength:  0.75,
	}
	for _, fn := range overrides {
		fn(&regime)
	}
	return regime
}

// validCompletionResponse writes an OpenAI-compatible /v1/chat/completions response
// whose assistant message content is the structured debate JSON.
func validCompletionResponse(w http.ResponseWriter, direction, rationale, bull, bear, judge string, confidence float64) {
	inner := map[string]interface{}{
		"direction":       direction,
		"confidence":      confidence,
		"rationale":       rationale,
		"bull_argument":   bull,
		"bear_argument":   bear,
		"judge_reasoning": judge,
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

// --- Tests ---

func TestAdvisor_RequestDebate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		validCompletionResponse(w, "LONG", "AI says buy", "strong uptrend", "supply overhead", "bull wins", 0.87)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)
	snap := getMockIndicatorSnapshot()
	regime := getMockMarketRegime()

	decision, err := advisor.RequestDebate(context.Background(), "BTCUSD", regime, snap)

	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, domain.DirectionLong, decision.Direction)
	assert.InDelta(t, 0.87, decision.Confidence, 0.001)
	assert.Equal(t, "AI says buy", decision.Rationale)
	assert.Equal(t, "strong uptrend", decision.BullArgument)
	assert.Equal(t, "supply overhead", decision.BearArgument)
	assert.Equal(t, "bull wins", decision.JudgeReasoning)
}

func TestAdvisor_RequestDebate_ShortDirection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validCompletionResponse(w, "SHORT", "AI says sell", "bearish momentum", "oversold bounce possible", "bear wins", 0.78)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	decision, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())

	require.NoError(t, err)
	assert.Equal(t, domain.DirectionShort, decision.Direction)
	assert.InDelta(t, 0.78, decision.Confidence, 0.001)
}

func TestAdvisor_RequestDebate_SendsStructuredPrompt(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	snap := getMockIndicatorSnapshot(func(s *domain.IndicatorSnapshot) {
		s.RSI = 55.0
		s.StochK = 45.0
		s.StochD = 40.0
		s.EMA9 = 51000.0
		s.EMA21 = 50000.0
		s.VWAP = 50500.0
	})
	regime := getMockMarketRegime(func(r *domain.MarketRegime) {
		r.Type = domain.RegimeBalance
		r.Strength = 0.6
	})

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", regime, snap)
	require.NoError(t, err)

	require.NotNil(t, capturedBody)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok, "request body must contain a 'messages' array")
	require.GreaterOrEqual(t, len(messages), 2, "must have at least system + user messages")

	sysMsg := messages[0].(map[string]interface{})
	sysContent := sysMsg["content"].(string)
	assert.Contains(t, sysContent, "Professional Risk Manager")
	assert.Contains(t, sysContent, "worst-case scenarios")

	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	assert.Contains(t, content, `"bull_argument"`)
	assert.Contains(t, content, `"judge_reasoning"`)
	assert.Contains(t, content, `"LONG | SHORT | NEUTRAL"`)

	assert.Contains(t, content, "BTCUSD")
	assert.Contains(t, content, "BALANCE")
	assert.Contains(t, content, "55")    // RSI
	assert.Contains(t, content, "45")    // StochK
	assert.Contains(t, content, "51000") // EMA9
	assert.Contains(t, content, "50000") // EMA21
	assert.Contains(t, content, "50500") // VWAP

	// EMA9=51000 > EMA21=50000 → Bullish Cross with +2.00% spread
	assert.Contains(t, content, "EMA Trend:")
	assert.Contains(t, content, "Bullish Cross")
	assert.Contains(t, content, "+2.00%")

	// EMA9=51000 vs VWAP=50500 → +0.99% above VWAP
	assert.Contains(t, content, "VWAP Position:")
	assert.Contains(t, content, "above VWAP")
}

func TestAdvisor_RequestDebate_PromptIncludesAnchorRegimes(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	snap := getMockIndicatorSnapshot(func(s *domain.IndicatorSnapshot) {
		s.AnchorRegimes = map[domain.Timeframe]domain.MarketRegime{
			"1m":  {Symbol: "BTCUSD", Timeframe: "1m", Type: domain.RegimeTrend, Strength: 0.80},
			"5m":  {Symbol: "BTCUSD", Timeframe: "5m", Type: domain.RegimeBalance, Strength: 0.55},
			"15m": {Symbol: "BTCUSD", Timeframe: "15m", Type: domain.RegimeReversal, Strength: 0.90},
		}
	})
	regime := getMockMarketRegime()

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", regime, snap)
	require.NoError(t, err)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	assert.Contains(t, content, "Multi-Timeframe Regimes:")
	assert.Contains(t, content, "1m: TREND (strength: 0.80)")
	assert.Contains(t, content, "5m: BALANCE (strength: 0.55)")
	assert.Contains(t, content, "15m: REVERSAL (strength: 0.90) (Primary Context)")
	assert.NotContains(t, content, "1m: TREND (strength: 0.80) (Primary Context)")
}

func TestAdvisor_RequestDebate_NoAnchorRegimes_OmitsSection(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	snap := getMockIndicatorSnapshot()
	regime := getMockMarketRegime()

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", regime, snap)
	require.NoError(t, err)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	assert.NotContains(t, content, "Multi-Timeframe Regimes:")
}

func TestAdvisor_RequestDebate_BearishDivergenceAndBelowVWAP(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "SHORT", "rationale", "bull", "bear", "judge", 0.75)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	snap := getMockIndicatorSnapshot(func(s *domain.IndicatorSnapshot) {
		s.EMA9 = 49000.0
		s.EMA21 = 50000.0
		s.VWAP = 50500.0
	})

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), snap)
	require.NoError(t, err)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	assert.Contains(t, content, "Bearish Divergence")
	assert.Contains(t, content, "EMA9 < EMA21")
	assert.Contains(t, content, "below VWAP")
}

func TestAdvisor_RequestDebate_OverextendedVWAP(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	snap := getMockIndicatorSnapshot(func(s *domain.IndicatorSnapshot) {
		s.EMA9 = 52000.0
		s.EMA21 = 50000.0
		s.VWAP = 50000.0
	})

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), snap)
	require.NoError(t, err)

	messages, ok := capturedBody["messages"].([]interface{})
	require.True(t, ok)
	userMsg := messages[len(messages)-1].(map[string]interface{})
	content := userMsg["content"].(string)

	// EMA9=52000 vs VWAP=50000 → +4.00% → overextended
	assert.Contains(t, content, "overextended")
	assert.Contains(t, content, "+4.00%")
}

func TestAdvisor_RequestDebate_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "llm")
}

func TestAdvisor_RequestDebate_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "llm")
}

func TestAdvisor_RequestDebate_NoChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no choices")
}

func TestAdvisor_RequestDebate_ServerUnreachable(t *testing.T) {
	advisor := llm.NewAdvisor("http://127.0.0.1:1", "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())

	require.Error(t, err)
}

func TestAdvisor_RequestDebate_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		}
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := advisor.RequestDebate(ctx, "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())

	require.Error(t, err)
}

func TestNewAdvisor_ReturnsNonNil(t *testing.T) {
	advisor := llm.NewAdvisor("http://localhost:8080", "", "", nil) // nil client → defaults to http.DefaultClient
	assert.NotNil(t, advisor)
}

func TestNewAdvisor_DefaultModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "anthropic/claude-sonnet-4", body["model"])
		validCompletionResponse(w, "LONG", "r", "b", "be", "j", 0.8)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "", "", nil) // empty model → uses default
	_, err := advisor.RequestDebate(context.Background(), "AAPL", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)
}

func TestAdvisor_RateLimit_SecondCallWithinIntervalIsRejected(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	// 500ms minimum interval — first call should pass, second (immediate) should be rejected.
	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient,
		llm.WithMinInterval(500*time.Millisecond))

	// First call: must succeed.
	_, err := advisor.RequestDebate(context.Background(), "AAPL", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)

	// Second call immediately after: must be rejected with rate-limit error.
	_, err = advisor.RequestDebate(context.Background(), "AAPL", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit")

	// Only 1 actual HTTP call must have been made.
	assert.Equal(t, 1, callCount)
}

func TestAdvisor_RateLimit_CallAfterIntervalSucceeds(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	// Very short interval so the test doesn't take long.
	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient,
		llm.WithMinInterval(30*time.Millisecond))

	// First call.
	_, err := advisor.RequestDebate(context.Background(), "AAPL", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)

	time.Sleep(40 * time.Millisecond) // wait out the interval

	// Second call after interval: must succeed.
	_, err = advisor.RequestDebate(context.Background(), "AAPL", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)

	assert.Equal(t, 2, callCount)
}

func TestAdvisor_NoMinInterval_AllCallsSucceed(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	// No WithMinInterval option — both rapid calls should hit the server.
	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(context.Background(), "AAPL", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)
	_, err = advisor.RequestDebate(context.Background(), "AAPL", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)

	assert.Equal(t, 2, callCount)
}

func TestAdvisor_RequestDebate_NeutralDirection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validCompletionResponse(w, "NEUTRAL", "no clear edge", "bull case", "bear case", "judge neutral", 0.5)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	decision, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())

	require.NoError(t, err)
	assert.Nil(t, decision, "NEUTRAL direction should return nil decision, not an error")
}

func TestAdvisor_ProviderRouting_IncludedWhenSet(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient,
		llm.WithProviderRouting("latency", nil))

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)

	// Provider field must be present with sort=latency.
	prov, ok := capturedBody["provider"].(map[string]interface{})
	require.True(t, ok, "request body must contain a 'provider' object")
	assert.Equal(t, "latency", prov["sort"])
}

func TestAdvisor_ProviderRouting_OmittedWhenNotSet(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	// No WithProviderRouting option — provider field must NOT appear in JSON.
	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient)

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)

	_, exists := capturedBody["provider"]
	assert.False(t, exists, "provider field must be omitted when not configured")
}

func TestAdvisor_ProviderRouting_WithPreferredMaxLatency(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewDecoder(r.Body).Decode(&capturedBody)
		assert.NoError(t, err)
		validCompletionResponse(w, "LONG", "rationale", "bull", "bear", "judge", 0.80)
	}))
	defer server.Close()

	advisor := llm.NewAdvisor(server.URL, "test-model", "", http.DefaultClient,
		llm.WithProviderRouting("", map[string]float64{"p90": 2.0}))

	_, err := advisor.RequestDebate(context.Background(), "BTCUSD", getMockMarketRegime(), getMockIndicatorSnapshot())
	require.NoError(t, err)

	prov, ok := capturedBody["provider"].(map[string]interface{})
	require.True(t, ok, "request body must contain a 'provider' object")
	// sort should NOT be present (empty string → omitempty).
	_, hasSortKey := prov["sort"]
	assert.False(t, hasSortKey, "empty sort should be omitted")
	// preferred_max_latency should be present.
	latency, ok := prov["preferred_max_latency"].(map[string]interface{})
	require.True(t, ok, "provider must contain preferred_max_latency")
	assert.InDelta(t, 2.0, latency["p90"], 0.001)
}
