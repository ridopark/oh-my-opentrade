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

const allAccountTags = "NetLiquidation,BuyingPower,DayTradingBuyingPower,PatternDayTrader"

func (a *Adapter) cachedAccountSummary(ib ibClient) (ibsync.AccountSummary, error) {
	a.acctCache.mu.Lock()
	defer a.acctCache.mu.Unlock()

	if time.Since(a.acctCache.fetchedAt) < accountSummaryCacheTTL && len(a.acctCache.summary) > 0 {
		return a.acctCache.summary, nil
	}

	summary, err := ib.ReqAccountSummary("All", allAccountTags)
	if err != nil {
		if len(a.acctCache.summary) > 0 {
			a.log.Warn().Err(err).Msg("ibkr: ReqAccountSummary failed, using stale cache")
			return a.acctCache.summary, nil
		}
		return nil, fmt.Errorf("ibkr: ReqAccountSummary: %w", err)
	}
	a.acctCache.summary = summary
	a.acctCache.fetchedAt = time.Now()
	return summary, nil
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

func (a *Adapter) GetQuote(_ context.Context, symbol domain.Symbol) (bid float64, ask float64, err error) {
	ib := a.conn.IB()
	if ib == nil {
		return 0, 0, fmt.Errorf("ibkr: not connected")
	}

	contract := newContract(symbol)
	ticker, snapErr := ib.Snapshot(contract)
	if snapErr != nil {
		return 0, 0, fmt.Errorf("ibkr: snapshot for %s: %w", symbol, snapErr)
	}
	return ticker.Bid(), ticker.Ask(), nil
}

func (a *Adapter) GetOptionPrices(_ context.Context, _ []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error) {
	return nil, errDeferred
}
