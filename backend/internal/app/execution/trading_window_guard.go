package execution

import (
	"fmt"
	"strconv"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

// TradingWindowGuard rejects entry orders outside a configured time-of-day
// window. The window is read from intent.Meta:
//
//	allowed_hours_start  — "HH:MM" (e.g. "08:00")
//	allowed_hours_end    — "HH:MM" (e.g. "17:00")
//	allowed_hours_tz     — IANA timezone (e.g. "America/New_York")
//	skip_weekends        — "true" to reject on Saturday/Sunday
//
// If none of these keys are present, the guard is a no-op (opt-in).
type TradingWindowGuard struct {
	nowFunc func() time.Time // injectable for testing
	log     zerolog.Logger
}

func NewTradingWindowGuard(log zerolog.Logger) *TradingWindowGuard {
	return &TradingWindowGuard{
		nowFunc: time.Now,
		log:     log,
	}
}

func NewTradingWindowGuardWithClock(nowFunc func() time.Time, log zerolog.Logger) *TradingWindowGuard {
	return &TradingWindowGuard{
		nowFunc: nowFunc,
		log:     log,
	}
}

func (g *TradingWindowGuard) Check(intent domain.OrderIntent) error {
	meta := intent.Meta
	if meta == nil {
		return nil
	}

	if raw, ok := meta["skip_weekends"]; ok {
		skip, _ := strconv.ParseBool(raw)
		if skip {
			now := g.nowFunc()
			loc := time.UTC
			if tz, tzOK := meta["allowed_hours_tz"]; tzOK && tz != "" {
				if parsed, err := time.LoadLocation(tz); err == nil {
					loc = parsed
				}
			}
			localNow := now.In(loc)
			wd := localNow.Weekday()
			if wd == time.Saturday || wd == time.Sunday {
				return fmt.Errorf("trading_window: %s rejected — trading disabled on weekends (%s in %s)",
					intent.Symbol, wd, loc)
			}
		}
	}

	startStr, hasStart := meta["allowed_hours_start"]
	endStr, hasEnd := meta["allowed_hours_end"]
	if !hasStart || !hasEnd {
		return nil
	}

	startH, startM, err := parseHHMM(startStr)
	if err != nil {
		g.log.Warn().Err(err).Str("start", startStr).Msg("trading window: invalid start time — allowing")
		return nil
	}
	endH, endM, err := parseHHMM(endStr)
	if err != nil {
		g.log.Warn().Err(err).Str("end", endStr).Msg("trading window: invalid end time — allowing")
		return nil
	}

	loc := time.UTC
	if tz, ok := meta["allowed_hours_tz"]; ok && tz != "" {
		if parsed, err := time.LoadLocation(tz); err == nil {
			loc = parsed
		} else {
			g.log.Warn().Err(err).Str("tz", tz).Msg("trading window: invalid timezone — using UTC")
		}
	}

	now := g.nowFunc().In(loc)
	currentMinutes := now.Hour()*60 + now.Minute()
	startMinutes := startH*60 + startM
	endMinutes := endH*60 + endM

	if currentMinutes < startMinutes || currentMinutes >= endMinutes {
		return fmt.Errorf("trading_window: %s rejected — current time %s outside allowed window %s–%s %s",
			intent.Symbol, now.Format("15:04"), startStr, endStr, loc)
	}

	g.log.Debug().
		Str("symbol", string(intent.Symbol)).
		Str("time", now.Format("15:04")).
		Str("window", fmt.Sprintf("%s–%s", startStr, endStr)).
		Msg("trading window guard passed")

	return nil
}

// parseHHMM parses "HH:MM" into hours and minutes.
func parseHHMM(s string) (int, int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, fmt.Errorf("expected HH:MM format, got %q", s)
	}
	h, err := strconv.Atoi(s[:2])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	m, err := strconv.Atoi(s[3:])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	return h, m, nil
}
