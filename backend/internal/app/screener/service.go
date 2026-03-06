package screener

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	screenerdomain "github.com/oh-my-opentrade/backend/internal/domain/screener"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type Bus interface {
	Publish(ctx context.Context, ev domain.Event) error
}

type MarketDataProvider interface {
	GetHistoricalBars(ctx context.Context, symbol domain.Symbol, timeframe domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error)
}

type Config struct {
	Enabled           bool
	RunAtHourET       int
	RunAtMinuteET     int
	RVOLLookbackDays  int
	TopN              int
	GapWeight         float64
	RVOLWeight        float64
	NewsWeight        float64
	EnableNewsScoring bool
}

type Service struct {
	log        zerolog.Logger
	cfg        Config
	tenantID   string
	envMode    string
	symbols    []string
	assetClass domain.AssetClass

	bus       Bus
	snapshots ports.SnapshotPort
	market    MarketDataProvider
	repo      ports.ScreenerRepoPort
	news      ports.NewsScorerPort

	now func() time.Time
}

func NewService(
	log zerolog.Logger,
	cfg Config,
	tenantID string,
	envMode string,
	symbols []string,
	assetClass domain.AssetClass,
	bus Bus,
	snapshots ports.SnapshotPort,
	marketData MarketDataProvider,
	repo ports.ScreenerRepoPort,
	news ports.NewsScorerPort,
) (*Service, error) {
	if tenantID == "" {
		return nil, errors.New("tenantID is required")
	}
	if envMode == "" {
		return nil, errors.New("envMode is required")
	}
	if bus == nil {
		return nil, errors.New("bus is required")
	}
	if snapshots == nil {
		return nil, errors.New("snapshots port is required")
	}
	if marketData == nil {
		return nil, errors.New("market data provider is required")
	}
	if repo == nil {
		return nil, errors.New("repo is required")
	}

	if cfg.RunAtHourET == 0 {
		cfg.RunAtHourET = 8
	}
	if cfg.RunAtMinuteET == 0 {
		cfg.RunAtMinuteET = 30
	}
	if cfg.RVOLLookbackDays == 0 {
		cfg.RVOLLookbackDays = 20
	}
	if cfg.TopN == 0 {
		cfg.TopN = 50
	}
	if cfg.GapWeight == 0 {
		cfg.GapWeight = 1.0
	}
	if cfg.RVOLWeight == 0 {
		cfg.RVOLWeight = 1.0
	}
	if cfg.NewsWeight == 0 {
		cfg.NewsWeight = 0.5
	}
	if cfg.EnableNewsScoring && news == nil {
		return nil, errors.New("news scoring enabled but news scorer is nil")
	}

	return &Service{
		log:        log,
		cfg:        cfg,
		tenantID:   tenantID,
		envMode:    envMode,
		symbols:    append([]string(nil), symbols...),
		assetClass: assetClass,
		bus:        bus,
		snapshots:  snapshots,
		market:     marketData,
		repo:       repo,
		news:       news,
		now:        time.Now,
	}, nil
}

func (s *Service) SetNowFunc(now func() time.Time) {
	if now == nil {
		s.now = time.Now
		return
	}
	s.now = now
}

func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.Enabled {
		s.log.Info().Msg("screener disabled")
		return nil
	}
	go s.schedulerLoop(ctx)
	return nil
}

func (s *Service) schedulerLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		next := nextRunTimeET(s.now(), s.cfg.RunAtHourET, s.cfg.RunAtMinuteET)
		if !s.assetClass.Is24x7() && isNonTradingDay(next) {
			next = nextRunTimeET(next.Add(24*time.Hour), s.cfg.RunAtHourET, s.cfg.RunAtMinuteET)
		}
		sleep := time.Until(next)
		if sleep > 0 {
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}

		if !s.assetClass.Is24x7() && isNonTradingDay(next) {
			continue
		}

		if err := s.RunScreen(ctx, next); err != nil {
			s.log.Error().Err(err).Msg("screener run failed")
		}
	}
}

func isNonTradingDay(t time.Time) bool {
	wd := t.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return true
	}
	if domain.IsNYSEHoliday(t) {
		return true
	}
	return false
}

func nextRunTimeET(now time.Time, hour, minute int) time.Time {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.Local
	}
	nowET := now.In(loc)
	run := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), hour, minute, 0, 0, loc)
	if !nowET.Before(run) {
		run = run.Add(24 * time.Hour)
	}
	return run
}

func (s *Service) RunScreen(ctx context.Context, asOfET time.Time) error {
	if len(s.symbols) == 0 {
		return nil
	}

	runID := uuid.NewString()

	snaps, err := s.snapshots.GetSnapshots(ctx, s.symbols, asOfET)
	if err != nil {
		return fmt.Errorf("screener: get snapshots: %w", err)
	}

	var newsScores map[string]float64
	if s.cfg.EnableNewsScoring {
		scores, err := s.news.Score(ctx, s.symbols, asOfET)
		if err != nil {
			return fmt.Errorf("screener: news scoring: %w", err)
		}
		newsScores = scores
	} else {
		newsScores = map[string]float64{}
	}

	results := make([]screenerdomain.ScreenerResult, 0, len(s.symbols))
	ranked := make([]screenerdomain.RankedSymbol, 0, len(s.symbols))
	nowUTC := s.now().UTC()

	for _, sym := range s.symbols {
		snap, ok := snaps[sym]
		res := screenerdomain.ScreenerResult{
			TenantID:    s.tenantID,
			EnvMode:     s.envMode,
			RunID:       runID,
			AsOf:        asOfET.UTC(),
			Symbol:      sym,
			Status:      screenerdomain.DataStatusOK,
			CreatedAt:   nowUTC,
			PrevClose:   nil,
			GapPct:      nil,
			RVOL:        nil,
			ErrorMsg:    nil,
			Score:       screenerdomain.ScreenerScore{},
			PriceSource: nil,
		}

		if !ok {
			res.Status = screenerdomain.DataStatusMissingData
			results = append(results, res)
			ranked = append(ranked, screenerdomain.RankedSymbol{Symbol: sym, TotalScore: 0})
			continue
		}

		res.PrevClose = snap.PrevClose
		res.PreMarketPrice = snap.PreMarketPrice
		res.PreMarketVolume = snap.PreMarketVolume

		price := snap.PreMarketPrice
		ps := screenerdomain.PriceSourcePreMarket
		if s.assetClass.Is24x7() || price == nil {
			price = snap.LastTradePrice
			ps = screenerdomain.PriceSourceLastTrade
		}
		if price != nil {
			res.PriceSource = &ps
		}

		if res.PrevClose == nil || price == nil {
			res.Status = screenerdomain.DataStatusMissingData
			results = append(results, res)
			ranked = append(ranked, screenerdomain.RankedSymbol{Symbol: sym, TotalScore: 0})
			continue
		}

		gap := (*price - *res.PrevClose) / *res.PrevClose * 100.0
		res.GapPct = &gap

		avgVol, err := s.avgDailyVolume(ctx, sym, asOfET, s.cfg.RVOLLookbackDays)
		if err != nil {
			msg := err.Error()
			res.Status = screenerdomain.DataStatusError
			res.ErrorMsg = &msg
			results = append(results, res)
			ranked = append(ranked, screenerdomain.RankedSymbol{Symbol: sym, GapPct: res.GapPct, TotalScore: 0})
			continue
		}
		res.AvgHistVolume = &avgVol

		var rvolVal *float64
		if res.PreMarketVolume != nil && avgVol > 0 {
			v := float64(*res.PreMarketVolume) / float64(avgVol)
			rvolVal = &v
			res.RVOL = rvolVal
		}

		gapScore := screenerdomain.NormalizeGap(gap)
		rvolScore := 0.0
		if rvolVal != nil {
			rvolScore = screenerdomain.NormalizeRVOL(*rvolVal)
		}

		var newsScorePtr *float64
		if s.cfg.EnableNewsScoring {
			if ns, ok := newsScores[sym]; ok {
				nsv := ns
				newsScorePtr = &nsv
			}
		}

		total := s.cfg.GapWeight*gapScore + s.cfg.RVOLWeight*rvolScore
		if newsScorePtr != nil {
			total += s.cfg.NewsWeight * (*newsScorePtr)
		}

		res.Score = screenerdomain.ScreenerScore{
			GapScore:  gapScore,
			RVOLScore: rvolScore,
			NewsScore: newsScorePtr,
			Total:     total,
		}

		results = append(results, res)
		ranked = append(ranked, screenerdomain.RankedSymbol{
			Symbol:     sym,
			GapPct:     res.GapPct,
			RVOL:       res.RVOL,
			NewsScore:  newsScorePtr,
			TotalScore: total,
		})
	}

	if err := s.repo.SaveResults(ctx, results); err != nil {
		return fmt.Errorf("screener: save results: %w", err)
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].TotalScore == ranked[j].TotalScore {
			return ranked[i].Symbol < ranked[j].Symbol
		}
		return ranked[i].TotalScore > ranked[j].TotalScore
	})

	topN := s.cfg.TopN
	if topN > len(ranked) {
		topN = len(ranked)
	}
	rankedTop := ranked[:topN]

	payload := screenerdomain.CompletedPayload{
		RunID:    runID,
		AsOf:     asOfET.UTC(),
		Universe: len(s.symbols),
		TopN:     topN,
		Ranked:   rankedTop,
	}
	ev, err := domain.NewEvent(domain.EventScreenerCompleted, s.tenantID, domain.EnvMode(s.envMode), runID+"-screener-completed", payload)
	if err != nil {
		return fmt.Errorf("screener: new event: %w", err)
	}
	if err := s.bus.Publish(ctx, *ev); err != nil {
		return fmt.Errorf("screener: publish completed: %w", err)
	}
	return nil
}

func (s *Service) avgDailyVolume(ctx context.Context, symbol string, asOfET time.Time, lookback int) (int64, error) {
	if lookback <= 0 {
		return 0, errors.New("lookback must be > 0")
	}
	sym, err := domain.NewSymbol(symbol)
	if err != nil {
		return 0, err
	}
	tf, _ := domain.NewTimeframe("1d")

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.Local
	}
	end := asOfET.In(loc)
	start := end.AddDate(0, 0, -(lookback * 3))

	bars, err := s.market.GetHistoricalBars(ctx, sym, tf, start, end)
	if err != nil {
		return 0, err
	}
	if len(bars) == 0 {
		return 0, errors.New("no historical bars")
	}

	vols := make([]float64, 0, len(bars))
	for _, b := range bars {
		if b.Volume > 0 {
			vols = append(vols, b.Volume)
		}
	}
	if len(vols) == 0 {
		return 0, errors.New("no volume bars")
	}
	if len(vols) > lookback {
		vols = vols[len(vols)-lookback:]
	}

	sum := 0.0
	for _, v := range vols {
		sum += v
	}
	avg := sum / float64(len(vols))
	return int64(avg), nil
}
