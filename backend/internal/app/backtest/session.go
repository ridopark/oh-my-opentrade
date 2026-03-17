package backtest

import (
	"context"
	"time"

	"database/sql"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

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
	sessions map[string]map[string]SessionData
	loc      *time.Location
}

func NewSessionResolver(loc *time.Location) *SessionResolver {
	return &SessionResolver{
		sessions: make(map[string]map[string]SessionData),
		loc:      loc,
	}
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
			continue
		}
		s.Date = day.Format("2006-01-02")
		symSessions[s.Date] = s
	}

	r.sessions[sym.String()] = symSessions
	return nil
}

func (r *SessionResolver) ResolveAnchors(symbol string, barTime time.Time, anchorNames []string) map[string]time.Time {
	symSessions := r.sessions[symbol]
	if symSessions == nil {
		return nil
	}

	et := barTime.In(r.loc)
	today := et.Format("2006-01-02")
	yesterday := et.AddDate(0, 0, -1).Format("2006-01-02")

	if et.Weekday() == time.Monday {
		yesterday = et.AddDate(0, 0, -3).Format("2006-01-02")
	}

	prevDay := symSessions[yesterday]
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
		}
	}
	return result
}

func (r *SessionResolver) PreviousDay(symbol string, barTime time.Time) *SessionData {
	symSessions := r.sessions[symbol]
	if symSessions == nil {
		return nil
	}
	et := barTime.In(r.loc)
	yesterday := et.AddDate(0, 0, -1).Format("2006-01-02")
	if et.Weekday() == time.Monday {
		yesterday = et.AddDate(0, 0, -3).Format("2006-01-02")
	}
	if s, ok := symSessions[yesterday]; ok {
		return &s
	}
	return nil
}
