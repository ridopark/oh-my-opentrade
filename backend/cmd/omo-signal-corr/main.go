// omo-signal-corr: one-shot analysis tool that measures overlap between the
// existing inducement detector and a minimum-viable "price-volume structure
// shift reversal factor". Writes per-symbol CSVs and prints summary metrics.
// Does not write to DB and does not touch live strategy paths.
package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

// Inducement defaults mirror configs/strategies/avwap_v4.toml.
const (
	indSwingN         = 3
	indSwingDepth     = 8
	indMaxAgeBars     = 60
	indBreachMinBPS   = 5.0
	indBreachMaxBPS   = 80.0
	indReversalBars   = 3
	indVolumeMinRatio = 1.2
	volumeSMAWindow   = 20
)

// Reversal-factor defaults from quant-analyst brief.
const (
	revVolShiftMax    = -0.5 // volume must DROP by this much z-score
	todBucketSessions = 20   // rolling sessions per HH:MM bucket
	fastVolWindow     = 3    // climax window (bars t-2..t)
	slowVolWindow     = 5    // baseline window (bars t-7..t-3)
	absorptionWindow  = 3    // bars for range/|ret| ratio
	sessionSigmaMinN  = 6    // min bars (30min of 5m) before stretch is valid
)

// Runtime-tunable knobs set via flags at main() entry.
var (
	revStretchZMin  = 1.5 // lower bound on |stretch_z|
	revStretchZMax  = 3.5 // upper cap — edge test showed Q5 (|z|>3.0) is noise
	indSuppressBars = 3   // ±N bars around an inducement fire to mark as ind-adjacent for edge tests
	edgeFromTime    time.Time
	edgeToTime      time.Time
)

// inEdgeWindow returns true if the row's time falls in the edge-test window.
// Zero times on both bounds disable the filter (include everything).
func inEdgeWindow(t time.Time) bool {
	if !edgeFromTime.IsZero() && t.Before(edgeFromTime) {
		return false
	}
	if !edgeToTime.IsZero() && !t.Before(edgeToTime) {
		return false
	}
	return true
}

type sampleRow struct {
	Time          time.Time
	Close         float64
	IndFired      bool
	IndDirection  int // +1 long-fade, -1 short-fade, 0 none
	IndScore      int // signed: +pos if long, -pos if short
	RevFired      bool
	RevDirection  int
	RevSignedF    float64 // continuous signed magnitude
	StretchZ      float64
	VolShift      float64
	Absorption    float64
	MACDCross     bool // true if MACD histogram changed sign on this bar
	MACDDirection int  // +1 bullish cross (long), -1 bearish cross (short)
	FwdR1         float64 // log(close[t+1]/close[t]) — 5m forward
	FwdR3         float64 // 15m forward
	FwdR6         float64 // 30m forward
	FwdR12        float64 // 60m forward
	HaveR1        bool
	HaveR3        bool
	HaveR6        bool
	HaveR12       bool
	IndNearby     bool // any inducement fire within ±indSuppressBars bars (same session)
}

// macdState tracks the 12/26/9 MACD incremental computation.
// Seed pattern mirrors backend/internal/app/monitor/indicators.go:
// EMAs seed from SMA(period) of closes, signal line seeds from first MACD
// value when at least `macdSignalPeriod` MACD values have been observed.
type macdState struct {
	closes       []float64 // retained until EMAs are seeded
	ema12        float64
	ema26        float64
	macdSig      float64
	ema12Init    bool
	ema26Init    bool
	macdSigInit  bool
	macdCount    int
	prevHistSet  bool
	prevHistSign int // -1, 0, +1 for sign(hist) on previous bar with defined hist
}

const (
	macdFast   = 12
	macdSlow   = 26
	macdSignal = 9
)

// updateMACD feeds a new close and returns (line, signal, crossed, direction).
// crossed is true only when prev bar's histogram sign differs from current
// bar's histogram sign. direction is +1 if cross moved into positive hist
// (MACD crossed above signal → bullish), -1 if negative (bearish), 0 if no cross.
func (m *macdState) updateMACD(close float64) (float64, float64, bool, int) {
	if !m.ema12Init || !m.ema26Init {
		m.closes = append(m.closes, close)
	}
	if !m.ema12Init && len(m.closes) >= macdFast {
		sum := 0.0
		for _, c := range m.closes[len(m.closes)-macdFast:] {
			sum += c
		}
		m.ema12 = sum / float64(macdFast)
		m.ema12Init = true
	} else if m.ema12Init {
		mult := 2.0 / (float64(macdFast) + 1.0)
		m.ema12 = (close-m.ema12)*mult + m.ema12
	}
	if !m.ema26Init && len(m.closes) >= macdSlow {
		sum := 0.0
		for _, c := range m.closes[len(m.closes)-macdSlow:] {
			sum += c
		}
		m.ema26 = sum / float64(macdSlow)
		m.ema26Init = true
		// Free retained closes once both EMAs are seeded.
		m.closes = nil
	} else if m.ema26Init {
		mult := 2.0 / (float64(macdSlow) + 1.0)
		m.ema26 = (close-m.ema26)*mult + m.ema26
	}
	if !m.ema12Init || !m.ema26Init {
		return 0, 0, false, 0
	}
	line := m.ema12 - m.ema26
	m.macdCount++
	if !m.macdSigInit && m.macdCount >= macdSignal {
		m.macdSig = line
		m.macdSigInit = true
	} else if m.macdSigInit {
		mult := 2.0 / (float64(macdSignal) + 1.0)
		m.macdSig = (line-m.macdSig)*mult + m.macdSig
	}
	if !m.macdSigInit {
		return line, 0, false, 0
	}
	hist := line - m.macdSig
	sign := 0
	if hist > 0 {
		sign = 1
	} else if hist < 0 {
		sign = -1
	}
	crossed := false
	dir := 0
	if m.prevHistSet && sign != 0 && m.prevHistSign != 0 && sign != m.prevHistSign {
		crossed = true
		dir = sign
	}
	if sign != 0 {
		m.prevHistSign = sign
		m.prevHistSet = true
	}
	return line, m.macdSig, crossed, dir
}

type symbolStats struct {
	Symbol             string
	Bars               int
	IndFires           int
	RevFires           int
	BothFire           int
	SameDirectionBoth  int
	OppositeDirBoth    int
	CondLift           float64 // P(rev|ind) / P(rev)
	Jaccard            float64
	DirAgreement       float64
	SpearmanAll        float64 // across all bars (scores)
	PearsonCoFire      float64 // among co-fires, signed scores
	PRevGivenInd       float64
	PRev               float64
}

func main() {
	var (
		symbolsFlag   string
		fromFlag      string
		toFlag        string
		timeframeFlag string
		configPath    string
		envPath       string
		outDirFlag    string
	)

	flag.StringVar(&symbolsFlag, "symbols", "SPY,QQQ,AAPL,MSFT,NVDA,AMD,TSLA,META", "Comma-separated symbols")
	flag.StringVar(&fromFlag, "from", "2025-06-01", "Start date YYYY-MM-DD")
	flag.StringVar(&toFlag, "to", "2026-04-01", "End date YYYY-MM-DD")
	flag.StringVar(&timeframeFlag, "timeframe", "1m", "Source bar timeframe (will be aggregated to 5m)")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config file")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.StringVar(&outDirFlag, "outdir", "_tmp/signal_corr", "Output directory for per-symbol CSVs")
	flag.Float64Var(&revStretchZMin, "stretch-z-min", revStretchZMin, "Lower bound on |stretch_z| for reversal factor fires")
	flag.Float64Var(&revStretchZMax, "stretch-z-max", revStretchZMax, "Upper cap on |stretch_z| for reversal factor fires (set to 100 to disable)")
	flag.IntVar(&indSuppressBars, "ind-suppress-bars", indSuppressBars, "±N bar window around inducement fires to tag as ind-adjacent for edge tests")
	var edgeFromFlag, edgeToFlag string
	flag.StringVar(&edgeFromFlag, "edge-from", "", "Restrict edge-test window to rows on/after this date (YYYY-MM-DD); bars before still load for warmup")
	flag.StringVar(&edgeToFlag, "edge-to", "", "Restrict edge-test window to rows before this date (YYYY-MM-DD)")
	flag.Parse()

	log := logger.New(logger.Config{
		Level:  zerolog.InfoLevel,
		Pretty: os.Getenv("LOG_PRETTY") == "true",
	}).With().Str("service", "omo-signal-corr").Logger()

	if edgeFromFlag != "" {
		t, err := time.Parse("2006-01-02", edgeFromFlag)
		if err != nil {
			log.Fatal().Err(err).Msg("invalid --edge-from")
		}
		edgeFromTime = t
	}
	if edgeToFlag != "" {
		t, err := time.Parse("2006-01-02", edgeToFlag)
		if err != nil {
			log.Fatal().Err(err).Msg("invalid --edge-to")
		}
		edgeToTime = t
	}

	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	fromTime, err := time.Parse("2006-01-02", fromFlag)
	if err != nil {
		log.Fatal().Err(err).Str("from", fromFlag).Msg("invalid --from")
	}
	toTime, err := time.Parse("2006-01-02", toFlag)
	if err != nil {
		log.Fatal().Err(err).Str("to", toFlag).Msg("invalid --to")
	}

	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse DB config")
	}
	sqlDB := stdlib.OpenDB(*pgxCfg)
	if err := sqlDB.PingContext(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to TimescaleDB")
	}
	defer sqlDB.Close()

	repo := timescaledb.NewRepositoryWithLogger(timescaledb.NewSqlDB(sqlDB), log.With().Str("component", "timescaledb").Logger())

	if err := os.MkdirAll(outDirFlag, 0o755); err != nil {
		log.Fatal().Err(err).Str("dir", outDirFlag).Msg("failed to create outdir")
	}

	nyLoc, err := time.LoadLocation("America/New_York")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load NY timezone")
	}

	symbols := strings.Split(symbolsFlag, ",")
	ctx := context.Background()
	tf := domain.Timeframe(timeframeFlag)

	var allStats []symbolStats
	var universeRows []sampleRow

	for _, symStr := range symbols {
		symStr = strings.TrimSpace(symStr)
		if symStr == "" {
			continue
		}
		symLog := log.With().Str("symbol", symStr).Logger()

		sym := domain.Symbol(symStr)
		srcBars, err := repo.GetMarketBars(ctx, sym, tf, fromTime, toTime.Add(24*time.Hour))
		if err != nil {
			symLog.Error().Err(err).Msg("failed to load bars")
			continue
		}
		if len(srcBars) < 100 {
			symLog.Warn().Int("bars", len(srcBars)).Msg("too few bars, skipping")
			continue
		}
		bars := aggregateTo5m(srcBars, nyLoc)
		symLog.Info().Int("src_bars", len(srcBars)).Int("agg_bars_5m", len(bars)).Msg("aggregated")

		rows := processSymbol(symStr, bars, nyLoc)
		stats := computeStats(symStr, rows)
		allStats = append(allStats, stats)
		universeRows = append(universeRows, rows...)

		csvPath := filepath.Join(outDirFlag, symStr+".csv")
		if err := writeCSV(csvPath, rows); err != nil {
			symLog.Error().Err(err).Msg("failed to write csv")
		}
		symLog.Info().
			Int("bars_rth", stats.Bars).
			Int("ind_fires", stats.IndFires).
			Int("rev_fires", stats.RevFires).
			Int("co_fires", stats.BothFire).
			Float64("jaccard", stats.Jaccard).
			Float64("dir_agree", stats.DirAgreement).
			Float64("spearman", stats.SpearmanAll).
			Float64("cond_lift", stats.CondLift).
			Str("csv", csvPath).
			Msg("symbol done")
	}

	universeStats := computeStats("UNIVERSE", universeRows)
	printSummary(allStats, universeStats)
	decision(universeStats)
	printEdgeTable(universeRows)
	printStretchZQuintiles(universeRows)
	printMACDEdgeTable(universeRows)
}

// processSymbol walks bars in order, maintaining all per-bar state, and emits
// one sampleRow per RTH bar. Non-RTH bars are skipped entirely: the reversal
// factor is not defined outside the session (no session VWAP anchor), and the
// inducement detector is co-configured by AVWAP which itself trades RTH only.
func processSymbol(symbol string, bars []domain.MarketBar, nyLoc *time.Location) []sampleRow {
	// Per-session state.
	var (
		sessionDate       string
		sessionVWAPNum    float64 // cumulative typical-price * volume
		sessionVWAPDen    float64 // cumulative volume
		sessionReturns    []float64
		sessionSigma      float64
		lastClose         float64
		// Volume SMA (20-bar rolling).
		volWindow    []float64
		// Time-of-day bucket rolling volumes: key = "HH:MM" ET.
		todBuckets = make(map[string][]float64)
		// Recent bucketed vol z-scores for vol_shift calc.
		bucketedZHist []float64
		// Recent bars for absorption calc.
		absHist []domain.MarketBar
		// Swing / inducement state.
		swingDet           = start.NewSwingDetector(indSwingN, "5m")
		recentSwingHighs   []start.SwingLevel
		recentSwingLows    []start.SwingLevel
		pendingInducement  *start.PendingInducement
		macd               macdState
	)

	_ = symbol // reserved for future per-symbol calibration
	indCfg := start.InducementConfig{
		BreachMinBPS:   indBreachMinBPS,
		BreachMaxBPS:   indBreachMaxBPS,
		ReversalBars:   indReversalBars,
		VolumeMinRatio: indVolumeMinRatio,
		MaxAgeBars:     indMaxAgeBars,
	}

	out := make([]sampleRow, 0, len(bars)/4)

	for _, mb := range bars {
		etTime := mb.Time.In(nyLoc)
		// RTH filter: 09:30 <= t < 16:00 ET, weekdays.
		if etTime.Weekday() == time.Saturday || etTime.Weekday() == time.Sunday {
			continue
		}
		minutes := etTime.Hour()*60 + etTime.Minute()
		if minutes < 9*60+30 || minutes >= 16*60 {
			continue
		}

		barDate := etTime.Format("2006-01-02")
		if barDate != sessionDate {
			// New session: reset session VWAP and sigma.
			sessionDate = barDate
			sessionVWAPNum = 0
			sessionVWAPDen = 0
			sessionReturns = sessionReturns[:0]
			sessionSigma = 0
			lastClose = 0
			// Absorption state is NOT reset across sessions — it's a 3-bar
			// rolling window and the first few bars of a new session will just
			// reflect the opening. That's fine for this analysis.
		}

		bar := start.Bar{
			Time:   mb.Time,
			Open:   mb.Open,
			High:   mb.High,
			Low:    mb.Low,
			Close:  mb.Close,
			Volume: mb.Volume,
		}

		// ── Volume SMA ───────────────────────────────────────────────────
		volWindow = append(volWindow, bar.Volume)
		if len(volWindow) > volumeSMAWindow {
			volWindow = volWindow[len(volWindow)-volumeSMAWindow:]
		}
		volumeSMA := mean(volWindow)

		// ── Inducement detection ────────────────────────────────────────
		for i := range recentSwingHighs {
			recentSwingHighs[i].BarAge++
		}
		for i := range recentSwingLows {
			recentSwingLows[i].BarAge++
		}
		for _, ca := range swingDet.Push(bar) {
			lvl := start.SwingLevel{
				Time:   ca.Time,
				Price:  ca.Price,
				BarAge: 0,
			}
			switch ca.Type {
			case start.AnchorSwingHigh:
				lvl.Price = ca.Price
				lvl.Side = start.InducementSwingHigh
				recentSwingHighs = appendSwingLevel(recentSwingHighs, lvl, indSwingDepth)
			case start.AnchorSwingLow:
				lvl.Side = start.InducementSwingLow
				recentSwingLows = appendSwingLevel(recentSwingLows, lvl, indSwingDepth)
			}
		}
		recentSwingHighs = pruneStale(recentSwingHighs, indMaxAgeBars)
		recentSwingLows = pruneStale(recentSwingLows, indMaxAgeBars)

		sig, pending := start.DetectInducement(
			bar, recentSwingHighs, recentSwingLows,
			pendingInducement, indCfg, volumeSMA,
		)
		pendingInducement = pending

		// ── Session VWAP & sigma ────────────────────────────────────────
		typical := (bar.High + bar.Low + bar.Close) / 3.0
		sessionVWAPNum += typical * bar.Volume
		sessionVWAPDen += bar.Volume
		sessionVWAP := 0.0
		if sessionVWAPDen > 0 {
			sessionVWAP = sessionVWAPNum / sessionVWAPDen
		}
		if lastClose > 0 {
			r := math.Log(bar.Close / lastClose)
			sessionReturns = append(sessionReturns, r)
			if len(sessionReturns) >= sessionSigmaMinN {
				sessionSigma = stdev(sessionReturns) * bar.Close // scale to price units (approx)
			}
		}
		lastClose = bar.Close

		// ── Stretch z-score ─────────────────────────────────────────────
		stretchZ := 0.0
		stretchReady := false
		if sessionSigma > 0 && sessionVWAP > 0 {
			stretchZ = (bar.Close - sessionVWAP) / sessionSigma
			stretchReady = true
		}

		// ── Time-of-day bucketed volume z-score ─────────────────────────
		bucketKey := etTime.Format("15:04")
		hist := todBuckets[bucketKey]
		bucketedZ := 0.0
		bucketReady := false
		if len(hist) >= 10 {
			med := medianOf(hist)
			if med > 0 {
				ratio := bar.Volume / med
				// Z-score the log-ratio across the bucket's history of ratios.
				// Cheaper: z-score the log(ratio) assuming bucket history has mean 0 in log space.
				// We approximate with a rolling stdev of log(vol / bucketMed) across hist.
				logRatios := make([]float64, len(hist))
				for i, v := range hist {
					if v > 0 && med > 0 {
						logRatios[i] = math.Log(v / med)
					}
				}
				sd := stdev(logRatios)
				if sd > 0 {
					bucketedZ = math.Log(ratio) / sd
					bucketReady = true
				}
			}
		}
		// Append AFTER computing the z-score so current bar doesn't self-bias.
		todBuckets[bucketKey] = append(hist, bar.Volume)
		if len(todBuckets[bucketKey]) > todBucketSessions {
			todBuckets[bucketKey] = todBuckets[bucketKey][len(todBuckets[bucketKey])-todBucketSessions:]
		}

		// ── Vol shift (climax-then-fade) ────────────────────────────────
		if bucketReady {
			bucketedZHist = append(bucketedZHist, bucketedZ)
			if len(bucketedZHist) > fastVolWindow+slowVolWindow {
				bucketedZHist = bucketedZHist[len(bucketedZHist)-(fastVolWindow+slowVolWindow):]
			}
		}
		volShift := 0.0
		volShiftReady := false
		if len(bucketedZHist) >= fastVolWindow+slowVolWindow {
			fast := bucketedZHist[len(bucketedZHist)-fastVolWindow:]
			slow := bucketedZHist[len(bucketedZHist)-(fastVolWindow+slowVolWindow) : len(bucketedZHist)-fastVolWindow]
			volShift = mean(fast) - mean(slow)
			volShiftReady = true
		}

		// ── Absorption ───────────────────────────────────────────────────
		absHist = append(absHist, mb)
		if len(absHist) > absorptionWindow {
			absHist = absHist[len(absHist)-absorptionWindow:]
		}
		absorption := 0.0
		absReady := false
		if len(absHist) >= absorptionWindow {
			var sumAbsRet, totalRange float64
			for i := 1; i < len(absHist); i++ {
				if absHist[i-1].Close > 0 {
					sumAbsRet += math.Abs(math.Log(absHist[i].Close / absHist[i-1].Close))
				}
				totalRange += math.Log(math.Max(absHist[i].High, 1e-9) / math.Max(absHist[i].Low, 1e-9))
			}
			if sumAbsRet > 0 {
				absorption = totalRange / sumAbsRet
				absReady = true
			}
		}

		// ── Reversal factor ─────────────────────────────────────────────
		revFired := false
		revDir := 0
		revSigned := 0.0
		if stretchReady && volShiftReady && absReady &&
			math.Abs(stretchZ) > revStretchZMin && math.Abs(stretchZ) <= revStretchZMax &&
			volShift < revVolShiftMax {
			revFired = true
			if stretchZ > 0 {
				revDir = -1 // fade long stretch: short
			} else {
				revDir = 1
			}
			mag := math.Tanh(math.Abs(stretchZ)/2.0) * math.Abs(volShift) * absorption
			revSigned = float64(revDir) * mag
		}

		// ── Inducement outputs ──────────────────────────────────────────
		indFired := false
		indDir := 0
		indScore := 0
		if sig != nil {
			indFired = true
			if sig.Direction == start.SideBuy {
				indDir = 1
			} else {
				indDir = -1
			}
			indScore = sig.Score * indDir
		}

		// ── MACD (12/26/9) crossover ────────────────────────────────────
		_, _, macdCross, macdDir := macd.updateMACD(bar.Close)

		out = append(out, sampleRow{
			Time:          bar.Time,
			Close:         bar.Close,
			IndFired:      indFired,
			IndDirection:  indDir,
			IndScore:      indScore,
			RevFired:      revFired,
			RevDirection:  revDir,
			RevSignedF:    revSigned,
			StretchZ:      stretchZ,
			VolShift:      volShift,
			Absorption:    absorption,
			MACDCross:     macdCross,
			MACDDirection: macdDir,
		})
	}

	fillForwardReturns(out)
	fillIndNearby(out, indSuppressBars)
	return out
}

// fillIndNearby marks each row whose ±window neighborhood (within the same
// UTC session date) contains at least one inducement fire. Used by edge tests
// to see whether the reversal factor's edge holds when we exclude the
// ambiguous bars adjacent to inducement activity.
func fillIndNearby(rows []sampleRow, window int) {
	if window <= 0 || len(rows) == 0 {
		return
	}
	// Precompute session dates.
	sess := make([]string, len(rows))
	for i := range rows {
		sess[i] = rows[i].Time.UTC().Format("2006-01-02")
	}
	// For each row, scan [i-window, i+window].
	for i := range rows {
		lo := i - window
		if lo < 0 {
			lo = 0
		}
		hi := i + window
		if hi >= len(rows) {
			hi = len(rows) - 1
		}
		for j := lo; j <= hi; j++ {
			if sess[j] != sess[i] {
				continue
			}
			if rows[j].IndFired {
				rows[i].IndNearby = true
				break
			}
		}
	}
}

// fillForwardReturns populates FwdR3/R6/R12 for each row by looking ahead in
// the same symbol's RTH row sequence. A forward return is only valid if the
// lookahead bar is within the SAME trading session (no overnight gaps) — this
// is the honest horizon for an intraday fade signal. Rows near session end
// or near the end of the full sample leave Have* = false.
func fillForwardReturns(rows []sampleRow) {
	if len(rows) == 0 {
		return
	}
	// Tag each row with its session date (YYYY-MM-DD in UTC — fine for
	// session-boundary detection since a session never crosses UTC dates for
	// US equities).
	sessionOf := func(t time.Time) string { return t.UTC().Format("2006-01-02") }
	sessions := make([]string, len(rows))
	for i := range rows {
		sessions[i] = sessionOf(rows[i].Time)
	}
	fillAt := func(i, k int) (float64, bool) {
		j := i + k
		if j >= len(rows) {
			return 0, false
		}
		if sessions[j] != sessions[i] {
			return 0, false
		}
		if rows[i].Close <= 0 || rows[j].Close <= 0 {
			return 0, false
		}
		return math.Log(rows[j].Close / rows[i].Close), true
	}
	for i := range rows {
		if r, ok := fillAt(i, 1); ok {
			rows[i].FwdR1 = r
			rows[i].HaveR1 = true
		}
		if r, ok := fillAt(i, 3); ok {
			rows[i].FwdR3 = r
			rows[i].HaveR3 = true
		}
		if r, ok := fillAt(i, 6); ok {
			rows[i].FwdR6 = r
			rows[i].HaveR6 = true
		}
		if r, ok := fillAt(i, 12); ok {
			rows[i].FwdR12 = r
			rows[i].HaveR12 = true
		}
	}
}

// aggregateTo5m groups 1m bars into 5m buckets aligned on clock-minute % 5 == 0
// in the source UTC time. RTH-boundary alignment is handled downstream by the
// RTH filter; this aggregator only needs consistent 5m boundaries so that the
// HH:MM bucketed-volume keys are stable across sessions. Bucket key = floor
// bar.Time to the nearest 5-minute mark.
func aggregateTo5m(src []domain.MarketBar, _ *time.Location) []domain.MarketBar {
	if len(src) == 0 {
		return nil
	}
	out := make([]domain.MarketBar, 0, len(src)/5+1)
	var cur domain.MarketBar
	var curKey time.Time
	flush := func() {
		if cur.Time.IsZero() {
			return
		}
		out = append(out, cur)
	}
	for _, b := range src {
		key := b.Time.Truncate(5 * time.Minute)
		if cur.Time.IsZero() {
			cur = domain.MarketBar{
				Time: key, Symbol: b.Symbol, Timeframe: "5m",
				Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume,
			}
			curKey = key
			continue
		}
		if !key.Equal(curKey) {
			flush()
			cur = domain.MarketBar{
				Time: key, Symbol: b.Symbol, Timeframe: "5m",
				Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume,
			}
			curKey = key
			continue
		}
		if b.High > cur.High {
			cur.High = b.High
		}
		if b.Low < cur.Low {
			cur.Low = b.Low
		}
		cur.Close = b.Close
		cur.Volume += b.Volume
	}
	flush()
	return out
}

func appendSwingLevel(buf []start.SwingLevel, lvl start.SwingLevel, depth int) []start.SwingLevel {
	buf = append(buf, lvl)
	if depth > 0 && len(buf) > depth {
		buf = buf[len(buf)-depth:]
	}
	return buf
}

func pruneStale(buf []start.SwingLevel, maxAge int) []start.SwingLevel {
	if maxAge <= 0 {
		return buf
	}
	out := buf[:0]
	for _, s := range buf {
		if s.BarAge <= maxAge {
			out = append(out, s)
		}
	}
	return out
}

func computeStats(symbol string, rows []sampleRow) symbolStats {
	s := symbolStats{Symbol: symbol, Bars: len(rows)}
	if len(rows) == 0 {
		return s
	}
	for _, r := range rows {
		if r.IndFired {
			s.IndFires++
		}
		if r.RevFired {
			s.RevFires++
		}
		if r.IndFired && r.RevFired {
			s.BothFire++
			if r.IndDirection == r.RevDirection {
				s.SameDirectionBoth++
			} else {
				s.OppositeDirBoth++
			}
		}
	}

	// Jaccard over fire events.
	union := s.IndFires + s.RevFires - s.BothFire
	if union > 0 {
		s.Jaccard = float64(s.BothFire) / float64(union)
	}
	if s.BothFire > 0 {
		s.DirAgreement = float64(s.SameDirectionBoth) / float64(s.BothFire)
	}

	s.PRev = float64(s.RevFires) / float64(s.Bars)
	if s.IndFires > 0 {
		s.PRevGivenInd = float64(s.BothFire) / float64(s.IndFires)
		if s.PRev > 0 {
			s.CondLift = s.PRevGivenInd / s.PRev
		}
	}

	// Spearman across all bars on signed scores. Treat inducement score (int)
	// and reversal signed magnitude directly; rank both, pearson on ranks.
	if len(rows) >= 30 {
		xs := make([]float64, len(rows))
		ys := make([]float64, len(rows))
		for i, r := range rows {
			xs[i] = float64(r.IndScore)
			ys[i] = r.RevSignedF
		}
		s.SpearmanAll = spearman(xs, ys)
	}

	// Pearson among co-fires on signed scores.
	if s.BothFire >= 10 {
		xs := make([]float64, 0, s.BothFire)
		ys := make([]float64, 0, s.BothFire)
		for _, r := range rows {
			if r.IndFired && r.RevFired {
				xs = append(xs, float64(r.IndScore))
				ys = append(ys, r.RevSignedF)
			}
		}
		s.PearsonCoFire = pearson(xs, ys)
	}

	return s
}

func printSummary(perSymbol []symbolStats, universe symbolStats) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 110))
	fmt.Println("SIGNAL CORRELATION: inducement vs price-volume structure shift reversal factor")
	fmt.Println(strings.Repeat("=", 110))
	fmt.Printf("%-8s %8s %8s %8s %8s %8s %10s %10s %10s %10s\n",
		"symbol", "bars", "ind_fr", "rev_fr", "both", "jacc", "dir_agree", "cond_lift", "spearman", "pearCoFire")
	fmt.Println(strings.Repeat("-", 110))
	for _, s := range perSymbol {
		indRate := 0.0
		revRate := 0.0
		if s.Bars > 0 {
			indRate = float64(s.IndFires) / float64(s.Bars) * 100
			revRate = float64(s.RevFires) / float64(s.Bars) * 100
		}
		fmt.Printf("%-8s %8d %7.2f%% %7.2f%% %8d %8.3f %10.3f %10.3f %10.3f %10.3f\n",
			s.Symbol, s.Bars, indRate, revRate, s.BothFire,
			s.Jaccard, s.DirAgreement, s.CondLift, s.SpearmanAll, s.PearsonCoFire)
	}
	fmt.Println(strings.Repeat("-", 110))
	uIndRate := 0.0
	uRevRate := 0.0
	if universe.Bars > 0 {
		uIndRate = float64(universe.IndFires) / float64(universe.Bars) * 100
		uRevRate = float64(universe.RevFires) / float64(universe.Bars) * 100
	}
	fmt.Printf("%-8s %8d %7.2f%% %7.2f%% %8d %8.3f %10.3f %10.3f %10.3f %10.3f\n",
		"UNIV", universe.Bars, uIndRate, uRevRate, universe.BothFire,
		universe.Jaccard, universe.DirAgreement, universe.CondLift, universe.SpearmanAll, universe.PearsonCoFire)
	fmt.Println(strings.Repeat("=", 110))
}

func decision(u symbolStats) {
	fmt.Println()
	fmt.Println("DECISION RULE (from strategy-tuner brief):")
	fmt.Println("  Spearman > 0.5 AND dir_agree > 0.80  → extend InducementSignal, no new detector")
	fmt.Println("  Spearman < 0.3  OR dir_agree < 0.60  → build as separate detector")
	fmt.Println("  Otherwise → ambiguous: test reversal edge on bars where inducement does NOT fire")
	fmt.Println()
	switch {
	case u.SpearmanAll > 0.5 && u.DirAgreement > 0.80:
		fmt.Println("VERDICT: EXTEND InducementSignal with 2 new fields (stretch_z, vol_shift).")
	case u.SpearmanAll < 0.3 || u.DirAgreement < 0.60:
		fmt.Println("VERDICT: BUILD as separate detector (signals are structurally independent).")
	default:
		fmt.Println("VERDICT: AMBIGUOUS — run an incremental-edge test on non-overlap bars next.")
	}
	fmt.Printf("  Observed: spearman=%.3f, dir_agreement=%.3f, cond_lift=%.3f, jaccard=%.3f\n",
		u.SpearmanAll, u.DirAgreement, u.CondLift, u.Jaccard)
}

// ── Forward-return edge tests ──────────────────────────────────────────

type bucketStats struct {
	Label   string
	N       int
	Mean    float64 // mean signed forward return (bps)
	Stdev   float64 // bps
	TStat   float64
	WinRate float64 // fraction with signed return > 0
}

// directionForBucket returns the "fade direction" to sign forward returns by.
// Reversal-category buckets use RevDirection; inducement-only uses IndDirection.
// Baseline uses 0 (no sign flip; returns are raw — expect ~0).
type rowFilter func(r sampleRow) (include bool, direction int)

func collectBucket(rows []sampleRow, filter rowFilter, horizon int, label string) bucketStats {
	bs := bucketStats{Label: label}
	vals := make([]float64, 0, 1024)
	for _, r := range rows {
		if !inEdgeWindow(r.Time) {
			continue
		}
		include, dir := filter(r)
		if !include {
			continue
		}
		var fwd float64
		var have bool
		switch horizon {
		case 1:
			fwd, have = r.FwdR1, r.HaveR1
		case 3:
			fwd, have = r.FwdR3, r.HaveR3
		case 6:
			fwd, have = r.FwdR6, r.HaveR6
		case 12:
			fwd, have = r.FwdR12, r.HaveR12
		}
		if !have {
			continue
		}
		signed := fwd
		if dir != 0 {
			signed = float64(dir) * fwd
		}
		vals = append(vals, signed*10000.0) // bps
	}
	if len(vals) == 0 {
		return bs
	}
	bs.N = len(vals)
	bs.Mean = mean(vals)
	bs.Stdev = stdev(vals)
	if bs.Stdev > 0 && bs.N > 1 {
		bs.TStat = bs.Mean / (bs.Stdev / math.Sqrt(float64(bs.N)))
	}
	wins := 0
	for _, v := range vals {
		if v > 0 {
			wins++
		}
	}
	bs.WinRate = float64(wins) / float64(bs.N)
	return bs
}

func printEdgeTable(rows []sampleRow) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 110))
	fmt.Println("FORWARD-RETURN EDGE TEST (direction-signed, bps; positive = signal worked)")
	if !edgeFromTime.IsZero() || !edgeToTime.IsZero() {
		from := "(none)"
		to := "(none)"
		if !edgeFromTime.IsZero() {
			from = edgeFromTime.Format("2006-01-02")
		}
		if !edgeToTime.IsZero() {
			to = edgeToTime.Format("2006-01-02")
		}
		fmt.Printf("  edge window: %s to %s\n", from, to)
	}
	fmt.Println(strings.Repeat("=", 110))
	fmt.Printf("%-24s %6s | %8s %6s %6s %6s | %8s %6s %6s %6s | %8s %6s %6s %6s\n",
		"bucket", "N-3",
		"r3_bps", "sd3", "t3", "wr3",
		"r6_bps", "sd6", "t6", "wr6",
		"r12_bps", "sd12", "t12", "wr12")
	fmt.Println(strings.Repeat("-", 110))

	rowSets := []struct {
		label  string
		filter rowFilter
	}{
		{"baseline (no fire)", func(r sampleRow) (bool, int) {
			return !r.IndFired && !r.RevFired, 0
		}},
		{"rev-any", func(r sampleRow) (bool, int) {
			return r.RevFired, r.RevDirection
		}},
		{"rev-only (no ind same bar)", func(r sampleRow) (bool, int) {
			return r.RevFired && !r.IndFired, r.RevDirection
		}},
		{"rev-only + ind-gated ±N", func(r sampleRow) (bool, int) {
			return r.RevFired && !r.IndNearby, r.RevDirection
		}},
		{"rev+ind co-fire", func(r sampleRow) (bool, int) {
			return r.RevFired && r.IndFired, r.RevDirection
		}},
		{"ind-only (no rev)", func(r sampleRow) (bool, int) {
			return r.IndFired && !r.RevFired, r.IndDirection
		}},
	}

	for _, rs := range rowSets {
		b3 := collectBucket(rows, rs.filter, 3, rs.label)
		b6 := collectBucket(rows, rs.filter, 6, rs.label)
		b12 := collectBucket(rows, rs.filter, 12, rs.label)
		fmt.Printf("%-24s %6d | %8.2f %6.1f %6.2f %6.2f | %8.2f %6.1f %6.2f %6.2f | %8.2f %6.1f %6.2f %6.2f\n",
			rs.label, b3.N,
			b3.Mean, b3.Stdev, b3.TStat, b3.WinRate,
			b6.Mean, b6.Stdev, b6.TStat, b6.WinRate,
			b12.Mean, b12.Stdev, b12.TStat, b12.WinRate)
	}
	fmt.Println(strings.Repeat("=", 110))
}

// printStretchZQuintiles buckets rev-only bars into |stretch_z| quintiles and
// reports the direction-signed 6-bar forward return per quintile. Monotone
// increasing = real edge that scales with conviction (quant brief's honesty
// check). Flat or non-monotone = likely noise.
func printStretchZQuintiles(rows []sampleRow) {
	var picked []sampleRow
	for _, r := range rows {
		if !inEdgeWindow(r.Time) {
			continue
		}
		if r.RevFired && !r.IndFired && r.HaveR6 {
			picked = append(picked, r)
		}
	}
	if len(picked) < 50 {
		fmt.Println()
		fmt.Printf("STRETCH-Z QUINTILE TEST: skipped (only %d rev-only rows, need >= 50)\n", len(picked))
		return
	}
	sort.Slice(picked, func(i, j int) bool {
		return math.Abs(picked[i].StretchZ) < math.Abs(picked[j].StretchZ)
	})
	q := len(picked) / 5
	fmt.Println()
	fmt.Println(strings.Repeat("=", 86))
	fmt.Println("STRETCH-Z QUINTILE MONOTONICITY TEST (rev-only bars, signed 6-bar forward return)")
	fmt.Println(strings.Repeat("=", 86))
	fmt.Printf("%-8s %8s %12s %12s %10s %10s %10s\n",
		"quintile", "N", "|z|_min", "|z|_max", "r6_bps", "tstat", "winrate")
	fmt.Println(strings.Repeat("-", 86))
	for i := 0; i < 5; i++ {
		lo := i * q
		hi := lo + q
		if i == 4 {
			hi = len(picked)
		}
		slice := picked[lo:hi]
		vals := make([]float64, len(slice))
		for j, r := range slice {
			vals[j] = float64(r.RevDirection) * r.FwdR6 * 10000.0
		}
		m := mean(vals)
		sd := stdev(vals)
		t := 0.0
		if sd > 0 && len(vals) > 1 {
			t = m / (sd / math.Sqrt(float64(len(vals))))
		}
		wins := 0
		for _, v := range vals {
			if v > 0 {
				wins++
			}
		}
		wr := float64(wins) / float64(len(vals))
		zMin := math.Abs(slice[0].StretchZ)
		zMax := math.Abs(slice[len(slice)-1].StretchZ)
		fmt.Printf("Q%d       %8d %12.3f %12.3f %10.2f %10.2f %10.3f\n",
			i+1, len(slice), zMin, zMax, m, t, wr)
	}
	fmt.Println(strings.Repeat("=", 86))
}

// printMACDEdgeTable isolates MACD crossover bars, splits them by whether the
// exhaustion pattern (RevFired) fired on the same bar, and reports
// direction-signed forward returns. Per quant brief: we want the co-fire
// bucket to have negative mean forward return if a MACD-veto is warranted.
// If positive or zero, the veto hypothesis is rejected.
func printMACDEdgeTable(rows []sampleRow) {
	// Precount MACD crossovers within the edge window for the header.
	var totalX, longX, shortX, cofireX int
	for _, r := range rows {
		if !inEdgeWindow(r.Time) || !r.MACDCross {
			continue
		}
		totalX++
		if r.MACDDirection > 0 {
			longX++
		} else if r.MACDDirection < 0 {
			shortX++
		}
		if r.RevFired {
			cofireX++
		}
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 114))
	fmt.Println("MACD CROSSOVER EDGE TEST (direction-signed by MACDDirection, bps; positive = MACD signal worked)")
	if !edgeFromTime.IsZero() || !edgeToTime.IsZero() {
		from := "(none)"
		to := "(none)"
		if !edgeFromTime.IsZero() {
			from = edgeFromTime.Format("2006-01-02")
		}
		if !edgeToTime.IsZero() {
			to = edgeToTime.Format("2006-01-02")
		}
		fmt.Printf("  edge window: %s to %s\n", from, to)
	}
	fmt.Printf("  crosses: total=%d (long=%d, short=%d)  co-fire with exhaustion pattern: %d\n",
		totalX, longX, shortX, cofireX)
	fmt.Println(strings.Repeat("=", 114))
	fmt.Printf("%-26s %6s | %8s %6s | %8s %6s | %8s %6s | %8s %6s %6s\n",
		"bucket", "N",
		"r1_bps", "t1",
		"r3_bps", "t3",
		"r6_bps", "t6",
		"r12_bps", "t12", "wr6")
	fmt.Println(strings.Repeat("-", 114))

	buckets := []struct {
		label  string
		filter rowFilter
	}{
		{"macd-cross (all)", func(r sampleRow) (bool, int) {
			return r.MACDCross, r.MACDDirection
		}},
		{"macd-cross, no cofire", func(r sampleRow) (bool, int) {
			return r.MACDCross && !r.RevFired, r.MACDDirection
		}},
		{"macd-cross + exhaustion", func(r sampleRow) (bool, int) {
			return r.MACDCross && r.RevFired, r.MACDDirection
		}},
		{"macd-cross + ind fires", func(r sampleRow) (bool, int) {
			return r.MACDCross && r.IndFired, r.MACDDirection
		}},
		{"macd-cross + ind + exhaustion", func(r sampleRow) (bool, int) {
			return r.MACDCross && r.IndFired && r.RevFired, r.MACDDirection
		}},
	}
	for _, b := range buckets {
		b1 := collectBucket(rows, b.filter, 1, b.label)
		b3 := collectBucket(rows, b.filter, 3, b.label)
		b6 := collectBucket(rows, b.filter, 6, b.label)
		b12 := collectBucket(rows, b.filter, 12, b.label)
		fmt.Printf("%-26s %6d | %8.2f %6.2f | %8.2f %6.2f | %8.2f %6.2f | %8.2f %6.2f %6.2f\n",
			b.label, b1.N,
			b1.Mean, b1.TStat,
			b3.Mean, b3.TStat,
			b6.Mean, b6.TStat,
			b12.Mean, b12.TStat, b6.WinRate)
	}
	fmt.Println(strings.Repeat("=", 114))
	fmt.Println("Veto decision: co-fire bucket mean returns should be NEGATIVE and consistent across IS/OOS/held-out.")
	fmt.Println("If positive or zero in any window, the MACD-veto hypothesis is rejected.")
}

func writeCSV(path string, rows []sampleRow) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"time", "close", "ind_fired", "ind_dir", "ind_score", "rev_fired", "rev_dir", "rev_signed", "stretch_z", "vol_shift", "absorption", "fwd_r3", "fwd_r6", "fwd_r12"})
	for _, r := range rows {
		r3 := ""
		if r.HaveR3 {
			r3 = ftoa(r.FwdR3)
		}
		r6 := ""
		if r.HaveR6 {
			r6 = ftoa(r.FwdR6)
		}
		r12 := ""
		if r.HaveR12 {
			r12 = ftoa(r.FwdR12)
		}
		_ = w.Write([]string{
			r.Time.UTC().Format(time.RFC3339),
			ftoa(r.Close),
			btoa(r.IndFired),
			fmt.Sprintf("%d", r.IndDirection),
			fmt.Sprintf("%d", r.IndScore),
			btoa(r.RevFired),
			fmt.Sprintf("%d", r.RevDirection),
			ftoa(r.RevSignedF),
			ftoa(r.StretchZ),
			ftoa(r.VolShift),
			ftoa(r.Absorption),
			r3, r6, r12,
		})
	}
	return nil
}

// ── Math helpers ────────────────────────────────────────────────────────

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func stdev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

func medianOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]float64, len(xs))
	copy(cp, xs)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

func pearson(xs, ys []float64) float64 {
	if len(xs) != len(ys) || len(xs) < 2 {
		return 0
	}
	mx := mean(xs)
	my := mean(ys)
	var num, dx2, dy2 float64
	for i := range xs {
		dx := xs[i] - mx
		dy := ys[i] - my
		num += dx * dy
		dx2 += dx * dx
		dy2 += dy * dy
	}
	den := math.Sqrt(dx2 * dy2)
	if den == 0 {
		return 0
	}
	return num / den
}

func spearman(xs, ys []float64) float64 {
	rx := rankAvg(xs)
	ry := rankAvg(ys)
	return pearson(rx, ry)
}

// rankAvg returns the average-rank (fractional ranks broken by average) for each x.
func rankAvg(xs []float64) []float64 {
	type iv struct {
		Idx int
		Val float64
	}
	n := len(xs)
	sorted := make([]iv, n)
	for i, v := range xs {
		sorted[i] = iv{i, v}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Val < sorted[j].Val })
	ranks := make([]float64, n)
	i := 0
	for i < n {
		j := i
		for j+1 < n && sorted[j+1].Val == sorted[i].Val {
			j++
		}
		// Average rank for ties (1-indexed).
		avg := float64(i+j+2) / 2.0
		for k := i; k <= j; k++ {
			ranks[sorted[k].Idx] = avg
		}
		i = j + 1
	}
	return ranks
}

func ftoa(x float64) string {
	return fmt.Sprintf("%.6f", x)
}

func btoa(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Silence unused-import when running offline.
var _ = sql.ErrNoRows
