package copytradereplay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Service replays a file of historical Discord copytrade messages into the
// event bus so the production copytrade strategy can be backtested without
// touching its code paths. It bypasses the HTTP handler (whose freshness TTL
// would drop any message older than a couple minutes) and publishes
// EventCopytradeSignalReceived directly to the sync bus.
//
// Usage: New, Load, then call AdvanceTo(tickTime) at the end of every replay
// tick to drain any signals whose PostedAt <= tickTime.
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
	ByAction         map[domain.CopytradeAction]int
}

type queuedSignal struct {
	payload domain.CopytradeSignalPayload
	order   int // action priority for stable ordering on PostedAt ties
}

// historyMessage mirrors the JSONL shape emitted by scrape_history.py
// (services/discord-copytrade/state/history_*.jsonl).
type historyMessage struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	TS     string `json:"ts"`
	Text   string `json:"text"`
}

// New returns a ready-to-load Service bound to bus. tenantID/envMode are
// stamped on every published event; they must match the values the rest of
// the replay binary uses so downstream routing stays consistent.
func New(bus ports.EventBusPort, tenantID string, envMode domain.EnvMode, log zerolog.Logger) *Service {
	return &Service{
		log:      log,
		bus:      bus,
		tenantID: tenantID,
		envMode:  envMode,
		stats:    Stats{ByAction: make(map[domain.CopytradeAction]int)},
	}
}

// Load reads a JSONL history file, parses each message, filters to
// [fromTime, toTime] by PostedAt, and stores the resulting signal queue
// sorted by PostedAt ascending with a stable secondary key on action so
// same-minute BTO+STC pairs drain BTO-first. Zero-value fromTime/toTime
// disable the respective bound.
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

		parsed := ParseMessage(msg.Text, postedAt)
		if len(parsed) == 0 {
			s.stats.MessagesDropped++
			continue
		}
		for i, sig := range parsed {
			action, err := coerceAction(sig.Action)
			if err != nil {
				return s.stats, fmt.Errorf("%s line %d: %w", msg.ID, i, err)
			}
			right, err := coerceRight(sig.Right)
			if err != nil {
				return s.stats, fmt.Errorf("%s line %d: %w", msg.ID, i, err)
			}
			s.queue = append(s.queue, queuedSignal{
				payload: domain.CopytradeSignalPayload{
					SignalID:  fmt.Sprintf("%s:%d", msg.ID, i),
					MessageID: msg.ID,
					Author:    msg.Author,
					PostedAt:  postedAt,
					Action:    action,
					Ticker:    domain.Symbol(sig.Ticker),
					Expiry:    sig.Expiry,
					Strike:    sig.Strike,
					Right:     right,
					Price:     sig.Price,
					Tail:      sig.Tail,
					RawLine:   sig.RawLine,
				},
				order: actionOrder(action),
			})
			s.stats.SignalsLoaded++
			s.stats.ByAction[action]++
		}
	}
	if err := sc.Err(); err != nil {
		return s.stats, fmt.Errorf("scan: %w", err)
	}

	sort.SliceStable(s.queue, func(i, j int) bool {
		a, b := s.queue[i], s.queue[j]
		if !a.payload.PostedAt.Equal(b.payload.PostedAt) {
			return a.payload.PostedAt.Before(b.payload.PostedAt)
		}
		return a.order < b.order
	})

	s.log.Info().
		Int("messages_read", s.stats.MessagesRead).
		Int("messages_dropped", s.stats.MessagesDropped).
		Int("signals_loaded", s.stats.SignalsLoaded).
		Str("from", fromTime.Format(time.RFC3339)).
		Str("to", toTime.Format(time.RFC3339)).
		Msg("copytrade replay history loaded")

	return s.stats, nil
}

// Pending returns the number of signals still waiting to be published.
func (s *Service) Pending() int {
	return len(s.queue) - s.cursor
}

// AdvanceTo publishes every queued signal whose PostedAt <= clockTime, in
// load-sorted order. The caller (replay tick driver) must call this at tick
// END after fills have settled, so same-minute BTO/STC pairs line up with
// the Pending-flag semantics of the live copytrade strategy.
func (s *Service) AdvanceTo(ctx context.Context, clockTime time.Time) (int, error) {
	published := 0
	for s.cursor < len(s.queue) {
		qs := s.queue[s.cursor]
		if qs.payload.PostedAt.After(clockTime) {
			break
		}
		evt := domain.NewBacktestEvent(
			domain.EventCopytradeSignalReceived,
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

// StatsSnapshot returns a copy of current counters for reporting.
func (s *Service) StatsSnapshot() Stats {
	cp := s.stats
	cp.ByAction = make(map[domain.CopytradeAction]int, len(s.stats.ByAction))
	maps.Copy(cp.ByAction, s.stats.ByAction)
	return cp
}

func actionOrder(a domain.CopytradeAction) int {
	switch a {
	case domain.CopytradeActionBTO:
		return 0
	case domain.CopytradeActionAVG:
		return 1
	case domain.CopytradeActionSTC:
		return 2
	default:
		return 3
	}
}

func coerceAction(s string) (domain.CopytradeAction, error) {
	switch strings.ToUpper(s) {
	case "BTO":
		return domain.CopytradeActionBTO, nil
	case "STC":
		return domain.CopytradeActionSTC, nil
	case "AVG":
		return domain.CopytradeActionAVG, nil
	default:
		return "", fmt.Errorf("unknown action %q", s)
	}
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
