package recap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -- fakes ------------------------------------------------------------

type fakeTradeFetcher struct {
	trades []domain.Trade
	err    error
}

func (f *fakeTradeFetcher) GetTrades(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.Trade, error) {
	return f.trades, f.err
}

type fakePnL struct {
	today    float64
	baseline []domain.DailyPnL
	err      error
}

func (f *fakePnL) GetDailyRealizedPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, date time.Time) (float64, error) {
	return f.today, f.err
}
func (f *fakePnL) GetDailyPnL(ctx context.Context, tenantID string, envMode domain.EnvMode, from, to time.Time) ([]domain.DailyPnL, error) {
	return f.baseline, f.err
}

type fakeSink struct {
	saved timescaledb.RecapDigest
	calls int
	err   error
}

func (f *fakeSink) Upsert(ctx context.Context, d timescaledb.RecapDigest) error {
	f.saved = d
	f.calls++
	return f.err
}

type fakeChat struct {
	lastSystem string
	lastUser   string
	lastModel  string
	reply      string
	err        error
}

func (f *fakeChat) Chat(ctx context.Context, model, system, user string) (string, error) {
	f.lastModel = model
	f.lastSystem = system
	f.lastUser = user
	return f.reply, f.err
}

type fakeNotifier struct {
	sent string
	err  error
}

func (n *fakeNotifier) Notify(ctx context.Context, tenantID string, msg string) error {
	n.sent = msg
	return n.err
}

// -- helpers ----------------------------------------------------------

func today() time.Time {
	return time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
}

func makeTrade(sym, side, strat string, qty, price float64) domain.Trade {
	return domain.Trade{
		Time:     today().Add(10 * time.Hour),
		Symbol:   domain.Symbol(sym),
		Side:     side,
		Quantity: qty,
		Price:    price,
		Strategy: strat,
	}
}

// -- tests ------------------------------------------------------------

func TestGenerateDigest_HappyPath(t *testing.T) {
	trades := &fakeTradeFetcher{
		trades: []domain.Trade{
			makeTrade("AAPL", "BUY", "macd_only_v1", 10, 180),
			makeTrade("AAPL", "SELL", "macd_only_v1", 10, 182),
			makeTrade("NVDA", "BUY", "avwap_v4", 5, 950),
		},
	}
	pnl := &fakePnL{
		today: 250.0,
		baseline: []domain.DailyPnL{
			{RealizedPnL: 100}, {RealizedPnL: 200}, {RealizedPnL: -50},
			{RealizedPnL: 300}, {RealizedPnL: 150},
		},
	}
	sink := &fakeSink{}
	chat := &fakeChat{reply: "  Today beat baseline by $110.\nBest: AAPL tight re-entry.\nWorst: none.\nFlag: NVDA overnight.  "}
	notifier := &fakeNotifier{}

	svc := NewService(Config{Model: "test-model"}, trades, pnl, sink, chat, notifier, zerolog.Nop())
	svc.SetClock(func() time.Time { return today().Add(17 * time.Hour) })

	d, err := svc.GenerateDigest(context.Background(), today())
	require.NoError(t, err)

	// Persisted row has trimmed body + correct metadata.
	assert.Equal(t, 1, sink.calls)
	assert.Equal(t, "Today beat baseline by $110.\nBest: AAPL tight re-entry.\nWorst: none.\nFlag: NVDA overnight.", d.Body)
	assert.Equal(t, 3, d.TradesCovered)
	assert.Equal(t, 250.0, d.NetPnLToday)
	assert.Equal(t, PromptVersion, d.PromptVersion)
	assert.Equal(t, "test-model", d.Model)
	assert.Equal(t, "default", d.TenantID)
	assert.Equal(t, string(domain.EnvModePaper), d.EnvMode)

	// Prompt includes today's PnL, baseline, and fills rollup with both strategies.
	assert.Contains(t, chat.lastUser, "$250.00")
	assert.Contains(t, chat.lastUser, "$140.00") // baseline avg = (100+200-50+300+150)/5 = 140
	assert.Contains(t, chat.lastUser, "macd_only_v1")
	assert.Contains(t, chat.lastUser, "avwap_v4")
	assert.Contains(t, chat.lastUser, "AAPL")
	assert.Contains(t, chat.lastUser, "NVDA")

	// Discord header + body delivered.
	assert.Contains(t, notifier.sent, "EOD Recap 2026-04-17")
	assert.Contains(t, notifier.sent, "net $250.00")
	assert.Contains(t, notifier.sent, "3 fills")
	assert.Contains(t, notifier.sent, "Today beat baseline")
}

func TestGenerateDigest_NoFills(t *testing.T) {
	trades := &fakeTradeFetcher{trades: nil}
	pnl := &fakePnL{today: 0, baseline: nil}
	sink := &fakeSink{}
	chat := &fakeChat{reply: "No trades today."}
	notifier := &fakeNotifier{}

	svc := NewService(Config{Model: "m"}, trades, pnl, sink, chat, notifier, zerolog.Nop())
	d, err := svc.GenerateDigest(context.Background(), today())
	require.NoError(t, err)
	assert.Equal(t, 0, d.TradesCovered)
	assert.Equal(t, "No trades today.", d.Body)
	assert.Equal(t, 1, sink.calls, "digest row written even on no-trade day for audit continuity")
	assert.Contains(t, notifier.sent, "0 fills")
}

func TestGenerateDigest_ChatError_Propagates(t *testing.T) {
	trades := &fakeTradeFetcher{trades: []domain.Trade{makeTrade("AAPL", "BUY", "s", 1, 100)}}
	pnl := &fakePnL{}
	sink := &fakeSink{}
	chat := &fakeChat{err: errors.New("rate limited")}

	svc := NewService(Config{}, trades, pnl, sink, chat, nil, zerolog.Nop())
	_, err := svc.GenerateDigest(context.Background(), today())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chat")
	assert.Equal(t, 0, sink.calls, "no digest persisted when LLM call fails")
}

func TestGenerateDigest_NotifierFailure_NonFatal(t *testing.T) {
	trades := &fakeTradeFetcher{trades: []domain.Trade{makeTrade("AAPL", "BUY", "s", 1, 100)}}
	pnl := &fakePnL{today: 5}
	sink := &fakeSink{}
	chat := &fakeChat{reply: "done"}
	notifier := &fakeNotifier{err: errors.New("discord 500")}

	svc := NewService(Config{}, trades, pnl, sink, chat, notifier, zerolog.Nop())
	d, err := svc.GenerateDigest(context.Background(), today())
	require.NoError(t, err, "notify failure must not fail digest generation -- row is still persisted")
	assert.Equal(t, 1, sink.calls)
	assert.Equal(t, "done", d.Body)
}

func TestSummarizeFills_OptionNotionalUsesPremium(t *testing.T) {
	trades := []domain.Trade{
		{Symbol: "AAPL", Side: "BUY", Quantity: 2, Price: 1.0,
			Strategy: "macd_only_v1", InstrumentType: domain.InstrumentTypeOption, Premium: 3.25},
		{Symbol: "AAPL", Side: "SELL", Quantity: 2, Price: 1.0,
			Strategy: "macd_only_v1", InstrumentType: domain.InstrumentTypeOption, Premium: 5.00},
	}
	r := summarizeFills(trades)
	require.Len(t, r.ByStrategy, 1)
	// 2 * 3.25 * 100 + 2 * 5.00 * 100 = 650 + 1000 = 1650
	assert.InDelta(t, 1650.0, r.ByStrategy[0].Notional, 1e-6)
	require.Len(t, r.BySymbol, 1)
	assert.Equal(t, 1, r.BySymbol[0].Buys)
	assert.Equal(t, 1, r.BySymbol[0].Sells)
}

func TestSummarizeFills_UnlabeledStrategyBucket(t *testing.T) {
	trades := []domain.Trade{{Symbol: "AAPL", Side: "BUY", Quantity: 1, Price: 100}}
	r := summarizeFills(trades)
	require.Len(t, r.ByStrategy, 1)
	assert.Equal(t, "(unlabeled)", r.ByStrategy[0].Strategy)
}

func TestScheduledService_NextRunSkipsWeekend(t *testing.T) {
	// Friday 2026-04-17 18:00 ET -- next run should be Monday 2026-04-20.
	et, _ := time.LoadLocation("America/New_York")
	friPostClose := time.Date(2026, 4, 17, 18, 0, 0, 0, et)
	s := NewScheduledService(ScheduledConfig{RunAtHourET: 17, RunAtMinuteET: 15}, nil, zerolog.Nop())
	next := s.nextRunTime(friPostClose)
	assert.Equal(t, time.Monday, next.Weekday(), "weekend must be skipped; got %s", next.Weekday())
	assert.Equal(t, 17, next.In(et).Hour())
	assert.Equal(t, 15, next.In(et).Minute())
}

func TestScheduledService_NextRunSameDay(t *testing.T) {
	et, _ := time.LoadLocation("America/New_York")
	thuMorning := time.Date(2026, 4, 16, 9, 30, 0, 0, et)
	s := NewScheduledService(ScheduledConfig{RunAtHourET: 17, RunAtMinuteET: 15}, nil, zerolog.Nop())
	next := s.nextRunTime(thuMorning)
	assert.Equal(t, time.Thursday, next.Weekday())
	// Must be the same calendar day's 17:15 ET.
	assert.Equal(t, 17, next.In(et).Hour())
	assert.True(t, strings.HasPrefix(next.In(et).Format("2006-01-02"), "2026-04-16"))
}
