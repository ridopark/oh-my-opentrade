package risk

import (
	"fmt"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/observability/metrics"
	"github.com/rs/zerolog"
)

// DailyPnLSource provides the cumulative daily realized P&L.
// Implemented by perf.LedgerWriter for fast in-memory lookups.
type DailyPnLSource interface {
	GetDailyRealizedPnL(tenantID string, envMode domain.EnvMode) float64
}

// DailyLossBreaker is a circuit breaker that halts trading when cumulative
// daily losses exceed configured thresholds. It checks both percentage-based
// and absolute USD limits.
//
// Usage pattern mirrors execution.KillSwitch: check before broker submission.
type DailyLossBreaker struct {
	maxLossPct float64 // e.g., 0.05 for 5%
	maxLossUSD float64 // absolute USD limit
	pnlSource  DailyPnLSource
	nowFunc    func() time.Time
	log        zerolog.Logger
	metrics    *metrics.Metrics

	mu       sync.Mutex
	haltDate string // YYYY-MM-DD when halted; empty = not halted
}

// NewDailyLossBreaker creates a circuit breaker that trips when daily loss
// exceeds maxLossPct (as fraction, e.g. 0.05 = 5%) of equity or maxLossUSD in absolute terms.
func NewDailyLossBreaker(maxLossPct, maxLossUSD float64, pnlSource DailyPnLSource, nowFunc func() time.Time, log zerolog.Logger) *DailyLossBreaker {
	return &DailyLossBreaker{
		maxLossPct: maxLossPct,
		maxLossUSD: maxLossUSD,
		pnlSource:  pnlSource,
		nowFunc:    nowFunc,
		log:        log,
	}
}

// SetMetrics injects Prometheus collectors. Safe to leave nil (no-op).
func (d *DailyLossBreaker) SetMetrics(m *metrics.Metrics) { d.metrics = m }

// Check evaluates whether trading should be halted for the given tenant.
// It returns an error if the daily loss circuit breaker is tripped.
// accountEquity is the current account equity used for percentage calculation.
func (d *DailyLossBreaker) Check(tenantID string, envMode domain.EnvMode, accountEquity float64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	today := d.nowFunc().UTC().Format("2006-01-02")

	// Reset halt if it's a new day.
	if d.haltDate != "" && d.haltDate != today {
		d.haltDate = ""
	}

	// If already halted today, reject immediately.
	if d.haltDate == today {
		if d.metrics != nil {
			d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "halted").Inc()
		}
		return fmt.Errorf("daily loss circuit breaker: trading halted for %s on %s", tenantID, today)
	}

	// Get cumulative realized P&L for today.
	dailyPnL := d.pnlSource.GetDailyRealizedPnL(tenantID, envMode)

	// Only check for losses (negative P&L).
	if dailyPnL >= 0 {
		if d.metrics != nil {
			d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "pass").Inc()
		}
		return nil
	}

	loss := -dailyPnL // positive number representing loss

	// Check absolute USD limit.
	if d.maxLossUSD > 0 && loss >= d.maxLossUSD {
		d.haltDate = today
		d.log.Warn().
			Float64("daily_loss", loss).
			Float64("max_loss_usd", d.maxLossUSD).
			Str("tenant_id", tenantID).
			Msg("daily loss circuit breaker tripped: absolute USD limit exceeded")
		if d.metrics != nil {
			d.metrics.Risk.CBTripsTotal.WithLabelValues("usd_limit").Inc()
			d.metrics.Risk.CBActive.WithLabelValues("daily_loss").Set(1)
			d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "tripped").Inc()
		}
		return fmt.Errorf("daily loss circuit breaker: loss $%.2f exceeds max $%.2f", loss, d.maxLossUSD)
	}

	// Check percentage limit.
	if d.maxLossPct > 0 && accountEquity > 0 {
		lossPct := loss / accountEquity
		if lossPct >= d.maxLossPct {
			d.haltDate = today
			d.log.Warn().
				Float64("daily_loss", loss).
				Float64("loss_pct", lossPct*100).
				Float64("max_loss_pct", d.maxLossPct*100).
				Float64("account_equity", accountEquity).
				Str("tenant_id", tenantID).
				Msg("daily loss circuit breaker tripped: percentage limit exceeded")
			if d.metrics != nil {
				d.metrics.Risk.CBTripsTotal.WithLabelValues("pct_limit").Inc()
				d.metrics.Risk.CBActive.WithLabelValues("daily_loss").Set(1)
				d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "tripped").Inc()
			}
			return fmt.Errorf("daily loss circuit breaker: loss %.2f%% exceeds max %.2f%%", lossPct*100, d.maxLossPct*100)
		}
	}

	if d.metrics != nil {
		d.metrics.Risk.ChecksTotal.WithLabelValues("daily_loss", "pass").Inc()
	}
	return nil
}

// IsHalted reports whether trading is currently halted for today.
func (d *DailyLossBreaker) IsHalted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	today := d.nowFunc().UTC().Format("2006-01-02")
	return d.haltDate == today
}

// Reset clears the halt state. Useful for testing.
func (d *DailyLossBreaker) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.haltDate = ""
}
