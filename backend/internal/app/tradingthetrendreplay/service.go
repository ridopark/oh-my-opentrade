// Package tradingthetrendreplay replays a JSONL of historical Discord
// tradingthetrend watchlist messages into the event bus so the production
// tradingthetrend_v1 strategy can be backtested without touching its code
// paths. It bypasses the HTTP handler (whose freshness TTL would drop any
// message older than ~60s) and publishes EventTradingTheTrendSignalReceived
// directly to the sync bus.
//
// Usage: New, Load, then call AdvanceTo(tickTime) at the end of every replay
// tick to drain any signals whose PostedAt <= tickTime. Mirrors the
// copytradereplay.Service shape so the runner integrates them symmetrically.
package tradingthetrendreplay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/strategy/builtin"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Service is the JSONL → bus replay injector for tradingthetrend.
type Service struct {
	log      zerolog.Logger
	bus      ports.EventBusPort
	tenantID string
	envMode  domain.EnvMode

	queue  []queuedSignal
	cursor int

	stats Stats
}

// Stats is a snapshot of loader + injector counters for end-of-run reports.
type Stats struct {
	MessagesRead     int
	MessagesDropped  int
	SignalsLoaded    int
	SignalsPublished int
}

type queuedSignal struct {
	payload domain.TradingTheTrendSignalPayload
}

// historyMessage mirrors the JSONL shape emitted by scrape_history.py
// (services/discord-tradingthetrend/state/history*.jsonl).
type historyMessage struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	TS     string `json:"ts"`
	Text   string `json:"text"`
}

// New returns a ready-to-load Service bound to bus.
func New(bus ports.EventBusPort, tenantID string, envMode domain.EnvMode, log zerolog.Logger) *Service {
	return &Service{
		log:      log,
		bus:      bus,
		tenantID: tenantID,
		envMode:  envMode,
	}
}

// Load reads a JSONL history file, parses each message via the builtin TTT
// parser, filters to [fromTime, toTime] by PostedAt, and stores the resulting
// signal queue sorted by PostedAt ascending. Zero-value bounds disable the
// respective filter.
func (s *Service) Load(path string, fromTime, toTime time.Time) (Stats, error) {
	f, err := os.Open(path)
	if err != nil {
		return s.stats, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var msg historyMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			return s.stats, fmt.Errorf("unmarshal history line: %w", err)
		}
		s.stats.MessagesRead++

		postedAt, err := time.Parse(time.RFC3339Nano, msg.TS)
		if err != nil {
			return s.stats, fmt.Errorf("parse ts %q: %w", msg.TS, err)
		}
		postedAt = postedAt.UTC()

		if !fromTime.IsZero() && postedAt.Before(fromTime) {
			continue
		}
		if !toTime.IsZero() && postedAt.After(toTime) {
			continue
		}

		parsed := builtin.ParseTradingTheTrendMessage(msg.Text)
		if len(parsed) == 0 {
			s.stats.MessagesDropped++
			continue
		}
		for i, sig := range parsed {
			right, err := coerceRight(sig.Right)
			if err != nil {
				return s.stats, fmt.Errorf("%s line %d: %w", msg.ID, i, err)
			}
			s.queue = append(s.queue, queuedSignal{
				payload: domain.TradingTheTrendSignalPayload{
					SignalID:  fmt.Sprintf("tradingthetrend:%s:%d", msg.ID, i),
					MessageID: msg.ID,
					Author:    msg.Author,
					PostedAt:  postedAt,
					Ticker:    domain.Symbol(sig.Ticker),
					Strike:    sig.Strike,
					Right:     right,
					Trigger:   sig.Trigger,
					RawLine:   sig.RawLine,
				},
			})
			s.stats.SignalsLoaded++
		}
	}
	if err := sc.Err(); err != nil {
		return s.stats, fmt.Errorf("scan: %w", err)
	}

	sort.SliceStable(s.queue, func(i, j int) bool {
		return s.queue[i].payload.PostedAt.Before(s.queue[j].payload.PostedAt)
	})

	s.log.Info().
		Int("messages_read", s.stats.MessagesRead).
		Int("messages_dropped", s.stats.MessagesDropped).
		Int("signals_loaded", s.stats.SignalsLoaded).
		Str("from", fromTime.Format(time.RFC3339)).
		Str("to", toTime.Format(time.RFC3339)).
		Msg("tradingthetrend replay history loaded")

	return s.stats, nil
}

// Pending returns the number of signals still waiting to be published.
func (s *Service) Pending() int {
	return len(s.queue) - s.cursor
}

// LoadUniverse scans path for the union of tickers across all parseable
// messages in [fromTime, toTime] (zero-value bounds disable the respective
// filter). Used by backtest bootstrap to pre-register sentinel-rooted TTT
// routing without depending on dynamic AddSymbol-on-signal plumbing.
//
// Reuses builtin.ParseTradingTheTrendMessage so the universe is exactly
// the set of tickers Load would later publish for the same date range.
func LoadUniverse(path string, fromTime, toTime time.Time) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	seen := make(map[string]struct{})
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var msg historyMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil, fmt.Errorf("unmarshal history line: %w", err)
		}
		postedAt, err := time.Parse(time.RFC3339Nano, msg.TS)
		if err != nil {
			return nil, fmt.Errorf("parse ts %q: %w", msg.TS, err)
		}
		postedAt = postedAt.UTC()
		if !fromTime.IsZero() && postedAt.Before(fromTime) {
			continue
		}
		if !toTime.IsZero() && postedAt.After(toTime) {
			continue
		}
		for _, sig := range builtin.ParseTradingTheTrendMessage(msg.Text) {
			seen[sig.Ticker] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// AdvanceTo publishes every queued signal whose PostedAt <= clockTime.
func (s *Service) AdvanceTo(ctx context.Context, clockTime time.Time) (int, error) {
	published := 0
	for s.cursor < len(s.queue) {
		qs := s.queue[s.cursor]
		if qs.payload.PostedAt.After(clockTime) {
			break
		}
		evt := domain.NewBacktestEvent(
			domain.EventTradingTheTrendSignalReceived,
			s.tenantID,
			s.envMode,
			qs.payload.SignalID,
			qs.payload,
			clockTime,
		)
		if err := s.bus.Publish(ctx, evt); err != nil {
			return published, fmt.Errorf("publish %s: %w", qs.payload.SignalID, err)
		}
		s.cursor++
		published++
		s.stats.SignalsPublished++
	}
	return published, nil
}

// StatsSnapshot returns a copy of current counters.
func (s *Service) StatsSnapshot() Stats {
	return s.stats
}

func coerceRight(s string) (domain.OptionRight, error) {
	switch strings.ToUpper(s) {
	case "C":
		return domain.OptionRightCall, nil
	case "P":
		return domain.OptionRightPut, nil
	default:
		return "", fmt.Errorf("unknown right %q", s)
	}
}
