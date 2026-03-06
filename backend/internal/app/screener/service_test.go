package screener

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	screenerdomain "github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type mockBus struct {
	published []domain.Event
	err       error
}

func (m *mockBus) Publish(_ context.Context, ev domain.Event) error {
	if m.err != nil {
		return m.err
	}
	m.published = append(m.published, ev)
	return nil
}

type mockSnapshots struct {
	data map[string]ports.Snapshot
	err  error
}

func (m *mockSnapshots) GetSnapshots(_ context.Context, symbols []string, _ time.Time) (map[string]ports.Snapshot, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := map[string]ports.Snapshot{}
	for _, s := range symbols {
		if v, ok := m.data[s]; ok {
			out[s] = v
		}
	}
	return out, nil
}

type mockMarketData struct {
	bars map[string][]domain.MarketBar
	err  error
}

func (m *mockMarketData) GetHistoricalBars(_ context.Context, symbol domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.bars[symbol.String()], nil
}

type mockRepo struct {
	saved []screenerdomain.ScreenerResult
	err   error
}

func (m *mockRepo) SaveResults(_ context.Context, results []screenerdomain.ScreenerResult) error {
	if m.err != nil {
		return m.err
	}
	m.saved = append(m.saved, results...)
	return nil
}

type mockNews struct {
	scores map[string]float64
	err    error
}

func (m *mockNews) Score(_ context.Context, symbols []string, _ time.Time) (map[string]float64, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := map[string]float64{}
	for _, s := range symbols {
		if v, ok := m.scores[s]; ok {
			out[s] = v
		}
	}
	return out, nil
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func TestRunScreen_HappyPath(t *testing.T) {
	ctx := context.Background()
	asOfET := time.Date(2026, time.March, 5, 8, 30, 0, 0, mustNY())

	bus := &mockBus{}
	snaps := &mockSnapshots{data: map[string]ports.Snapshot{
		"AAA": {Symbol: "AAA", PrevClose: f64(10), PreMarketPrice: f64(12), PreMarketVolume: i64(2000)},
		"BBB": {Symbol: "BBB", PrevClose: f64(20), PreMarketPrice: f64(21), PreMarketVolume: i64(4000)},
	}}
	md := &mockMarketData{bars: map[string][]domain.MarketBar{
		"AAA": {
			{Volume: 1000}, {Volume: 1000}, {Volume: 1000},
		},
		"BBB": {
			{Volume: 2000}, {Volume: 2000}, {Volume: 2000},
		},
	}}
	repo := &mockRepo{}

	svc, err := NewService(zerolog.Nop(), Config{Enabled: true, RVOLLookbackDays: 3, TopN: 2}, "default", "paper", []string{"AAA", "BBB"}, domain.AssetClassEquity, bus, snaps, md, repo, nil)
	if err != nil {
		t.Fatalf("NewService err: %v", err)
	}
	svc.SetNowFunc(func() time.Time { return time.Date(2026, time.March, 5, 12, 0, 0, 0, time.UTC) })

	if err := svc.RunScreen(ctx, asOfET); err != nil {
		t.Fatalf("RunScreen err: %v", err)
	}
	if len(repo.saved) != 2 {
		t.Fatalf("expected 2 saved results, got %d", len(repo.saved))
	}
	if len(bus.published) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(bus.published))
	}
	if bus.published[0].Type != domain.EventScreenerCompleted {
		t.Fatalf("expected EventScreenerCompleted, got %s", bus.published[0].Type)
	}
	payload, ok := bus.published[0].Payload.(screenerdomain.CompletedPayload)
	if !ok {
		t.Fatalf("expected CompletedPayload, got %T", bus.published[0].Payload)
	}
	if payload.Universe != 2 || payload.TopN != 2 {
		t.Fatalf("unexpected universe/topn: %d/%d", payload.Universe, payload.TopN)
	}
	if len(payload.Ranked) != 2 {
		t.Fatalf("expected ranked size 2, got %d", len(payload.Ranked))
	}
	if payload.Ranked[0].Symbol != "AAA" {
		t.Fatalf("expected AAA first, got %s", payload.Ranked[0].Symbol)
	}
	if payload.Ranked[1].Symbol != "BBB" {
		t.Fatalf("expected BBB second, got %s", payload.Ranked[1].Symbol)
	}
}

func TestRunScreen_MissingSnapshotStillSucceeds(t *testing.T) {
	ctx := context.Background()
	asOfET := time.Date(2026, time.March, 5, 8, 30, 0, 0, mustNY())

	bus := &mockBus{}
	snaps := &mockSnapshots{data: map[string]ports.Snapshot{
		"AAA": {Symbol: "AAA", PrevClose: f64(10), PreMarketPrice: f64(11), PreMarketVolume: i64(1000)},
	}}
	md := &mockMarketData{bars: map[string][]domain.MarketBar{
		"AAA": {{Volume: 1000}},
	}}
	repo := &mockRepo{}

	svc, err := NewService(zerolog.Nop(), Config{Enabled: true, RVOLLookbackDays: 1, TopN: 2}, "default", "paper", []string{"AAA", "MISSING"}, domain.AssetClassEquity, bus, snaps, md, repo, nil)
	if err != nil {
		t.Fatalf("NewService err: %v", err)
	}
	svc.SetNowFunc(func() time.Time { return time.Date(2026, time.March, 5, 12, 0, 0, 0, time.UTC) })

	if err := svc.RunScreen(ctx, asOfET); err != nil {
		t.Fatalf("RunScreen err: %v", err)
	}
	if len(repo.saved) != 2 {
		t.Fatalf("expected 2 saved results, got %d", len(repo.saved))
	}
	var missing screenerdomain.ScreenerResult
	for _, r := range repo.saved {
		if r.Symbol == "MISSING" {
			missing = r
		}
	}
	if missing.Symbol != "MISSING" {
		t.Fatalf("missing symbol result not found")
	}
	if missing.Status != screenerdomain.DataStatusMissingData {
		t.Fatalf("expected missing_data status, got %s", missing.Status)
	}
}

func TestRunScreen_WithNewsScoring(t *testing.T) {
	ctx := context.Background()
	asOfET := time.Date(2026, time.March, 5, 8, 30, 0, 0, mustNY())

	bus := &mockBus{}
	snaps := &mockSnapshots{data: map[string]ports.Snapshot{
		"AAA": {Symbol: "AAA", PrevClose: f64(10), PreMarketPrice: f64(11), PreMarketVolume: i64(1000)},
	}}
	md := &mockMarketData{bars: map[string][]domain.MarketBar{
		"AAA": {{Volume: 1000}},
	}}
	repo := &mockRepo{}
	news := &mockNews{scores: map[string]float64{"AAA": 0.8}}

	svc, err := NewService(zerolog.Nop(), Config{Enabled: true, RVOLLookbackDays: 1, TopN: 1, EnableNewsScoring: true, GapWeight: 1, RVOLWeight: 1, NewsWeight: 0.5}, "default", "paper", []string{"AAA"}, domain.AssetClassEquity, bus, snaps, md, repo, news)
	if err != nil {
		t.Fatalf("NewService err: %v", err)
	}
	svc.SetNowFunc(func() time.Time { return time.Date(2026, time.March, 5, 12, 0, 0, 0, time.UTC) })

	if err := svc.RunScreen(ctx, asOfET); err != nil {
		t.Fatalf("RunScreen err: %v", err)
	}
	if len(bus.published) != 1 {
		t.Fatalf("expected 1 published event")
	}
	payload := bus.published[0].Payload.(screenerdomain.CompletedPayload)
	if payload.Ranked[0].NewsScore == nil {
		t.Fatalf("expected news score")
	}
	if *payload.Ranked[0].NewsScore != 0.8 {
		t.Fatalf("expected news score 0.8 got %v", *payload.Ranked[0].NewsScore)
	}
	if payload.Ranked[0].TotalScore <= 0 {
		t.Fatalf("expected positive total score")
	}
}

func TestRunScreen_RankingSortedByTotalScoreDesc(t *testing.T) {
	ctx := context.Background()
	asOfET := time.Date(2026, time.March, 5, 8, 30, 0, 0, mustNY())

	bus := &mockBus{}
	snaps := &mockSnapshots{data: map[string]ports.Snapshot{
		"LOW":  {Symbol: "LOW", PrevClose: f64(10), PreMarketPrice: f64(10.1), PreMarketVolume: i64(1000)},
		"HIGH": {Symbol: "HIGH", PrevClose: f64(10), PreMarketPrice: f64(12), PreMarketVolume: i64(1000)},
	}}
	md := &mockMarketData{bars: map[string][]domain.MarketBar{
		"LOW":  {{Volume: 1000}},
		"HIGH": {{Volume: 1000}},
	}}
	repo := &mockRepo{}

	svc, err := NewService(zerolog.Nop(), Config{Enabled: true, RVOLLookbackDays: 1, TopN: 2}, "default", "paper", []string{"LOW", "HIGH"}, domain.AssetClassEquity, bus, snaps, md, repo, nil)
	if err != nil {
		t.Fatalf("NewService err: %v", err)
	}
	svc.SetNowFunc(func() time.Time { return time.Date(2026, time.March, 5, 12, 0, 0, 0, time.UTC) })

	if err := svc.RunScreen(ctx, asOfET); err != nil {
		t.Fatalf("RunScreen err: %v", err)
	}
	payload := bus.published[0].Payload.(screenerdomain.CompletedPayload)
	if payload.Ranked[0].Symbol != "HIGH" {
		t.Fatalf("expected HIGH first, got %s", payload.Ranked[0].Symbol)
	}
}

func TestAvgDailyVolume(t *testing.T) {
	ctx := context.Background()
	asOfET := time.Date(2026, time.March, 5, 8, 30, 0, 0, mustNY())

	bus := &mockBus{}
	snaps := &mockSnapshots{data: map[string]ports.Snapshot{}}
	md := &mockMarketData{bars: map[string][]domain.MarketBar{
		"AAA": {{Volume: 100}, {Volume: 200}, {Volume: 0}, {Volume: 300}},
	}}
	repo := &mockRepo{}

	svc, err := NewService(zerolog.Nop(), Config{Enabled: true, RVOLLookbackDays: 3, TopN: 1}, "default", "paper", []string{"AAA"}, domain.AssetClassEquity, bus, snaps, md, repo, nil)
	if err != nil {
		t.Fatalf("NewService err: %v", err)
	}

	avg, err := svc.avgDailyVolume(ctx, "AAA", asOfET, 2)
	if err != nil {
		t.Fatalf("avgDailyVolume err: %v", err)
	}
	if avg != 250 {
		t.Fatalf("expected avg 250, got %d", avg)
	}
}

func TestNextRunTimeET(t *testing.T) {
	loc := mustNY()
	now := time.Date(2026, time.March, 5, 8, 0, 0, 0, loc)
	next := nextRunTimeET(now, 8, 30)
	want := time.Date(2026, time.March, 5, 8, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("expected %v got %v", want, next)
	}

	now2 := time.Date(2026, time.March, 5, 9, 0, 0, 0, loc)
	next2 := nextRunTimeET(now2, 8, 30)
	want2 := time.Date(2026, time.March, 6, 8, 30, 0, 0, loc)
	if !next2.Equal(want2) {
		t.Fatalf("expected %v got %v", want2, next2)
	}
}

func TestIsNonTradingDay(t *testing.T) {
	loc := mustNY()
	sat := time.Date(2026, time.March, 7, 8, 30, 0, 0, loc)
	if !isNonTradingDay(sat) {
		t.Fatalf("expected Saturday to be non-trading")
	}
	mon := time.Date(2026, time.March, 9, 8, 30, 0, 0, loc)
	if isNonTradingDay(mon) {
		t.Fatalf("expected Monday to be trading")
	}
	holiday := time.Date(2026, time.January, 1, 8, 30, 0, 0, loc)
	if !isNonTradingDay(holiday) {
		t.Fatalf("expected NYSE holiday to be non-trading")
	}
}

func mustNY() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}
