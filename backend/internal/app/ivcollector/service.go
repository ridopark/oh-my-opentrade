package ivcollector

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

type Config struct {
	Symbols       []string
	TargetDTE     int // target days-to-expiration for ATM IV (default 30)
	RunAtHourET   int // hour in ET to run (default 16 = 4 PM)
	RunAtMinuteET int // minute in ET to run (default 15)
}

type Service struct {
	cfg         Config
	optionsData ports.OptionsMarketDataPort
	snapshots   ports.SnapshotPort
	ivRepo      ports.IVHistoryPort
	log         zerolog.Logger
	etLocation  *time.Location
}

func NewService(
	cfg Config,
	optionsData ports.OptionsMarketDataPort,
	snapshots ports.SnapshotPort,
	ivRepo ports.IVHistoryPort,
	log zerolog.Logger,
) *Service {
	if cfg.TargetDTE == 0 {
		cfg.TargetDTE = 30
	}
	if cfg.RunAtHourET == 0 {
		cfg.RunAtHourET = 16
	}
	if cfg.RunAtMinuteET == 0 {
		cfg.RunAtMinuteET = 15
	}
	et, _ := time.LoadLocation("America/New_York")
	return &Service{
		cfg:         cfg,
		optionsData: optionsData,
		snapshots:   snapshots,
		ivRepo:      ivRepo,
		log:         log,
		etLocation:  et,
	}
}

func (s *Service) Start(ctx context.Context) error {
	go s.loop(ctx)
	s.log.Info().
		Strs("symbols", s.cfg.Symbols).
		Int("target_dte", s.cfg.TargetDTE).
		Int("run_hour_et", s.cfg.RunAtHourET).
		Int("run_minute_et", s.cfg.RunAtMinuteET).
		Msg("IV collector started")
	return nil
}

func (s *Service) loop(ctx context.Context) {
	for {
		next := s.nextRunTime(time.Now())
		s.log.Debug().Time("next_run", next).Msg("IV collector scheduled")

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.collectAll(ctx)
		}
	}
}

func (s *Service) nextRunTime(now time.Time) time.Time {
	nowET := now.In(s.etLocation)
	target := time.Date(
		nowET.Year(), nowET.Month(), nowET.Day(),
		s.cfg.RunAtHourET, s.cfg.RunAtMinuteET, 0, 0,
		s.etLocation,
	)
	if nowET.After(target) {
		target = target.AddDate(0, 0, 1)
	}
	for target.Weekday() == time.Saturday || target.Weekday() == time.Sunday {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

func (s *Service) collectAll(ctx context.Context) {
	s.log.Info().Int("symbols", len(s.cfg.Symbols)).Msg("IV collection run starting")

	spotPrices, err := s.snapshots.GetSnapshots(ctx, s.cfg.Symbols, time.Now())
	if err != nil {
		s.log.Error().Err(err).Msg("failed to fetch spot prices for IV collection")
		return
	}

	targetExpiry := s.nearestFriday(time.Now(), s.cfg.TargetDTE)
	now := time.Now()

	var wg sync.WaitGroup
	var mu sync.Mutex
	collected := 0

	for _, sym := range s.cfg.Symbols {
		sym := sym
		wg.Add(1)
		go func() {
			defer wg.Done()

			snap, ok := spotPrices[sym]
			if !ok || snap.LastTradePrice == nil || *snap.LastTradePrice <= 0 {
				s.log.Warn().Str("symbol", sym).Msg("no spot price, skipping IV snapshot")
				return
			}
			spot := *snap.LastTradePrice

			ivSnap, err := s.collectSymbol(ctx, sym, spot, targetExpiry, now)
			if err != nil {
				s.log.Warn().Err(err).Str("symbol", sym).Msg("IV collection failed")
				return
			}

			if err := s.ivRepo.SaveIVSnapshot(ctx, ivSnap); err != nil {
				s.log.Error().Err(err).Str("symbol", sym).Msg("failed to save IV snapshot")
				return
			}

			mu.Lock()
			collected++
			mu.Unlock()

			s.log.Info().
				Str("symbol", sym).
				Float64("atm_iv", ivSnap.ATMIV).
				Float64("call_iv", ivSnap.CallIV).
				Float64("put_iv", ivSnap.PutIV).
				Float64("atm_strike", ivSnap.ATMStrike).
				Float64("spot", ivSnap.SpotPrice).
				Msg("IV snapshot collected")
		}()
	}

	wg.Wait()
	s.log.Info().
		Int("collected", collected).
		Int("total", len(s.cfg.Symbols)).
		Msg("IV collection run complete")
}

func (s *Service) collectSymbol(
	ctx context.Context,
	symbol string,
	spot float64,
	targetExpiry time.Time,
	now time.Time,
) (domain.IVSnapshot, error) {
	calls, err := s.optionsData.GetOptionChain(ctx, domain.Symbol(symbol), targetExpiry, domain.OptionRightCall)
	if err != nil {
		return domain.IVSnapshot{}, err
	}
	puts, err := s.optionsData.GetOptionChain(ctx, domain.Symbol(symbol), targetExpiry, domain.OptionRightPut)
	if err != nil {
		return domain.IVSnapshot{}, err
	}

	atmCall := findATMContract(calls, spot)
	atmPut := findATMContract(puts, spot)

	if atmCall == nil && atmPut == nil {
		return domain.IVSnapshot{}, errNoATMContracts
	}

	var callIV, putIV, atmStrike float64
	if atmCall != nil {
		callIV = atmCall.Greeks.IV
		atmStrike = atmCall.Strike
	}
	if atmPut != nil {
		putIV = atmPut.Greeks.IV
		if atmStrike == 0 {
			atmStrike = atmPut.Strike
		}
	}

	atmIV := averageNonZero(callIV, putIV)

	return domain.IVSnapshot{
		Time:      now,
		Symbol:    domain.Symbol(symbol),
		ATMIV:     atmIV,
		ATMStrike: atmStrike,
		SpotPrice: spot,
		CallIV:    callIV,
		PutIV:     putIV,
	}, nil
}

// nearestFriday finds the Friday closest to targetDTE days from now.
// Standard equity options expire on Fridays.
func (s *Service) nearestFriday(now time.Time, targetDTE int) time.Time {
	target := now.AddDate(0, 0, targetDTE)
	weekday := target.Weekday()
	switch weekday {
	case time.Saturday:
		target = target.AddDate(0, 0, -1)
	case time.Sunday:
		target = target.AddDate(0, 0, -2)
	case time.Monday:
		target = target.AddDate(0, 0, 4)
	case time.Tuesday:
		target = target.AddDate(0, 0, 3)
	case time.Wednesday:
		target = target.AddDate(0, 0, 2)
	case time.Thursday:
		target = target.AddDate(0, 0, 1)
	}
	return target
}

func findATMContract(chain []domain.OptionContractSnapshot, spot float64) *domain.OptionContractSnapshot {
	if len(chain) == 0 {
		return nil
	}
	sort.Slice(chain, func(i, j int) bool {
		return math.Abs(chain[i].Strike-spot) < math.Abs(chain[j].Strike-spot)
	})
	for i := range chain {
		if chain[i].Greeks.IV > 0 {
			return &chain[i]
		}
	}
	return nil
}

func averageNonZero(a, b float64) float64 {
	if a > 0 && b > 0 {
		return (a + b) / 2
	}
	if a > 0 {
		return a
	}
	return b
}

var errNoATMContracts = errString("no ATM contracts found with valid IV")

type errString string

func (e errString) Error() string { return string(e) }
