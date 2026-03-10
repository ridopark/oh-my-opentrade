package orchestrator

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/perf"
	"github.com/oh-my-opentrade/backend/internal/app/risk"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Inline mocks
// ---------------------------------------------------------------------------

// mockEventBus implements ports.EventBusPort with a no-op Subscribe.
type mockEventBus struct{}

func (m *mockEventBus) Publish(_ context.Context, _ domain.Event) error { return nil }
func (m *mockEventBus) Subscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}
func (m *mockEventBus) SubscribeAsync(_ context.Context, et domain.EventType, h ports.EventHandler) error {
	return m.Subscribe(context.Background(), et, h)
}
func (m *mockEventBus) Unsubscribe(_ context.Context, _ domain.EventType, _ ports.EventHandler) error {
	return nil
}
func (m *mockEventBus) Close() {}

// mockBroker implements ports.BrokerPort (needed for execution.NewService and perf.NewLedgerWriter).
type mockBroker struct{}

func (m *mockBroker) SubmitOrder(_ context.Context, _ domain.OrderIntent) (string, error) {
	return "mock-order-id", nil
}
func (m *mockBroker) CancelOrder(_ context.Context, _ string) error { return nil }
func (m *mockBroker) GetOrderStatus(_ context.Context, _ string) (string, error) {
	return "filled", nil
}
func (m *mockBroker) GetPositions(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (m *mockBroker) CancelOpenOrders(_ context.Context, _ domain.Symbol, _ string) (int, error) {
	return 0, nil
}

// mockRepo implements ports.RepositoryPort (needed for execution.NewService).
type mockRepo struct{}

func (m *mockRepo) SaveMarketBar(_ context.Context, _ domain.MarketBar) error { return nil }
func (m *mockRepo) GetMarketBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (m *mockRepo) SaveTrade(_ context.Context, _ domain.Trade) error { return nil }
func (m *mockRepo) GetTrades(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (m *mockRepo) SaveStrategyDNA(_ context.Context, _ domain.StrategyDNA) error { return nil }
func (m *mockRepo) GetLatestStrategyDNA(_ context.Context, _ string, _ domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (m *mockRepo) SaveOrder(_ context.Context, _ domain.BrokerOrder) error { return nil }
func (m *mockRepo) UpdateOrderFill(_ context.Context, _ string, _ time.Time, _, _ float64) error {
	return nil
}
func (m *mockRepo) ListTrades(_ context.Context, _ ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (m *mockRepo) ListOrders(_ context.Context, _ ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (m *mockRepo) SaveThoughtLog(_ context.Context, _ domain.ThoughtLog) error { return nil }
func (m *mockRepo) GetThoughtLogsByIntentID(_ context.Context, _ string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (m *mockRepo) UpdateTradeThesis(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ json.RawMessage) error {
	return nil
}
func (m *mockRepo) GetMaxBarHighSince(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time) (float64, error) {
	return 0, nil
}

// mockPnLRepo implements ports.PnLPort (needed for perf.NewLedgerWriter).
type mockPnLRepo struct{}

func (m *mockPnLRepo) UpsertDailyPnL(_ context.Context, _ domain.DailyPnL) error { return nil }
func (m *mockPnLRepo) GetDailyPnL(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.DailyPnL, error) {
	return nil, nil
}
func (m *mockPnLRepo) SaveEquityPoint(_ context.Context, _ domain.EquityPoint) error { return nil }
func (m *mockPnLRepo) GetEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.EquityPoint, error) {
	return nil, nil
}
func (m *mockPnLRepo) GetDailyRealizedPnL(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) (float64, error) {
	return 0, nil
}
func (m *mockPnLRepo) GetBucketedEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time, _ string) ([]domain.EquityPoint, error) {
	return nil, nil
}
func (m *mockPnLRepo) GetMaxDrawdown(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (float64, error) {
	return 0, nil
}
func (m *mockPnLRepo) GetSharpe(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (*float64, error) {
	return nil, nil
}
func (m *mockPnLRepo) GetSortino(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) (*float64, error) {
	return nil, nil
}
func (m *mockPnLRepo) UpsertStrategyDailyPnL(_ context.Context, _ domain.StrategyDailyPnL) error {
	return nil
}
func (m *mockPnLRepo) GetStrategyDailyPnL(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.StrategyDailyPnL, error) {
	return nil, nil
}
func (m *mockPnLRepo) SaveStrategyEquityPoint(_ context.Context, _ domain.StrategyEquityPoint) error {
	return nil
}
func (m *mockPnLRepo) GetStrategyEquityCurve(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) ([]domain.StrategyEquityPoint, error) {
	return nil, nil
}
func (m *mockPnLRepo) SaveStrategySignalEvent(_ context.Context, _ domain.StrategySignalEvent) error {
	return nil
}
func (m *mockPnLRepo) GetStrategySignalEvents(_ context.Context, _ ports.StrategySignalQuery) (ports.StrategySignalPage, error) {
	return ports.StrategySignalPage{}, nil
}
func (m *mockPnLRepo) GetStrategyDashboard(_ context.Context, _ string, _ domain.EnvMode, _ string, _, _ time.Time) (domain.StrategyDashboard, error) {
	return domain.StrategyDashboard{}, nil
}

// mockEquitySource implements EquitySource with configurable return value.
type mockEquitySource struct {
	equity float64
	calls  atomic.Int64
}

func (m *mockEquitySource) GetAccountEquity(_ context.Context) (float64, error) {
	m.calls.Add(1)
	return m.equity, nil
}

// mockClosable implements Closable.
type mockClosable struct {
	closed atomic.Bool
}

func (m *mockClosable) Close() error {
	m.closed.Store(true)
	return nil
}

// mockDailyPnLSource implements risk.DailyPnLSource (needed for DailyLossBreaker).
type mockDailyPnLSource struct{}

func (m *mockDailyPnLSource) GetDailyRealizedPnL(_ string, _ domain.EnvMode) float64 { return 0 }

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestShared() SharedDeps {
	bus := &mockEventBus{}
	return SharedDeps{
		EventBus:   bus,
		Repo:       &mockRepo{},
		PnLRepo:    &mockPnLRepo{},
		MarketData: nil, // shared market data — not used in orchestrator tests
		SpecStore:  nil, // not used directly by orchestrator
		Metrics:    nil,
		Log:        zerolog.Nop(),
	}
}

// newTestHandle creates a valid AccountHandle with real execution.Service and
// perf.LedgerWriter instances backed by mock dependencies. Both services'
// Start() methods only call eventBus.Subscribe() — the mock returns nil.
func newTestHandle(tenantID string, bus ports.EventBusPort) *AccountHandle {
	broker := &mockBroker{}
	repo := &mockRepo{}
	pnlRepo := &mockPnLRepo{}
	log := zerolog.Nop()

	posGate := execution.NewPositionGate(broker, log)
	execSvc := execution.NewService(
		bus, broker, repo,
		execution.NewRiskEngine(0.02),
		execution.NewSlippageGuard(nil),
		execution.NewKillSwitch(3, 5*time.Minute, 15*time.Minute, time.Now),
		risk.NewDailyLossBreaker(2.0, 1000, &mockDailyPnLSource{}, time.Now, log),
		100000.0,
		log,
		execution.WithPositionGate(posGate),
	)

	lw := perf.NewLedgerWriter(bus, pnlRepo, broker, nil, 100000.0, log)

	return &AccountHandle{
		TenantID:     tenantID,
		Label:        tenantID + "-label",
		EnvMode:      domain.EnvModePaper,
		Equity:       &mockEquitySource{equity: 100000},
		Close:        &mockClosable{},
		Execution:    execSvc,
		LedgerWriter: lw,
		DailyLossBreaker: risk.NewDailyLossBreaker(
			2.0, 1000, &mockDailyPnLSource{}, time.Now, log,
		),
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	shared := newTestShared()
	o := New(shared)

	if o == nil {
		t.Fatal("New returned nil")
	}
	if o.refreshEvery != 5*time.Minute {
		t.Errorf("expected default refreshEvery 5m, got %v", o.refreshEvery)
	}
	if o.accounts == nil {
		t.Error("accounts map should be initialized")
	}
	if len(o.accounts) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(o.accounts))
	}
	if o.started.Load() {
		t.Error("started should be false on new orchestrator")
	}
	if o.globalHlt.Load() {
		t.Error("globalHlt should be false on new orchestrator")
	}
}

func TestAdd_NilHandle(t *testing.T) {
	o := New(newTestShared())
	err := o.Add(nil)
	if err == nil {
		t.Fatal("expected error for nil handle")
	}
	if err.Error() != "orchestrator: account handle is nil" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestAdd_EmptyTenantID(t *testing.T) {
	o := New(newTestShared())
	h := &AccountHandle{TenantID: ""}
	err := o.Add(h)
	if err == nil {
		t.Fatal("expected error for empty tenant_id")
	}
	if err.Error() != "orchestrator: account handle missing tenant_id" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestAdd_DuplicateTenantID(t *testing.T) {
	o := New(newTestShared())
	h1 := &AccountHandle{TenantID: "acct-1", Label: "Account 1"}
	h2 := &AccountHandle{TenantID: "acct-1", Label: "Account 1 Dup"}

	if err := o.Add(h1); err != nil {
		t.Fatalf("first Add failed: %v", err)
	}
	err := o.Add(h2)
	if err == nil {
		t.Fatal("expected error for duplicate tenant_id")
	}
	expected := `orchestrator: duplicate tenant_id "acct-1"`
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestAdd_Success(t *testing.T) {
	o := New(newTestShared())
	h := &AccountHandle{TenantID: "acct-1", Label: "Primary"}

	if err := o.Add(h); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	accounts := o.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if accounts[0].TenantID != "acct-1" {
		t.Errorf("expected tenant_id 'acct-1', got %q", accounts[0].TenantID)
	}
}

func TestOrchestrator_StartStop(t *testing.T) {
	shared := newTestShared()
	o := New(shared)

	h := newTestHandle("primary", shared.EventBus)
	if err := o.Add(h); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	ctx := context.Background()
	if err := o.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !o.started.Load() {
		t.Error("started should be true after Start")
	}

	// Start should be idempotent — second call returns nil without error.
	if err := o.Start(ctx); err != nil {
		t.Fatalf("second Start should be no-op, got: %v", err)
	}

	o.Stop()

	// After Stop, the handle's cancel should have been called.
	// The closable mock should be closed.
	cl := h.Close.(*mockClosable)
	if !cl.closed.Load() {
		t.Error("expected Close to be called on account handle after Stop")
	}
}

func TestOrchestrator_StartRequiresServices(t *testing.T) {
	shared := newTestShared()
	o := New(shared)

	// Handle without Execution and LedgerWriter should fail on Start.
	h := &AccountHandle{
		TenantID: "missing-services",
		Label:    "Missing",
		EnvMode:  domain.EnvModePaper,
	}
	if err := o.Add(h); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	err := o.Start(context.Background())
	if err == nil {
		t.Fatal("expected error when starting without required services")
	}
	expected := `orchestrator: tenant "missing-services" missing required services`
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestOrchestrator_GlobalHalt(t *testing.T) {
	o := New(newTestShared())

	if o.IsGloballyHalted() {
		t.Error("should not be halted initially")
	}

	o.GlobalHalt()

	if !o.IsGloballyHalted() {
		t.Error("should be halted after GlobalHalt()")
	}
}

func TestOrchestrator_AccountIsolation(t *testing.T) {
	shared := newTestShared()
	o := New(shared)

	h1 := newTestHandle("acct-alpha", shared.EventBus)
	h2 := newTestHandle("acct-beta", shared.EventBus)

	if err := o.Add(h1); err != nil {
		t.Fatalf("Add h1 failed: %v", err)
	}
	if err := o.Add(h2); err != nil {
		t.Fatalf("Add h2 failed: %v", err)
	}

	accounts := o.Accounts()
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}

	tenants := make(map[string]bool)
	for _, a := range accounts {
		tenants[a.TenantID] = true
	}
	if !tenants["acct-alpha"] {
		t.Error("missing acct-alpha")
	}
	if !tenants["acct-beta"] {
		t.Error("missing acct-beta")
	}

	// Start both and verify independent lifecycle.
	ctx := context.Background()
	if err := o.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	o.Stop()

	// Both closable mocks should be closed.
	for _, a := range []*AccountHandle{h1, h2} {
		cl := a.Close.(*mockClosable)
		if !cl.closed.Load() {
			t.Errorf("expected Close called for tenant %q", a.TenantID)
		}
	}
}

func TestOrchestrator_EquityRefresh(t *testing.T) {
	shared := newTestShared()
	o := New(shared)
	o.refreshEvery = 10 * time.Millisecond // fast ticking for test

	eqSrc := &mockEquitySource{equity: 150000}
	h := newTestHandle("refresh-acct", shared.EventBus)
	h.Equity = eqSrc

	if err := o.Add(h); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	ctx := context.Background()
	if err := o.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait long enough for several ticks (initial call + at least 2 ticker fires).
	time.Sleep(80 * time.Millisecond)

	calls := eqSrc.calls.Load()
	if calls < 2 {
		t.Errorf("expected >= 2 equity refresh calls, got %d", calls)
	}

	o.Stop()

	// After stop, the refresh goroutine should exit. Record calls and wait.
	callsAfterStop := eqSrc.calls.Load()
	time.Sleep(30 * time.Millisecond)
	callsLater := eqSrc.calls.Load()
	if callsLater > callsAfterStop+1 {
		t.Errorf("equity refresh should stop after Stop(); calls went from %d to %d", callsAfterStop, callsLater)
	}
}

func TestOrchestrator_MultiAccountStart(t *testing.T) {
	shared := newTestShared()
	o := New(shared)

	// Add 3 accounts and start all.
	for _, id := range []string{"a", "b", "c"} {
		h := newTestHandle(id, shared.EventBus)
		if err := o.Add(h); err != nil {
			t.Fatalf("Add %q failed: %v", id, err)
		}
	}

	if err := o.Start(context.Background()); err != nil {
		t.Fatalf("Start with 3 accounts failed: %v", err)
	}

	accounts := o.Accounts()
	if len(accounts) != 3 {
		t.Errorf("expected 3 accounts, got %d", len(accounts))
	}

	o.Stop()
}
