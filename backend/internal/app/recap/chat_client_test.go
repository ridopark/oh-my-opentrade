package recap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPChatClient_OpenAICompat_ShapeAndResponse(t *testing.T) {
	var (
		gotPath   string
		gotAuth   string
		gotBody   map[string]any
		gotMethod string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from openai"}}]}`))
	}))
	defer srv.Close()

	c := NewHTTPChatClientWithProvider(ProviderOpenAI, srv.URL, "test-key", srv.Client())
	out, err := c.Chat(context.Background(), "gpt-4o-mini", "you are an analyst", "today's fills: ...")
	require.NoError(t, err)
	assert.Equal(t, "from openai", out)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/v1/chat/completions", gotPath)
	assert.Equal(t, "Bearer test-key", gotAuth)

	// Body carries model + [system, user] messages.
	assert.Equal(t, "gpt-4o-mini", gotBody["model"])
	msgs, ok := gotBody["messages"].([]any)
	require.True(t, ok, "messages field missing or wrong type")
	require.Len(t, msgs, 2)
	first := msgs[0].(map[string]any)
	assert.Equal(t, "system", first["role"])
	assert.Equal(t, "you are an analyst", first["content"])
	second := msgs[1].(map[string]any)
	assert.Equal(t, "user", second["role"])
}

func TestHTTPChatClient_Anthropic_NativeShape(t *testing.T) {
	var (
		gotPath       string
		gotAPIKey     string
		gotAnthropVer string
		gotBody       map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotAnthropVer = r.Header.Get("anthropic-version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		// Mimic Anthropic native response.
		_, _ = w.Write([]byte(`{
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"content":[{"type":"text","text":"from anthropic"}]
		}`))
	}))
	defer srv.Close()

	c := NewHTTPChatClientWithProvider(ProviderAnthropic, srv.URL, "sk-ant-test", srv.Client())
	out, err := c.Chat(context.Background(), "claude-haiku-4-5", "sys prompt", "user prompt")
	require.NoError(t, err)
	assert.Equal(t, "from anthropic", out)

	assert.Equal(t, "/v1/messages", gotPath)
	assert.Equal(t, "sk-ant-test", gotAPIKey, "anthropic uses x-api-key header, not Authorization")
	assert.Equal(t, anthropicVersion, gotAnthropVer)

	// Anthropic body: system at top level, messages only carries user (no system role in the list).
	assert.Equal(t, "claude-haiku-4-5", gotBody["model"])
	assert.Equal(t, "sys prompt", gotBody["system"])
	assert.Equal(t, float64(anthropicMaxTok), gotBody["max_tokens"])
	msgs, ok := gotBody["messages"].([]any)
	require.True(t, ok)
	require.Len(t, msgs, 1, "anthropic path must not inject a system-role message into the messages list")
	only := msgs[0].(map[string]any)
	assert.Equal(t, "user", only["role"])
	assert.Equal(t, "user prompt", only["content"])
}

func TestHTTPChatClient_Anthropic_NonTextBlockSkipped(t *testing.T) {
	// If the first content block is non-text (e.g. tool_use), the client
	// should skip it and return the next text block. Guards against empty
	// returns when Anthropic prepends non-text content.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[
			{"type":"tool_use","id":"x"},
			{"type":"text","text":"real answer"}
		]}`))
	}))
	defer srv.Close()

	c := NewHTTPChatClientWithProvider(ProviderAnthropic, srv.URL, "k", srv.Client())
	out, err := c.Chat(context.Background(), "m", "s", "u")
	require.NoError(t, err)
	assert.Equal(t, "real answer", out)
}

func TestHTTPChatClient_Anthropic_HTTPErrorSurfacesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	c := NewHTTPChatClientWithProvider(ProviderAnthropic, srv.URL, "bad", srv.Client())
	_, err := c.Chat(context.Background(), "m", "s", "u")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "authentication_error")
}

func TestHTTPChatClient_DefaultProviderIsOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If this were Anthropic, path would be /v1/messages; the default
		// must land on /v1/chat/completions.
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewHTTPChatClient(srv.URL, "", srv.Client())
	out, err := c.Chat(context.Background(), "any", "s", "u")
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
}
