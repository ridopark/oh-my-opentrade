package copytradereplay

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Ledger subscribes to EventFillReceived filtered by strategy=copytrade_v1
// and writes one CSV row per fill to path. The output is the raw per-copier
// execution audit trail for a backtest run: pair with AuthorStatedLedger to
// measure how much of the author's stated edge survives real fill mechanics.
type Ledger struct {
	log zerolog.Logger

	mu     sync.Mutex
	f      *os.File
	w      *csv.Writer
	rows   int
	closed bool
}

// NewLedger opens path for writing and emits the CSV header. Caller must
// invoke Subscribe(ctx, bus) to start capturing fills, and Close() at
// shutdown. Returns a running ledger even if an existing file is present
// (the file is truncated on Open).
func NewLedger(path string, log zerolog.Logger) (*Ledger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("ledger: open %s: %w", path, err)
	}
	l := &Ledger{
		log: log,
		f:   f,
		w:   csv.NewWriter(f),
	}
	if err := l.w.Write([]string{
		"ts_filled",
		"contract_symbol",
		"side",
		"direction",
		"quantity",
		"price",
		"author",
		"signal_id",
		"copytrade_action",
		"generation",
		"ref_price",
		"option_right",
		"option_expiry",
	}); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ledger: write header: %w", err)
	}
	return l, nil
}

// Subscribe attaches the ledger to bus. Handler filters by
// strategy=copytrade_v1 and ignores other fills.
func (l *Ledger) Subscribe(ctx context.Context, bus ports.EventBusPort) error {
	return bus.Subscribe(ctx, domain.EventFillReceived, l.handle)
}

func (l *Ledger) handle(_ context.Context, ev domain.Event) error {
	payload, ok := ev.Payload.(map[string]any)
	if !ok {
		return nil
	}
	strategyID, _ := payload["strategy"].(string)
	if strategyID != "copytrade_v1" {
		return nil
	}

	tags, _ := payload["signal_tags"].(map[string]string)
	row := []string{
		tsAsString(payload["filled_at"]),
		stringOf(payload["symbol"]),
		stringOf(payload["side"]),
		stringOf(payload["direction"]),
		floatAsString(payload["quantity"]),
		floatAsString(payload["price"]),
		tags["author"],
		tags["signal_id"],
		tags["copytrade_action"],
		tags["generation"],
		tags["ref_price"],
		stringOf(payload["option_right"]),
		stringOf(payload["option_expiry"]),
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if err := l.w.Write(row); err != nil {
		l.log.Error().Err(err).Msg("copytrade ledger: write row failed")
		return nil
	}
	l.rows++
	return nil
}

// Rows returns the number of fill rows written so far.
func (l *Ledger) Rows() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rows
}

// Close flushes and closes the ledger file. Safe to call multiple times.
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.w.Flush()
	if err := l.w.Error(); err != nil {
		_ = l.f.Close()
		return fmt.Errorf("ledger: flush: %w", err)
	}
	return l.f.Close()
}

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func floatAsString(v any) string {
	if v == nil {
		return ""
	}
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', 6, 64)
	}
	return fmt.Sprint(v)
}

func tsAsString(v any) string {
	if v == nil {
		return ""
	}
	if t, ok := v.(time.Time); ok {
		return t.UTC().Format(time.RFC3339)
	}
	return fmt.Sprint(v)
}
