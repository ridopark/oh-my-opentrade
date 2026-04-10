package ibkr

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/scmhub/ibsync"
)

// accountSummaryTags are the tags we need for equity, buying power, and P&L.
const accountSummaryTags = "NetLiquidation,BuyingPower,DayTradingBuyingPower,RealizedPnL,UnrealizedPnL,PatternDayTrader"

func (a *Adapter) cachedAccountSummary(ib ibClient) (ibsync.AccountSummary, error) {
	a.acctCache.mu.Lock()
	defer a.acctCache.mu.Unlock()

	if time.Since(a.acctCache.fetchedAt) < accountSummaryCacheTTL && len(a.acctCache.summary) > 0 {
		return a.acctCache.summary, nil
	}

	// Use ReqAccountSummary (request/response) instead of AccountSummary (cached + PubSub).
	// AccountSummary() internally creates an ibsync PubSub subscription that blocks the
	// EReader goroutine when the channel isn't drained, deadlocking the entire IBKR connection.
	// ReqAccountSummary subscribes, drains until "end", and unsubscribes — safe under load.
	type summaryResult struct {
		data ibsync.AccountSummary
		err  error
	}
	ch := make(chan summaryResult, 1)
	go func() {
		data, err := ib.ReqAccountSummary("All", accountSummaryTags)
		ch <- summaryResult{data: data, err: err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			if len(a.acctCache.summary) > 0 {
				a.log.Warn().Err(r.err).Msg("ibkr: ReqAccountSummary failed, using stale cache")
				return a.acctCache.summary, nil
			}
			return nil, fmt.Errorf("ibkr: ReqAccountSummary: %w", r.err)
		}
		if len(r.data) == 0 {
			if len(a.acctCache.summary) > 0 {
				a.log.Warn().Msg("ibkr: ReqAccountSummary returned empty, using stale cache")
				return a.acctCache.summary, nil
			}
			return nil, fmt.Errorf("ibkr: ReqAccountSummary returned empty")
		}
		a.acctCache.summary = r.data
		a.acctCache.fetchedAt = time.Now()
		return r.data, nil
	case <-time.After(5 * time.Second):
		if len(a.acctCache.summary) > 0 {
			a.log.Warn().Msg("ibkr: ReqAccountSummary timed out, using stale cache")
			return a.acctCache.summary, nil
		}
		return nil, fmt.Errorf("ibkr: ReqAccountSummary timed out and no cache available")
	}
}

func (a *Adapter) GetAccountBuyingPower(_ context.Context) (ports.BuyingPower, error) {
	ib := a.conn.IB()
	if ib == nil {
		return ports.BuyingPower{}, fmt.Errorf("ibkr: not connected")
	}

	summary, err := a.cachedAccountSummary(ib)
	if err != nil {
		return ports.BuyingPower{}, err
	}

	var bp ports.BuyingPower
	for _, v := range summary {
		switch v.Tag {
		case "BuyingPower":
			bp.EffectiveBuyingPower, _ = strconv.ParseFloat(v.Value, 64)
		case "DayTradingBuyingPower":
			bp.DayTradingBuyingPower, _ = strconv.ParseFloat(v.Value, 64)
		case "PatternDayTrader":
			bp.PatternDayTrader = v.Value == "1" || v.Value == "Y"
		}
	}
	return bp, nil
}

// GetDailyPnL returns realized + unrealized P&L for the day from IBKR account summary.
func (a *Adapter) GetDailyPnL(_ context.Context) (realized float64, unrealized float64, err error) {
	ib := a.conn.IB()
	if ib == nil {
		return 0, 0, fmt.Errorf("ibkr: not connected")
	}
	summary, sErr := a.cachedAccountSummary(ib)
	if sErr != nil {
		return 0, 0, sErr
	}
	for _, v := range summary {
		switch v.Tag {
		case "RealizedPnL":
			realized, _ = strconv.ParseFloat(v.Value, 64)
		case "UnrealizedPnL":
			unrealized, _ = strconv.ParseFloat(v.Value, 64)
		}
	}
	return realized, unrealized, nil
}

func (a *Adapter) GetAccountEquity(_ context.Context) (float64, error) {
	ib := a.conn.IB()
	if ib == nil {
		return 0, fmt.Errorf("ibkr: not connected")
	}

	summary, err := a.cachedAccountSummary(ib)
	if err != nil {
		return 0, err
	}
	for _, v := range summary {
		if v.Tag == "NetLiquidation" {
			return strconv.ParseFloat(v.Value, 64)
		}
	}
	return 0, fmt.Errorf("ibkr: NetLiquidation tag not found in account summary")
}

func (a *Adapter) GetQuote(ctx context.Context, symbol domain.Symbol) (bid float64, ask float64, err error) {
	ib := a.conn.IB()
	if ib == nil {
		return 0, 0, fmt.Errorf("ibkr: not connected")
	}

	contract := newContract(symbol)

	type result struct {
		bid, ask float64
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		ticker, snapErr := ib.Snapshot(contract)
		if snapErr != nil {
			ch <- result{err: fmt.Errorf("ibkr: snapshot for %s: %w", symbol, snapErr)}
			return
		}
		ch <- result{bid: ticker.Bid(), ask: ticker.Ask()}
	}()

	select {
	case r := <-ch:
		return r.bid, r.ask, r.err
	case <-ctx.Done():
		return 0, 0, fmt.Errorf("ibkr: snapshot for %s: %w", symbol, ctx.Err())
	case <-time.After(5 * time.Second):
		return 0, 0, fmt.Errorf("ibkr: snapshot for %s: timeout after 5s", symbol)
	}
}

