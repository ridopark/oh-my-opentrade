// Package copytradereplay replays historical Discord copytrade messages
// through the same event bus the live sidecar feeds, so the production
// copytrade_v1 strategy can be backtested against 90+ days of signals
// without changing its code paths.
//
// parser.go is a Go port of services/discord-copytrade/parser.py. The grammar
// is stable and shared with production; any change here must also update the
// Python sidecar so live and replay signal shapes stay identical.
package copytradereplay

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParsedSignal is the structural output of parsing one line of a Discord
// message. Callers combine this with Discord message metadata (id, author,
// posted_at) to construct a domain.CopytradeSignalPayload.
type ParsedSignal struct {
	Action  string    // BTO | STC | AVG (uppercase)
	Ticker  string    // uppercase
	Expiry  time.Time // 00:00 UTC of the resolved calendar date
	Strike  float64
	Right   string // C | P (uppercase)
	Price   float64
	Tail    string // trimmed trailing text after the price
	RawLine string // the matched line, trimmed
}

var lineRe = regexp.MustCompile(
	`(?i)^\s*` +
		`(?P<action>BTO|STC|AVG)\s+` +
		`(?P<ticker>[A-Z]{1,6})\s+` +
		`(?P<expiry>\d{1,2}/\d{1,2}(?:/\d{2,4})?)\s+` +
		`(?P<strike>\d+(?:\.\d+)?)` +
		`(?P<right>[CP])` +
		`\s*@\s*` +
		`(?P<price>\d*\.?\d+)` +
		`(?P<tail>.*)$`,
)

// ParseMessage parses one Discord message body into zero or more signals,
// one per matching line. Non-matching lines (pure commentary, malformed
// entries) are silently skipped, matching the Python parser's contract.
//
// today determines how ambiguous M/D expiries roll forward: if the
// resolved date is before today, it rolls to the next year. For deterministic
// replay, pass the message's PostedAt date; for live, time.Now() works.
func ParseMessage(text string, today time.Time) []ParsedSignal {
	var out []ParsedSignal
	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		sig, ok := parseLine(line, today)
		if !ok {
			continue
		}
		out = append(out, sig)
	}
	return out
}

func parseLine(line string, today time.Time) (ParsedSignal, bool) {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return ParsedSignal{}, false
	}
	g := func(name string) string { return m[lineRe.SubexpIndex(name)] }

	strike, err := strconv.ParseFloat(g("strike"), 64)
	if err != nil {
		return ParsedSignal{}, false
	}
	price, err := strconv.ParseFloat(g("price"), 64)
	if err != nil {
		return ParsedSignal{}, false
	}
	expiry, err := resolveExpiry(g("expiry"), today)
	if err != nil {
		return ParsedSignal{}, false
	}
	return ParsedSignal{
		Action:  strings.ToUpper(g("action")),
		Ticker:  strings.ToUpper(g("ticker")),
		Expiry:  expiry,
		Strike:  strike,
		Right:   strings.ToUpper(g("right")),
		Price:   price,
		Tail:    strings.TrimSpace(g("tail")),
		RawLine: line,
	}, true
}

// resolveExpiry mirrors parser.py:_resolve_expiry. Accepts M/D, M/D/YY, or
// M/D/YYYY. For bare M/D, rolls forward a year if the resolved date is
// strictly before today (author shorthand for next year's expiry).
func resolveExpiry(md string, today time.Time) (time.Time, error) {
	parts := strings.Split(md, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return time.Time{}, fmt.Errorf("invalid expiry format %q", md)
	}
	month, err := strconv.Atoi(parts[0])
	if err != nil {
		return time.Time{}, err
	}
	day, err := strconv.Atoi(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	if len(parts) == 3 {
		yr, err := strconv.Atoi(parts[2])
		if err != nil {
			return time.Time{}, err
		}
		if yr < 100 {
			yr += 2000
		}
		return time.Date(yr, time.Month(month), day, 0, 0, 0, 0, time.UTC), nil
	}
	todayUTC := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	candidate := time.Date(today.Year(), time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if candidate.Before(todayUTC) {
		candidate = time.Date(today.Year()+1, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	return candidate, nil
}
