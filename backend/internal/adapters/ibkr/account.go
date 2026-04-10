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

// accountSummaryTags lists the tags we need for equity, buying power, and P&L.
// Used only for filtering AccountValues results.
var accountSummaryTags = map[string]struct{}{
	"NetLiquidation":      {},
	"BuyingPower":         {},
	"DayTradingBuyingPower": {},
	"RealizedPnL":         {},
	"UnrealizedPnL":       {},
	"PatternDayTrader":    {},
}

func (a *Adapter) cachedAccountSummary(ib ibClient) (ibsync.AccountSummary, error) {
	a.acctCache.mu.Lock()
	defer a.acctCache.mu.Unlock()

	if time.Since(a.acctCache.fetchedAt) < accountSummaryCacheTTL && len(a.acctCache.summary) > 0 {
		return a.acctCache.summary, nil
	}

	// Use AccountValues (reads from ibsync's startup ReqAccountUpdates subscription)
	// instead of ReqAccountSummary (creates a new IBKR server-side subscription on
	// every call but never cancels it, leaking subscriptions until IBKR returns
	// error 322 "Maximum number of account summary requests exceeded").
	// AccountValues is populated at connect time and continuously updated — no new
	// subscriptions, no leak.
	allVals := ib.AccountValues()
	var filtered ibsync.AccountSummary
	for _, v := range allVals {
		if _, ok := accountSummaryTags[v.Tag]; ok {
			filtered = append(filtered, ibsync.AccountValue(v))
		}
	}

	if len(filtered) == 0 {
		if len(a.acctCache.summary) > 0 {
			a.log.Warn().Msg("ibkr: AccountValues returned no matching tags, using stale cache")
			return a.acctCache.summary, nil
		}
		return nil, fmt.Errorf("ibkr: AccountValues returned no matching tags and no cache available")
	}
	a.acctCache.summary = filtered
	a.acctCache.fetchedAt = time.Now()
	return filtered, nil
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

