package alpaca

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCorporateActionsClient_DisabledWhenCredentialsMissing(t *testing.T) {
	c := NewCorporateActionsClient("https://example", "", "", zerolog.Nop())
	assert.Nil(t, c, "nil-return contract keeps composite chain simple")
}

func TestCorporateActionsClient_NilReceiverIsSafe(t *testing.T) {
	var c *CorporateActionsClient
	out, err := c.GetSplits(context.Background(), "AAPL", time.Now(), time.Now())
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestCorporateActionsClient_GetSplits_ParsesForwardSplit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/corporate_actions/announcements", r.URL.Path)
		assert.Equal(t, "split", r.URL.Query().Get("ca_types"))
		assert.Equal(t, "AAPL", r.URL.Query().Get("symbol"))
		assert.Equal(t, "test-key", r.Header.Get(headerAPIKey))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"ca_type":"split","ca_sub_type":"stock_split","initiating_symbol":"AAPL","target_symbol":"AAPL","effective_date":"2020-08-31","old_rate":"1","new_rate":"4","cash":"0"}
		]`))
	}))
	defer srv.Close()

	c := NewCorporateActionsClient(srv.URL, "test-key", "test-secret", zerolog.Nop())
	require.NotNil(t, c)

	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	out, err := c.GetSplits(context.Background(), "AAPL", from, to)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "split", out[0].ActionType)
	assert.Equal(t, 4.0, out[0].RatioNumerator)
	assert.Equal(t, 1.0, out[0].RatioDenominator)
	assert.Equal(t, "alpaca", out[0].Source)
	assert.Equal(t, 2020, out[0].EffectiveDate.Year())
}

func TestCorporateActionsClient_GetSplits_DetectsReverse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"ca_type":"split","ca_sub_type":"reverse_split","initiating_symbol":"ZZZZ","target_symbol":"ZZZZ","effective_date":"2022-01-10","old_rate":"5","new_rate":"1"}
		]`))
	}))
	defer srv.Close()

	c := NewCorporateActionsClient(srv.URL, "k", "s", zerolog.Nop())
	out, err := c.GetSplits(context.Background(), "ZZZZ", time.Now().AddDate(-3, 0, 0), time.Now())
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "reverse_split", out[0].ActionType)
	assert.Equal(t, 1.0, out[0].RatioNumerator)
	assert.Equal(t, 5.0, out[0].RatioDenominator)
}

func TestCorporateActionsClient_Non2xxReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewCorporateActionsClient(srv.URL, "k", "s", zerolog.Nop())
	out, err := c.GetSplits(context.Background(), "AAPL", time.Now().AddDate(-1, 0, 0), time.Now())
	require.NoError(t, err)
	assert.Empty(t, out)
}
