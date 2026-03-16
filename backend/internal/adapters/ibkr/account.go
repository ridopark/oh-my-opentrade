package ibkr

import (
	"context"
	"fmt"
	"strconv"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

func (a *Adapter) GetAccountBuyingPower(_ context.Context) (ports.BuyingPower, error) {
	ib := a.conn.IB()
	if ib == nil {
		return ports.BuyingPower{}, fmt.Errorf("ibkr: not connected")
	}

	summary, err := ib.ReqAccountSummary("All", "BuyingPower,DayTradingBuyingPower,PatternDayTrader")
	if err != nil {
		return ports.BuyingPower{}, fmt.Errorf("ibkr: ReqAccountSummary: %w", err)
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

	summary, err := ib.ReqAccountSummary("All", "NetLiquidation")
	if err != nil {
		return 0, fmt.Errorf("ibkr: ReqAccountSummary: %w", err)
	}
	for _, v := range summary {
		if v.Tag == "NetLiquidation" {
			equity, err := strconv.ParseFloat(v.Value, 64)
			return equity, err
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
