package tradier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

func TestNewClient_EmptyTokenReturnsNil(t *testing.T) {
	if c := NewClient(Config{}, zerolog.Nop()); c != nil {
		t.Fatal("expected nil client when token is empty")
	}
}

func TestExpirations_ParsesResponse(t *testing.T) {
	payload := `{"expirations":{"date":["2025-05-02","2025-05-09","2025-06-20"]}}`
	srv := testServer(t, "/markets/options/expirations", payload)
	defer srv.Close()

	c := NewClient(Config{Token: "test", BaseURL: srv.URL}, zerolog.Nop())
	dates, err := c.Expirations(context.Background(), "SOFI")
	if err != nil {
		t.Fatal(err)
	}
	if len(dates) != 3 {
		t.Fatalf("expected 3 dates, got %d", len(dates))
	}
	if dates[0].Format("2006-01-02") != "2025-05-02" {
		t.Errorf("first date wrong: %s", dates[0])
	}
}

func TestChainSnapshot_ArrayShape(t *testing.T) {
	payload := `{"options":{"option":[
	  {"symbol":"SOFI250620C00009000","underlying":"SOFI","strike":9.0,"option_type":"call","bid":1.23,"ask":1.27,"greeks":{"delta":0.45,"gamma":0.08,"theta":-0.02,"vega":0.015,"rho":0.003,"mid_iv":0.62}},
	  {"symbol":"SOFI250620P00009000","underlying":"SOFI","strike":9.0,"option_type":"put","bid":0.34,"ask":0.38,"greeks":{"delta":-0.55,"gamma":0.08,"theta":-0.02,"vega":0.014,"rho":-0.003,"mid_iv":0.60}}
	]}}`
	srv := testServer(t, "/markets/options/chains", payload)
	defer srv.Close()

	c := NewClient(Config{Token: "test", BaseURL: srv.URL}, zerolog.Nop())
	rows, err := c.ChainSnapshot(context.Background(), "SOFI",
		time.Date(2025, 6, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	c0 := rows[0]
	if c0.Symbol != domain.Symbol("SOFI") {
		t.Errorf("underlying: got %s", c0.Symbol)
	}
	if c0.Strike != 9.0 {
		t.Errorf("strike: got %f", c0.Strike)
	}
	if c0.Right != domain.OptionRightCall {
		t.Errorf("right: got %s", c0.Right)
	}
	if c0.Bid != 1.23 || c0.Ask != 1.27 {
		t.Errorf("bid/ask: got %f/%f", c0.Bid, c0.Ask)
	}
	if c0.Delta != 0.45 || c0.IV != 0.62 {
		t.Errorf("greeks: delta=%f iv=%f", c0.Delta, c0.IV)
	}
	if rows[1].Right != domain.OptionRightPut {
		t.Errorf("put right: got %s", rows[1].Right)
	}
}

func TestChainSnapshot_SingleShape(t *testing.T) {
	// Tradier returns a bare object (not an array) when only one contract matches.
	payload := `{"options":{"option":{"symbol":"X","underlying":"X","strike":1,"option_type":"call","bid":0.01,"ask":0.02,"greeks":{"delta":0.01,"mid_iv":1.5}}}}`
	srv := testServer(t, "/markets/options/chains", payload)
	defer srv.Close()

	c := NewClient(Config{Token: "t", BaseURL: srv.URL}, zerolog.Nop())
	rows, err := c.ChainSnapshot(context.Background(), "X", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

func TestChainSnapshot_NullShape(t *testing.T) {
	payload := `{"options":null}`
	srv := testServer(t, "/markets/options/chains", payload)
	defer srv.Close()

	c := NewClient(Config{Token: "t", BaseURL: srv.URL}, zerolog.Nop())
	rows, err := c.ChainSnapshot(context.Background(), "X", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("expected zero rows, got %d", len(rows))
	}
}

func TestGet_NonOKStatusBubblesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"fault":"bad"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(Config{Token: "t", BaseURL: srv.URL}, zerolog.Nop())
	_, err := c.Expirations(context.Background(), "X")
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

// testServer returns an httptest.Server that validates the Authorization
// header, asserts the path prefix, and returns payload on GET.
func testServer(t *testing.T, wantPathPrefix, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test" && got != "Bearer t" {
			t.Errorf("auth header: got %q", got)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("accept header: got %q", r.Header.Get("Accept"))
		}
		if r.URL.Path != wantPathPrefix {
			t.Errorf("path: got %q want prefix %q", r.URL.Path, wantPathPrefix)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(json.RawMessage(payload))
	}))
}
