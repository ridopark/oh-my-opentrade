package positionmonitor

import (
	"context"
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// openOrderLookbackWindow bounds how far back the startup reconciler looks
// for journal rows. 48h is more than enough to cover a weekend outage while
// avoiding loading thousands of stale dev-session rows.
const openOrderLookbackWindow = 48 * time.Hour

// reconcileOpenOrdersOnBoot cross-references broker-reported working orders
// against the Sprint 2 intent journal and decides whether to resume, alert
// on, or mark each one lost. Implements Phase B of SPRINT_2_PLAN.md.
//
// When the intent journal is nil (cfg.OrderJournalEnabled=false at the
// caller), this falls back to the legacy "cancel everything" behavior so
// deploys without the flag are byte-identical to pre-Sprint-2.
//
// The safety fallback: if EITHER the broker query OR the journal query
// fails, cancel-all is invoked. Correctness is preserved at the cost of
// canceling legitimate stops — no worse than today.
func (s *Service) reconcileOpenOrdersOnBoot(ctx context.Context) {
	// Feature gate is upstream (services.go reads cfg.OrderJournalEnabled
	// and only constructs the journal when true). A nil intentJournal here
	// therefore means "legacy path requested" — never an accidental nil.
	if s.intentJournal == nil {
		if canceled, err := s.broker.CancelAllOpenOrders(ctx); err != nil {
			s.log.Warn().Err(err).Msg("bootstrap: failed to cancel stale open orders — proceeding anyway")
		} else if canceled > 0 {
			s.log.Info().Int("canceled", canceled).Msg("bootstrap: canceled stale open orders from prior session")
		}
		return
	}

	brokerOpen, err := s.broker.GetOpenOrders(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("bootstrap: failed to query broker open orders — falling back to cancel-all for safety")
		s.notify(ctx, fmt.Sprintf("🚨 Startup reconcile: broker query failed (%v) — falling back to cancel-all", err))
		if _, cerr := s.broker.CancelAllOpenOrders(ctx); cerr != nil {
			s.log.Warn().Err(cerr).Msg("bootstrap: cancel-all fallback also failed")
		}
		return
	}

	journalRows, err := s.intentJournal.OpenIntents(ctx, s.tenantID, s.envMode, openOrderLookbackWindow)
	if err != nil {
		s.log.Error().Err(err).Msg("bootstrap: failed to load intent journal — falling back to cancel-all for safety")
		s.notify(ctx, fmt.Sprintf("🚨 Startup reconcile: journal query failed (%v) — falling back to cancel-all", err))
		if _, cerr := s.broker.CancelAllOpenOrders(ctx); cerr != nil {
			s.log.Warn().Err(cerr).Msg("bootstrap: cancel-all fallback also failed")
		}
		return
	}

	s.reconcileOpenOrders(ctx, brokerOpen, journalRows)
}

// reconcileOpenOrders is the pure-logic core of the bootstrap reconciler,
// split out so unit tests can drive it directly without touching real
// broker/journal implementations.
func (s *Service) reconcileOpenOrders(
	ctx context.Context,
	brokerOpen []ports.OpenOrder,
	journalRows []domain.OrderIntentJournalRow,
) (matched, unmanaged, lost int) {
	journalByBrokerID := make(map[string]domain.OrderIntentJournalRow, len(journalRows))
	for _, row := range journalRows {
		if row.BrokerOrderID != "" {
			journalByBrokerID[row.BrokerOrderID] = row
		}
	}
	brokerByID := make(map[string]ports.OpenOrder, len(brokerOpen))
	for _, o := range brokerOpen {
		brokerByID[o.BrokerOrderID] = o
	}

	var unmanagedList []ports.OpenOrder
	for _, o := range brokerOpen {
		if row, ok := journalByBrokerID[o.BrokerOrderID]; ok {
			// Case 1: broker has it, journal has it. Resume.
			s.resumeTracking(o, row)
			matched++
			continue
		}
		unmanagedList = append(unmanagedList, o)
	}
	if len(unmanagedList) > 0 {
		unmanaged = len(unmanagedList)
		s.log.Warn().
			Int("count", unmanaged).
			Msg("bootstrap: broker has open orders with no journal entry — leaving in place for operator review")
		for _, o := range unmanagedList {
			s.log.Warn().
				Str("broker_order_id", o.BrokerOrderID).
				Str("symbol", o.Symbol).
				Str("side", o.Side).
				Float64("qty", o.Quantity).
				Str("order_type", o.OrderType).
				Msg("bootstrap: unmanaged broker order (not in journal)")
		}
		s.notify(ctx, fmt.Sprintf("⚠️ Startup found %d unmanaged broker orders (not in journal). Review manually.", unmanaged))
	}

	for _, row := range journalRows {
		if row.Status != domain.OrderIntentJournalSubmitted || row.BrokerOrderID == "" {
			continue
		}
		if _, ok := brokerByID[row.BrokerOrderID]; ok {
			continue
		}
		// Case 3: journal has a submitted intent that the broker no longer
		// reports as working. Either it filled or was canceled while we were
		// down. The existing reconcileExecutions path will catch any actual
		// fill; here we just mark the row.
		if err := s.intentJournal.MarkIntentLost(ctx, row.ID, time.Now()); err != nil {
			s.log.Error().Err(err).Str("intent_id", row.ID.String()).Msg("bootstrap: failed to mark lost intent")
		}
		lost++
		s.log.Warn().
			Str("intent_id", row.ID.String()).
			Str("broker_order_id", row.BrokerOrderID).
			Str("symbol", string(row.Symbol)).
			Msg("bootstrap: journaled intent no longer present on broker — marked lost")
	}
	if lost > 0 {
		s.notify(ctx, fmt.Sprintf("⚠️ Startup reconcile: %d journaled intents no longer on broker (marked lost)", lost))
	}
	s.log.Info().
		Int("matched", matched).
		Int("unmanaged", unmanaged).
		Int("lost", lost).
		Msg("bootstrap: order reconciliation complete")
	return matched, unmanaged, lost
}

// resumeTracking is invoked for broker orders that match a journal row.
// The sprint plan notes that full re-registration into the execution
// service's pendingOrders map would require a significant refactor of
// that service's private state. The degraded alternative specified in
// the plan is to log the resume and rely on the existing fill-reconciler
// path (runReconciliationLoop + reconcileExecutions in execution.Service)
// to pick up any eventual fills through the orders table and order-stream
// channel. That is what we do here.
func (s *Service) resumeTracking(o ports.OpenOrder, row domain.OrderIntentJournalRow) {
	s.log.Info().
		Str("broker_order_id", o.BrokerOrderID).
		Str("symbol", o.Symbol).
		Str("side", o.Side).
		Float64("qty", o.Quantity).
		Str("journal_status", row.Status).
		Str("strategy", row.Strategy).
		Msg("bootstrap: resuming tracking of matched broker order (journal-backed) — existing reconciler will land fills")
}

// notify pushes an operator-facing alert through the injected NotifierPort
// (Discord/Telegram fan-out), falling back to a log warning when no notifier
// is wired. Sprint 2 shipped this as log-only because the position monitor
// did not own a notifier; Sprint 3 added WithNotifier and this implementation
// actually delivers the message.
//
// The log.Warn is unconditional so that operators watching the log stream
// always see the alert even when the notifier is configured — failing
// notifier delivery should never silently swallow a reconciliation signal.
func (s *Service) notify(ctx context.Context, msg string) {
	s.log.Warn().Str("alert", msg).Msg("bootstrap notify")
	if s.notifier == nil {
		return
	}
	if err := s.notifier.Notify(ctx, s.tenantID, msg); err != nil {
		s.log.Warn().Err(err).Msg("bootstrap: notifier delivery failed (alert logged only)")
	}
}
