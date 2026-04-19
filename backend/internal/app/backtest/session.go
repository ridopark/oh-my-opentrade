package backtest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// dayKey returns a "YYYY-MM-DD" string using strconv instead of time.Format
// to reduce allocations in hot paths.
func dayKey(t time.Time, loc *time.Location) string {
	y, m, d := t.In(loc).Date()
	var buf [10]byte
	buf[0] = byte('0' + y/1000)
	buf[1] = byte('0' + (y/100)%10)
	buf[2] = byte('0' + (y/10)%10)
	buf[3] = byte('0' + y%10)
	buf[4] = '-'
	buf[5] = byte('0' + int(m)/10)
	buf[6] = byte('0' + int(m)%10)
	buf[7] = '-'
	buf[8] = byte('0' + d/10)
	buf[9] = byte('0' + d%10)
	return string(buf[:])
}

type SessionData struct {
	Date     string
	Open     float64
	OpenTime time.Time
	High     float64
	HighTime time.Time
	Low      float64
	LowTime  time.Time
	Close    float64

	ORHigh     float64
	ORHighTime time.Time
	ORLow      float64
	ORLowTime  time.Time
}

type SessionResolver struct {
	// RWMutex so ResolveAnchors/KeyLevelPrices/PreviousDay/GetBarsSince
	// can run concurrently during replay. Load*/PopulateBarCache and
	// counter increments still take the write lock.
	mu       sync.RWMutex
	sessions map[string]map[string]SessionData
	barCache map[string][]start.Bar // "SYMBOL:2025-03-15" → 1m bars for that day
	loc      *time.Location
	log      zerolog.Logger

	// Sprint-7 add-on survivorship-bias filter. Both optional; when
	// enforceUniverse is false (default) these fields have no effect and
	// the loader returns every row just like before.
	universe        ports.UniverseHistoryPort
	enforceUniverse bool
	// windowCache memoises WindowsFor lookups for the duration of a
	// backtest run to avoid re-querying the DB for every bar.
	windowCache map[string][]ports.UniverseWindow

	// Diagnostics counters. Protected by mu alongside sessions.
	scanErrors         int
	unknownSymbolHits  int
}

func NewSessionResolver(loc *time.Location) *SessionResolver {
	return &SessionResolver{
		sessions:    make(map[string]map[string]SessionData),
		barCache:    make(map[string][]start.Bar),
		loc:         loc,
		log:         zerolog.Nop(),
		windowCache: make(map[string][]ports.UniverseWindow),
	}
}

// SetLogger attaches a logger for diagnostics. Safe to call before or
// after Load/Load24H; calls with a zero logger effectively disable output.
func (r *SessionResolver) SetLogger(log zerolog.Logger) {
	r.log = log.With().Str("component", "session_resolver").Logger()
}

// SetUniverseHistory wires the Sprint-7-addon survivorship-bias filter.
// Pass enforce=false (or a nil port) to disable — the loader behaves
// exactly as before. When enforce=true and port==nil the resolver still
// disables filtering; the caller is responsible for logging the warning
// referenced in BacktestConfig.EnforceUniverseHistory.
func (r *SessionResolver) SetUniverseHistory(port ports.UniverseHistoryPort, enforce bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.universe = port
	r.enforceUniverse = enforce && port != nil
	r.windowCache = make(map[string][]ports.UniverseWindow)
}

// CheckUniverse returns true iff `sym` has any tradable overlap with
// [from, to). When the filter is disabled it always returns true. Use
// before paying the cost of LoadBars/Load for a symbol that might not
// have existed during the requested range.
//
// Logs a warning at the first observation of a symbol missing from the
// universe port entirely (distinct from a symbol whose windows simply
// don't overlap the requested range), so callers notice seeding gaps.
func (r *SessionResolver) CheckUniverse(ctx context.Context, sym domain.Symbol, from, to time.Time) (bool, error) {
	r.mu.RLock()
	port := r.universe
	enforce := r.enforceUniverse
	r.mu.RUnlock()
	if !enforce || port == nil {
		return true, nil
	}
	windows, err := r.loadWindows(ctx, sym)
	if err != nil {
		return false, err
	}
	if len(windows) == 0 {
		r.mu.Lock()
		r.unknownSymbolHits++
		r.mu.Unlock()
		r.log.Warn().
			Str("sym", sym.String()).
			Time("from", from).
			Time("to", to).
			Msg("universe filter: symbol has no seeded windows; treating as non-tradable")
		return false, nil
	}
	for _, w := range windows {
		// Overlap test: [from_date, to_date) ∩ [from, to) non-empty.
		winStart := w.FromDate
		// zero winEnd marks an open-ended (still-tradable) window.
		var winEnd time.Time
		if w.ToDate != nil {
			winEnd = *w.ToDate
		}
		if !winStart.Before(to) {
			continue
		}
		if !winEnd.IsZero() && !winEnd.After(from) {
			continue
		}
		return true, nil
	}
	return false, nil
}

// Stats returns end-of-run counters for observability. Safe to call any
// time; reads are protected by the resolver's mutex.
func (r *SessionResolver) Stats() (scanErrors, unknownSymbols int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.scanErrors, r.unknownSymbolHits
}

// loadWindows returns cached windows or queries+caches them.
func (r *SessionResolver) loadWindows(ctx context.Context, sym domain.Symbol) ([]ports.UniverseWindow, error) {
	key := sym.String()
	r.mu.RLock()
	if cached, ok := r.windowCache[key]; ok {
		r.mu.RUnlock()
		return cached, nil
	}
	port := r.universe
	r.mu.RUnlock()
	if port == nil {
		return nil, nil
	}
	windows, err := port.WindowsFor(ctx, sym)
	if err != nil {
		return nil, fmt.Errorf("session: load universe windows for %s: %w", sym, err)
	}
	r.mu.Lock()
	r.windowCache[key] = windows
	r.mu.Unlock()
	return windows, nil
}

// tradableAt reports whether `at` falls inside any window. Operates on
// already-loaded windows so no DB call happens on the per-bar hot path.
func tradableAt(windows []ports.UniverseWindow, at time.Time) bool {
	for _, w := range windows {
		if at.Before(w.FromDate) {
			continue
		}
		if w.ToDate != nil && !at.Before(*w.ToDate) {
			continue
		}
		return true
	}
	return false
}

func (r *SessionResolver) Load(ctx context.Context, db *sql.DB, sym domain.Symbol, from, to time.Time) error {
	rows, err := db.QueryContext(ctx, `
		WITH rth_bars AS (
			SELECT time, open, high, low, close, volume,
				   DATE(time AT TIME ZONE 'America/New_York') as trading_day,
				   (time AT TIME ZONE 'America/New_York')::time as bar_time
			FROM market_bars
			WHERE symbol = $1 AND timeframe = '1m'
			  AND time >= $2 AND time < $3
			  AND (time AT TIME ZONE 'America/New_York')::time >= '09:30:00'
			  AND (time AT TIME ZONE 'America/New_York')::time < '16:00:00'
		),
		daily AS (
			SELECT trading_day,
				   (ARRAY_AGG(open ORDER BY time))[1] as day_open,
				   MIN(time) as open_time,
				   MAX(high) as day_high,
				   MIN(low) as day_low,
				   (ARRAY_AGG(close ORDER BY time DESC))[1] as day_close
			FROM rth_bars
			GROUP BY trading_day
		),
		high_times AS (
			SELECT DISTINCT ON (r.trading_day) r.trading_day, r.time as high_time
			FROM rth_bars r JOIN daily d ON r.trading_day = d.trading_day
			WHERE r.high = d.day_high
			ORDER BY r.trading_day, r.time
		),
		low_times AS (
			SELECT DISTINCT ON (r.trading_day) r.trading_day, r.time as low_time
			FROM rth_bars r JOIN daily d ON r.trading_day = d.trading_day
			WHERE r.low = d.day_low
			ORDER BY r.trading_day, r.time
		),
		opening_range AS (
			SELECT trading_day,
				   MAX(high) as or_high,
				   MIN(low) as or_low
			FROM rth_bars
			WHERE bar_time >= '09:30:00' AND bar_time < '10:00:00'
			GROUP BY trading_day
		),
		or_high_times AS (
			SELECT DISTINCT ON (r.trading_day) r.trading_day, r.time as or_high_time
			FROM rth_bars r JOIN opening_range o ON r.trading_day = o.trading_day
			WHERE r.high = o.or_high AND r.bar_time >= '09:30:00' AND r.bar_time < '10:00:00'
			ORDER BY r.trading_day, r.time
		),
		or_low_times AS (
			SELECT DISTINCT ON (r.trading_day) r.trading_day, r.time as or_low_time
			FROM rth_bars r JOIN opening_range o ON r.trading_day = o.trading_day
			WHERE r.low = o.or_low AND r.bar_time >= '09:30:00' AND r.bar_time < '10:00:00'
			ORDER BY r.trading_day, r.time
		)
		SELECT d.trading_day, d.day_open, d.open_time, d.day_high, COALESCE(ht.high_time, d.open_time),
			   d.day_low, COALESCE(lt.low_time, d.open_time), d.day_close,
			   COALESCE(o.or_high, 0), COALESCE(oht.or_high_time, d.open_time),
			   COALESCE(o.or_low, 0), COALESCE(olt.or_low_time, d.open_time)
		FROM daily d
		LEFT JOIN high_times ht ON d.trading_day = ht.trading_day
		LEFT JOIN low_times lt ON d.trading_day = lt.trading_day
		LEFT JOIN opening_range o ON d.trading_day = o.trading_day
		LEFT JOIN or_high_times oht ON d.trading_day = oht.trading_day
		LEFT JOIN or_low_times olt ON d.trading_day = olt.trading_day
		ORDER BY d.trading_day`,
		string(sym), from, to)
	if err != nil {
		return err
	}
	defer rows.Close()

	symSessions := make(map[string]SessionData)
	for rows.Next() {
		var s SessionData
		var day time.Time
		if scanErr := rows.Scan(&day, &s.Open, &s.OpenTime, &s.High, &s.HighTime,
			&s.Low, &s.LowTime, &s.Close, &s.ORHigh, &s.ORHighTime, &s.ORLow, &s.ORLowTime); scanErr != nil {
			r.mu.Lock()
			r.scanErrors++
			r.mu.Unlock()
			r.log.Warn().Err(scanErr).Str("sym", sym.String()).Msg("session row scan failed; skipping row")
			continue
		}
		s.Date = day.Format("2006-01-02")
		symSessions[s.Date] = s
	}

	r.mu.Lock()
	r.sessions[sym.String()] = symSessions
	r.mu.Unlock()
	return nil
}

// Load24H loads session data for crypto symbols using full 24h bars (no RTH filter).
// pd_high/pd_low use the entire calendar day's range. Opening range uses 09:30-10:00 ET
// because US equity market hours drive peak crypto volume.
func (r *SessionResolver) Load24H(ctx context.Context, db *sql.DB, sym domain.Symbol, from, to time.Time) error {
	rows, err := db.QueryContext(ctx, `
		WITH all_bars AS (
			SELECT time, open, high, low, close, volume,
				   DATE(time AT TIME ZONE 'America/New_York') as trading_day,
				   (time AT TIME ZONE 'America/New_York')::time as bar_time
			FROM market_bars
			WHERE symbol = $1 AND timeframe = '1m'
			  AND time >= $2 AND time < $3
		),
		daily AS (
			SELECT trading_day,
				   (ARRAY_AGG(open ORDER BY time))[1] as day_open,
				   MIN(time) as open_time,
				   MAX(high) as day_high,
				   MIN(low) as day_low,
				   (ARRAY_AGG(close ORDER BY time DESC))[1] as day_close
			FROM all_bars
			GROUP BY trading_day
		),
		high_times AS (
			SELECT DISTINCT ON (r.trading_day) r.trading_day, r.time as high_time
			FROM all_bars r JOIN daily d ON r.trading_day = d.trading_day
			WHERE r.high = d.day_high
			ORDER BY r.trading_day, r.time
		),
		low_times AS (
			SELECT DISTINCT ON (r.trading_day) r.trading_day, r.time as low_time
			FROM all_bars r JOIN daily d ON r.trading_day = d.trading_day
			WHERE r.low = d.day_low
			ORDER BY r.trading_day, r.time
		),
		opening_range AS (
			SELECT trading_day,
				   MAX(high) as or_high,
				   MIN(low) as or_low
			FROM all_bars
			WHERE bar_time >= '09:30:00' AND bar_time < '10:00:00'
			GROUP BY trading_day
		),
		or_high_times AS (
			SELECT DISTINCT ON (r.trading_day) r.trading_day, r.time as or_high_time
			FROM all_bars r JOIN opening_range o ON r.trading_day = o.trading_day
			WHERE r.high = o.or_high AND r.bar_time >= '09:30:00' AND r.bar_time < '10:00:00'
			ORDER BY r.trading_day, r.time
		),
		or_low_times AS (
			SELECT DISTINCT ON (r.trading_day) r.trading_day, r.time as or_low_time
			FROM all_bars r JOIN opening_range o ON r.trading_day = o.trading_day
			WHERE r.low = o.or_low AND r.bar_time >= '09:30:00' AND r.bar_time < '10:00:00'
			ORDER BY r.trading_day, r.time
		)
		SELECT d.trading_day, d.day_open, d.open_time, d.day_high, COALESCE(ht.high_time, d.open_time),
			   d.day_low, COALESCE(lt.low_time, d.open_time), d.day_close,
			   COALESCE(o.or_high, 0), COALESCE(oht.or_high_time, d.open_time),
			   COALESCE(o.or_low, 0), COALESCE(olt.or_low_time, d.open_time)
		FROM daily d
		LEFT JOIN high_times ht ON d.trading_day = ht.trading_day
		LEFT JOIN low_times lt ON d.trading_day = lt.trading_day
		LEFT JOIN opening_range o ON d.trading_day = o.trading_day
		LEFT JOIN or_high_times oht ON d.trading_day = oht.trading_day
		LEFT JOIN or_low_times olt ON d.trading_day = olt.trading_day
		ORDER BY d.trading_day`,
		string(sym), from, to)
	if err != nil {
		return err
	}
	defer rows.Close()

	symSessions := make(map[string]SessionData)
	for rows.Next() {
		var s SessionData
		var day time.Time
		if scanErr := rows.Scan(&day, &s.Open, &s.OpenTime, &s.High, &s.HighTime,
			&s.Low, &s.LowTime, &s.Close, &s.ORHigh, &s.ORHighTime, &s.ORLow, &s.ORLowTime); scanErr != nil {
			r.mu.Lock()
			r.scanErrors++
			r.mu.Unlock()
			r.log.Warn().Err(scanErr).Str("sym", sym.String()).Msg("session row scan failed (24h); skipping row")
			continue
		}
		s.Date = day.Format("2006-01-02")
		symSessions[s.Date] = s
	}

	r.mu.Lock()
	r.sessions[sym.String()] = symSessions
	r.mu.Unlock()
	return nil
}

func (r *SessionResolver) ResolveAnchors(symbol string, barTime time.Time, anchorNames []string) map[string]time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	symSessions := r.sessions[symbol]
	if symSessions == nil {
		return nil
	}

	et := barTime.In(r.loc)
	today := et.Format("2006-01-02")

	prevDay := r.findPreviousDay(symSessions, et)
	todayData := symSessions[today]

	result := make(map[string]time.Time)
	for _, name := range anchorNames {
		switch name {
		case "pd_high":
			result[name] = prevDay.HighTime
		case "pd_low":
			result[name] = prevDay.LowTime
		case "on_high":
			result[name] = prevDay.HighTime
		case "on_low":
			result[name] = prevDay.LowTime
		case "or_high":
			result[name] = todayData.ORHighTime
		case "or_low":
			result[name] = todayData.ORLowTime
		case "session_open":
			result[name] = todayData.OpenTime
		case "weekly_open":
			weekOpenTime := r.findWeekOpen(symSessions, et)
			if !weekOpenTime.IsZero() {
				result[name] = weekOpenTime
			}
		}
	}

	return result
}

// LoadBars pre-fetches all 1m bars for a symbol across the full date range and
// indexes them by trading day in the barCache. Call once per symbol during init
// so that GetBarsSince can serve from memory instead of hitting the DB.
//
// Deprecated: prefer PopulateBarCache to reuse bars already loaded for replay.
func (r *SessionResolver) LoadBars(ctx context.Context, db *sql.DB, sym domain.Symbol, from, to time.Time) error {
	rows, err := db.QueryContext(ctx, `
		SELECT time, open, high, low, close, volume
		FROM market_bars
		WHERE symbol = $1 AND timeframe = '1m'
		  AND time >= $2 AND time < $3
		ORDER BY time`, string(sym), from, to)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Survivorship-bias filter: when enabled, load the symbol's tradable
	// windows once and drop any bar whose timestamp falls outside. This
	// is defense-in-depth on top of CheckUniverse — CheckUniverse skips
	// symbols with zero overlap, but partial overlaps (mid-range IPOs,
	// delist-then-relist) still need per-row filtering.
	r.mu.RLock()
	enforce := r.enforceUniverse
	r.mu.RUnlock()
	var windows []ports.UniverseWindow
	if enforce {
		windows, err = r.loadWindows(ctx, sym)
		if err != nil {
			return err
		}
	}

	dayBars := make(map[string][]start.Bar)
	for rows.Next() {
		var b start.Bar
		if scanErr := rows.Scan(&b.Time, &b.Open, &b.High, &b.Low, &b.Close, &b.Volume); scanErr != nil {
			continue
		}
		if enforce && !tradableAt(windows, b.Time) {
			continue
		}
		day := dayKey(b.Time, r.loc)
		dayBars[day] = append(dayBars[day], b)
	}

	r.mu.Lock()
	for day, bars := range dayBars {
		r.barCache[sym.String()+":"+day] = bars
	}
	r.mu.Unlock()
	return nil
}

// PopulateBarCache indexes already-loaded MarketBars by trading day so that
// GetBarsSince can serve from memory without a second DB round-trip.
func (r *SessionResolver) PopulateBarCache(sym domain.Symbol, bars []domain.MarketBar) {
	dayBars := make(map[string][]start.Bar)
	for i := range bars {
		b := &bars[i]
		day := dayKey(b.Time, r.loc)
		dayBars[day] = append(dayBars[day], start.Bar{
			Time:   b.Time,
			Open:   b.Open,
			High:   b.High,
			Low:    b.Low,
			Close:  b.Close,
			Volume: b.Volume,
		})
	}

	r.mu.Lock()
	for day, db := range dayBars {
		r.barCache[sym.String()+":"+day] = db
	}
	r.mu.Unlock()
}

// GetBarsSince returns 1m bars for a symbol from `since` to end of that day's session.
// For equities, caps at 16:00 ET (RTH close). For crypto symbols (containing "/"),
// caps at midnight ET (full 24h day). Uses the in-memory barCache populated by
// LoadBars; falls back to a DB query on cache miss.
func (r *SessionResolver) GetBarsSince(ctx context.Context, db *sql.DB, symbol string, since time.Time) []start.Bar {
	if since.IsZero() {
		return nil
	}
	et := since.In(r.loc)
	day := dayKey(since, r.loc)
	eodHour := 16 // RTH close for equities
	if strings.Contains(symbol, "/") {
		eodHour = 24 // full day for crypto
	}
	eod := time.Date(et.Year(), et.Month(), et.Day(), eodHour, 0, 0, 0, r.loc)

	key := symbol + ":" + day
	r.mu.RLock()
	cached, ok := r.barCache[key]
	r.mu.RUnlock()
	if ok {
		// Filter to bars >= since && < eod. Read cached slice header
		// outside the lock; the slice backing array isn't mutated after
		// publication so a read-only iteration is safe.
		var result []start.Bar
		for _, b := range cached {
			if !b.Time.Before(since) && b.Time.Before(eod) {
				result = append(result, b)
			}
		}
		return result
	}

	// Fallback: DB query (should rarely happen if LoadBars was called)
	rows, err := db.QueryContext(ctx, `
		SELECT time, open, high, low, close, volume
		FROM market_bars
		WHERE symbol = $1 AND timeframe = '1m'
		  AND time >= $2 AND time < $3
		ORDER BY time`, symbol, since, eod)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var bars []start.Bar
	for rows.Next() {
		var b start.Bar
		if scanErr := rows.Scan(&b.Time, &b.Open, &b.High, &b.Low, &b.Close, &b.Volume); scanErr != nil {
			continue
		}
		bars = append(bars, b)
	}
	return bars
}

// KeyLevelPrices returns key price levels (pd_high, pd_low, or_high, or_low)
// for confluence scoring in the AVWAP strategy.
func (r *SessionResolver) KeyLevelPrices(symbol string, barTime time.Time) map[string]float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	symSessions := r.sessions[symbol]
	if symSessions == nil {
		return nil
	}
	et := barTime.In(r.loc)
	today := et.Format("2006-01-02")

	prevDay := r.findPreviousDay(symSessions, et)
	todayData := symSessions[today]

	levels := make(map[string]float64)
	if prevDay.High > 0 {
		levels["pd_high"] = prevDay.High
	}
	if prevDay.Low > 0 {
		levels["pd_low"] = prevDay.Low
	}
	if todayData.ORHigh > 0 {
		levels["or_high"] = todayData.ORHigh
	}
	if todayData.ORLow > 0 {
		levels["or_low"] = todayData.ORLow
	}
	if len(levels) == 0 {
		return nil
	}
	return levels
}

func (r *SessionResolver) PreviousDay(symbol string, barTime time.Time) *SessionData {
	r.mu.RLock()
	defer r.mu.RUnlock()
	symSessions := r.sessions[symbol]
	if symSessions == nil {
		return nil
	}
	et := barTime.In(r.loc)
	prev := r.findPreviousDay(symSessions, et)
	if prev.High > 0 {
		return &prev
	}
	return nil
}

// findPreviousDay walks back up to 4 days from the given time to find the most
// recent session with data. For equities this handles Mon→Fri (skip weekends).
// For crypto (24/7) this finds the actual previous calendar day with bars,
// including weekends.
func (r *SessionResolver) findPreviousDay(sessions map[string]SessionData, et time.Time) SessionData {
	for offset := 1; offset <= 4; offset++ {
		day := et.AddDate(0, 0, -offset).Format("2006-01-02")
		if s, ok := sessions[day]; ok {
			return s
		}
	}
	return SessionData{}
}

// findWeekOpen returns the open time of the first trading day in the current ISO week.
// For crypto (which trades 24/7), this anchors to Monday's first session bar.
func (r *SessionResolver) findWeekOpen(sessions map[string]SessionData, et time.Time) time.Time {
	// Find Monday of the current week (ISO: Monday=1, Sunday=7).
	weekday := et.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	monday := et.AddDate(0, 0, -int(weekday-time.Monday))

	// Walk forward from Monday to find the first session with data.
	for i := 0; i < 7; i++ {
		day := monday.AddDate(0, 0, i)
		dayStr := day.Format("2006-01-02")
		if sd, ok := sessions[dayStr]; ok && !sd.OpenTime.IsZero() {
			return sd.OpenTime
		}
	}
	return time.Time{}
}
