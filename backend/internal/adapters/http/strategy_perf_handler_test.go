package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type testCtx struct {
	now    time.Time
	logger *slog.Logger
}

func (c *testCtx) Now() time.Time              { return c.now }
func (c *testCtx) Logger() *slog.Logger        { return c.logger }
func (c *testCtx) EmitDomainEvent(_ any) error { return nil }

func newTestCtx() *testCtx {
	return &testCtx{now: time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC), logger: slog.Default()}
}

type fakeState struct{ data string }

func (s *fakeState) Marshal() ([]byte, error) { return []byte(s.data), nil }
func (s *fakeState) Unmarshal(d []byte) error {
	s.data = string(d)
	return nil
}

type fakeStrategy struct{ meta strat.Meta }

func newFakeStrategy(id, version string) *fakeStrategy {
	sid, _ := strat.NewStrategyID(id)
	ver, _ := strat.NewVersion(version)
	return &fakeStrategy{meta: strat.Meta{ID: sid, Version: ver, Name: "Fake " + id, Author: "test"}}
}

func (f *fakeStrategy) Meta() strat.Meta { return f.meta }
func (f *fakeStrategy) WarmupBars() int  { return 0 }
func (f *fakeStrategy) Init(_ strat.Context, _ string, _ map[string]any, _ strat.State) (strat.State, error) {
	return &fakeState{data: `"init"`}, nil
}
func (f *fakeStrategy) OnBar(_ strat.Context, _ string, _ strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
	return st, nil, nil
}
func (f *fakeStrategy) OnEvent(_ strat.Context, _ string, _ any, st strat.State) (strat.State, []strat.Signal, error) {
	return st, nil, nil
}

type mockPnLRepo struct {
	ports.PnLPort

	dashFn    func(ctx context.Context, tenantID string, envMode domain.EnvMode, strategyID string, from, to time.Time) (domain.StrategyDashboard, error)
	signalsFn func(ctx context.Context, q ports.StrategySignalQuery) (ports.StrategySignalPage, error)

	lastDashboard struct {
		tenantID  string
		envMode   domain.EnvMode
		strategy  string
		from, to  time.Time
		callCount int
	}

	lastSignals struct {
		q         ports.StrategySignalQuery
		callCount int
	}
}

func (m *mockPnLRepo) GetStrategyDashboard(ctx context.Context, tenantID string, envMode domain.EnvMode, strategyID string, from, to time.Time) (domain.StrategyDashboard, error) {
	m.lastDashboard.tenantID = tenantID
	m.lastDashboard.envMode = envMode
	m.lastDashboard.strategy = strategyID
	m.lastDashboard.from = from
	m.lastDashboard.to = to
	m.lastDashboard.callCount++
	if m.dashFn == nil {
		return domain.StrategyDashboard{}, nil
	}
	return m.dashFn(ctx, tenantID, envMode, strategyID, from, to)
}

func (m *mockPnLRepo) GetStrategySignalEvents(ctx context.Context, q ports.StrategySignalQuery) (ports.StrategySignalPage, error) {
	m.lastSignals.q = q
	m.lastSignals.callCount++
	if m.signalsFn == nil {
		return ports.StrategySignalPage{}, nil
	}
	return m.signalsFn(ctx, q)
}

func newRunnerWithInstances(t *testing.T, instances ...*strategy.Instance) *strategy.Runner {
	t.Helper()

	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	runner := strategy.NewRunner(bus, router, "test-tenant", envMode, nil)
	for _, inst := range instances {
		router.Register(inst)
	}
	return runner
}

func newInstance(t *testing.T, strategyID, version string, symbols []string, priority int) *strategy.Instance {
	t.Helper()
	fs := newFakeStrategy(strategyID, version)

	iidRaw := strategyID + ":" + version + ":multi"
	if len(symbols) == 1 {
		iidRaw = strategyID + ":" + version + ":" + symbols[0]
	}
	iid, err := strat.NewInstanceID(iidRaw)
	if err != nil {
		t.Fatalf("instance id: %v", err)
	}

	inst := strategy.NewInstance(iid, fs, nil, strategy.InstanceAssignment{Symbols: symbols, Priority: priority}, strat.LifecycleLiveActive, nil)
	return inst
}

func TestStrategyPerfHandler_ListStrategies(t *testing.T) {
	inst1 := newInstance(t, "alpha_strat", "1.0.0", []string{"AAPL"}, 200)
	inst2 := newInstance(t, "beta_strat", "2.0.0", []string{"TSLA"}, 100)
	runner := newRunnerWithInstances(t, inst1, inst2)

	h := NewStrategyPerfHandler(runner, &mockPnLRepo{}, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/strategies/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var got []strategy.StrategyInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 strategies, got %d", len(got))
	}
}

func TestStrategyPerfHandler_Dashboard(t *testing.T) {
	pnl := &mockPnLRepo{}
	want := domain.StrategyDashboard{Strategy: "orb_break_retest"}
	pnl.dashFn = func(_ context.Context, tenantID string, envMode domain.EnvMode, strategyID string, from, to time.Time) (domain.StrategyDashboard, error) {
		if tenantID != "default" {
			t.Fatalf("tenantID=%q", tenantID)
		}
		if envMode != domain.EnvModePaper {
			t.Fatalf("envMode=%q", envMode)
		}
		if strategyID != want.Strategy {
			t.Fatalf("strategyID=%q", strategyID)
		}
		if from.IsZero() || to.IsZero() || !to.After(from) {
			t.Fatalf("invalid range from=%s to=%s", from, to)
		}
		return want, nil
	}

	h := NewStrategyPerfHandler(nil, pnl, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/strategies/orb_break_retest/dashboard?range=30d", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got domain.StrategyDashboard
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Strategy != want.Strategy {
		t.Fatalf("strategy=%q", got.Strategy)
	}
	if pnl.lastDashboard.callCount != 1 {
		t.Fatalf("expected GetStrategyDashboard called once")
	}
}

func TestStrategyPerfHandler_State(t *testing.T) {
	inst := newInstance(t, "test_strat", "1.0.0", []string{"AAPL", "MSFT"}, 100)
	ctx := newTestCtx()
	if err := inst.InitSymbol(ctx, "AAPL", nil); err != nil {
		t.Fatalf("init AAPL: %v", err)
	}
	if err := inst.InitSymbol(ctx, "MSFT", nil); err != nil {
		t.Fatalf("init MSFT: %v", err)
	}
	runner := newRunnerWithInstances(t, inst)
	h := NewStrategyPerfHandler(runner, &mockPnLRepo{}, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/strategies/test_strat/state", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var snaps []domain.StateSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snaps); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}
}

func TestStrategyPerfHandler_StateSingleSymbol(t *testing.T) {
	inst := newInstance(t, "test_strat", "1.0.0", []string{"AAPL", "MSFT"}, 100)
	ctx := newTestCtx()
	if err := inst.InitSymbol(ctx, "AAPL", nil); err != nil {
		t.Fatalf("init AAPL: %v", err)
	}
	runner := newRunnerWithInstances(t, inst)
	h := NewStrategyPerfHandler(runner, &mockPnLRepo{}, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/strategies/test_strat/state/AAPL", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var snap domain.StateSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if snap.Symbol != "AAPL" {
		t.Fatalf("symbol=%q", snap.Symbol)
	}
	if snap.Strategy != "test_strat" {
		t.Fatalf("strategy=%q", snap.Strategy)
	}
}

func TestStrategyPerfHandler_StateSingleSymbol_NotFound(t *testing.T) {
	inst := newInstance(t, "test_strat", "1.0.0", []string{"AAPL"}, 100)
	ctx := newTestCtx()
	if err := inst.InitSymbol(ctx, "AAPL", nil); err != nil {
		t.Fatalf("init AAPL: %v", err)
	}
	runner := newRunnerWithInstances(t, inst)
	h := NewStrategyPerfHandler(runner, &mockPnLRepo{}, zerolog.Nop())

	req := httptest.NewRequest(http.MethodGet, "/api/strategies/test_strat/state/UNKNOWN", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestStrategyPerfHandler_Signals(t *testing.T) {
	cursorTime := time.Date(2026, 3, 4, 12, 0, 0, 123, time.UTC)
	cursorRaw := cursorTime.Format(time.RFC3339Nano) + "|sig-1"
	cursor := base64.URLEncoding.EncodeToString([]byte(cursorRaw))

	pnl := &mockPnLRepo{}
	pnl.signalsFn = func(_ context.Context, q ports.StrategySignalQuery) (ports.StrategySignalPage, error) {
		if q.TenantID != "default" {
			t.Fatalf("tenantID=%q", q.TenantID)
		}
		if q.EnvMode != domain.EnvModePaper {
			t.Fatalf("envMode=%q", q.EnvMode)
		}
		if q.Strategy != "test_strat" {
			t.Fatalf("strategy=%q", q.Strategy)
		}
		if q.Symbol != "AAPL" {
			t.Fatalf("symbol=%q", q.Symbol)
		}
		if q.Limit != 50 {
			t.Fatalf("limit=%d", q.Limit)
		}
		if q.CursorTime == nil || !q.CursorTime.Equal(cursorTime) {
			t.Fatalf("cursorTime=%v", q.CursorTime)
		}
		if q.CursorID != "sig-1" {
			t.Fatalf("cursorID=%q", q.CursorID)
		}

		items := []domain.StrategySignalEvent{{
			TS:       time.Date(2026, 3, 4, 12, 1, 0, 0, time.UTC),
			TenantID: "default",
			EnvMode:  domain.EnvModePaper,
			Strategy: "test_strat",
			SignalID: "sig-2",
			Symbol:   "AAPL",
			Kind:     "entry",
			Side:     "BUY",
			Status:   domain.SignalStatusGenerated,
		}}
		return ports.StrategySignalPage{Items: items, NextCursor: "next"}, nil
	}

	h := NewStrategyPerfHandler(nil, pnl, zerolog.Nop())
	req := httptest.NewRequest(http.MethodGet, "/api/strategies/test_strat/signals?range=30d&limit=50&symbol=AAPL&cursor="+cursor, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items      []domain.StrategySignalEvent `json:"items"`
		NextCursor string                       `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items=%d", len(resp.Items))
	}
	if resp.NextCursor != "next" {
		t.Fatalf("next_cursor=%q", resp.NextCursor)
	}
}

func TestStrategyPerfHandler_CORS(t *testing.T) {
	h := NewStrategyPerfHandler(nil, &mockPnLRepo{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodOptions, "/api/strategies/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow-origin=%q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, OPTIONS" {
		t.Fatalf("allow-methods=%q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Fatalf("allow-headers=%q", got)
	}
}

func TestStrategyPerfHandler_MethodNotAllowed(t *testing.T) {
	h := NewStrategyPerfHandler(nil, &mockPnLRepo{}, zerolog.Nop())
	req := httptest.NewRequest(http.MethodPost, "/api/strategies/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
