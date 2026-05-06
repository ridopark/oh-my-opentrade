package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/oh-my-opentrade/backend/internal/app/backtest"
)

func TestChainSourceTags(t *testing.T) {
	tests := []struct {
		name string
		res  *backtest.Result
		want []string
	}{
		{
			name: "nil_result",
			res:  nil,
			want: []string{},
		},
		{
			name: "nil_chain_stats",
			res:  &backtest.Result{ChainStats: nil},
			want: []string{},
		},
		{
			name: "live_hits_zero",
			res: &backtest.Result{
				ChainStats: &backtest.ChainSourceStats{HistHits: 5, LiveHits: 0, SynthHits: 2},
			},
			want: []string{},
		},
		{
			name: "live_hits_positive",
			res: &backtest.Result{
				ChainStats: &backtest.ChainSourceStats{HistHits: 1, LiveHits: 3, SynthHits: 0},
			},
			want: []string{"chain_source=live_now"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chainSourceTags(tt.res)
			if len(got) != len(tt.want) {
				t.Fatalf("len=%d want=%d (got=%v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("tag[%d]=%q want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestBacktestHandler_PreferLiveChain_NoLivePort_Returns400 verifies the
// fail-fast at the request-parse boundary: when prefer_live_chain=true but
// the handler has no live options market wired (infra.alpacaData was nil at
// construction), the request is rejected with 400 BEFORE any runner is built
// or any DB call is made.
func TestBacktestHandler_PreferLiveChain_NoLivePort_Returns400(t *testing.T) {
	// Construct the handler directly without NewBacktestHandler so we avoid
	// the worker goroutine. The 400 fires before any field beyond liveOptions
	// is read.
	h := &BacktestHandler{
		log:         zerolog.Nop(),
		liveOptions: nil,
	}

	body := map[string]any{
		"prefer_live_chain": true,
		"symbols":           []string{"SPY"},
		"from":              "2026-05-04",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/backtest/run", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	h.handleRun(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, "prefer_live_chain") {
		t.Fatalf("body missing 'prefer_live_chain': %s", got)
	}
	if !strings.Contains(got, "not configured") {
		t.Fatalf("body missing 'not configured': %s", got)
	}
}

// TestResolvePreferLiveChain covers the resolution matrix: pointer-typed
// request field overrides server default, server default tracks whether
// liveOptions is wired.
func TestResolvePreferLiveChain(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name           string
		reqField       *bool
		hasLiveOptions bool
		want           bool
	}{
		{"absent_with_live_wired_defaults_on", nil, true, true},
		{"absent_without_live_wired_defaults_off", nil, false, false},
		{"explicit_true_with_live_wired", &yes, true, true},
		{"explicit_true_without_live_wired", &yes, false, true}, // 400 fires later in handler
		{"explicit_false_with_live_wired", &no, true, false},
		{"explicit_false_without_live_wired", &no, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePreferLiveChain(tc.reqField, tc.hasLiveOptions)
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

