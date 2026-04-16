package onchain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDuneClient_MissingKey(t *testing.T) {
	_, err := NewDuneClient("", "", zerolog.Nop())
	assert.ErrorIs(t, err, ErrDuneNotConfigured)
}

func TestExecuteQuery_RequestBuilding(t *testing.T) {
	var capturedReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r.Clone(r.Context())
		json.NewEncoder(w).Encode(executeResponse{
			ExecutionID: "exec-123",
			State:       "QUERY_STATE_PENDING",
		})
	}))
	defer srv.Close()

	client := NewDuneClientWithHTTP("test-key", srv.URL+"/", srv.Client(), zerolog.Nop())

	execID, err := client.ExecuteQuery(context.Background(), 42, map[string]string{"window_hours": "24"})
	require.NoError(t, err)
	assert.Equal(t, "exec-123", execID)
	assert.Equal(t, "test-key", capturedReq.Header.Get("x-dune-api-key"))
	assert.Equal(t, http.MethodPost, capturedReq.Method)
	assert.Contains(t, capturedReq.URL.Path, "execution/42/execute")
}

func TestGetResults_PollUntilComplete(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n <= 2 {
			// First two calls: pending.
			json.NewEncoder(w).Encode(resultResponse{
				ExecutionID: "exec-456",
				State:       "QUERY_STATE_PENDING",
			})
			return
		}
		// Third call: complete with results.
		json.NewEncoder(w).Encode(resultResponse{
			ExecutionID: "exec-456",
			State:       "QUERY_STATE_COMPLETED",
			Result: &struct {
				Rows []map[string]any `json:"rows"`
			}{
				Rows: []map[string]any{
					{"from_address": "0xabc", "to_address": "0xdef", "amount_usd": 5000000.0},
				},
			},
		})
	}))
	defer srv.Close()

	client := NewDuneClientWithHTTP("test-key", srv.URL+"/", srv.Client(), zerolog.Nop())

	rows, err := client.GetResults(context.Background(), "exec-456")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "0xabc", rows[0]["from_address"])
	assert.GreaterOrEqual(t, int(callCount.Load()), 3)
}

func TestGetResults_QueryFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(resultResponse{
			ExecutionID: "exec-fail",
			State:       "QUERY_STATE_FAILED",
		})
	}))
	defer srv.Close()

	client := NewDuneClientWithHTTP("test-key", srv.URL+"/", srv.Client(), zerolog.Nop())

	_, err := client.GetResults(context.Background(), "exec-fail")
	assert.ErrorIs(t, err, ErrDuneQueryFailed)
}

func TestGetResults_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := NewDuneClientWithHTTP("test-key", srv.URL+"/", srv.Client(), zerolog.Nop())

	_, err := client.GetResults(context.Background(), "exec-rl")
	assert.ErrorIs(t, err, ErrDuneRateLimit)
}

func TestGetLatestResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "query/99/results")
		json.NewEncoder(w).Encode(latestResultResponse{
			ExecutionID: "exec-latest",
			State:       "QUERY_STATE_COMPLETED",
			Result: &struct {
				Rows []map[string]any `json:"rows"`
			}{
				Rows: []map[string]any{
					{"tx_hash": "0xfeed"},
				},
			},
		})
	}))
	defer srv.Close()

	client := NewDuneClientWithHTTP("test-key", srv.URL+"/", srv.Client(), zerolog.Nop())

	rows, err := client.GetLatestResults(context.Background(), 99)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "0xfeed", rows[0]["tx_hash"])
}

func TestGetLatestResults_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := NewDuneClientWithHTTP("test-key", srv.URL+"/", srv.Client(), zerolog.Nop())

	_, err := client.GetLatestResults(context.Background(), 99)
	assert.ErrorIs(t, err, ErrDuneRateLimit)
}
