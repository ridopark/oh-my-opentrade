package noop

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

// NoopRepo implements ports.RepositoryPort as a no-op for backtest/test mode.
// Execution service calls SaveOrder/UpdateOrderFill/SaveTrade which we discard.
type NoopRepo struct{}

// Compile-time assertion that NoopRepo implements ports.RepositoryPort.
var _ ports.RepositoryPort = (*NoopRepo)(nil)

func (n *NoopRepo) SaveMarketBar(_ context.Context, _ domain.MarketBar) error { return nil }

func (n *NoopRepo) SaveMarketBars(_ context.Context, _ []domain.MarketBar) (int, error) {
	return 0, nil
}

func (n *NoopRepo) GetMarketBars(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _, _ time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}

func (n *NoopRepo) GetMarketBarsMulti(_ context.Context, _ []domain.Symbol, _ domain.Timeframe, _, _ time.Time) (map[string][]domain.MarketBar, error) {
	return map[string][]domain.MarketBar{}, nil
}

func (n *NoopRepo) SaveTrade(_ context.Context, _ domain.Trade) error { return nil }

func (n *NoopRepo) GetTrades(_ context.Context, _ string, _ domain.EnvMode, _, _ time.Time) ([]domain.Trade, error) {
	return nil, nil
}

func (n *NoopRepo) SaveStrategyDNA(_ context.Context, _ domain.StrategyDNA) error { return nil }

func (n *NoopRepo) GetLatestStrategyDNA(_ context.Context, _ string, _ domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}

func (n *NoopRepo) SaveOrder(_ context.Context, _ domain.BrokerOrder) error { return nil }

func (n *NoopRepo) UpdateOrderFill(_ context.Context, _ string, _ time.Time, _, _ float64) error {
	return nil
}

func (n *NoopRepo) RecordFill(_ context.Context, _ string, _ time.Time, _, _ float64, _ domain.Trade) error {
	return nil
}

func (n *NoopRepo) ListTrades(_ context.Context, _ ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}

func (n *NoopRepo) ListOrders(_ context.Context, _ ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}

func (n *NoopRepo) SaveThoughtLog(_ context.Context, _ domain.ThoughtLog) error { return nil }

func (n *NoopRepo) GetThoughtLogsByIntentID(_ context.Context, _ string) ([]domain.ThoughtLog, error) {
	return nil, nil
}

func (n *NoopRepo) UpdateTradeThesis(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ json.RawMessage) error {
	return nil
}

func (n *NoopRepo) GetMaxBarHighSince(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time) (float64, error) {
	return 0, nil
}

func (n *NoopRepo) GetLatestThesisForSymbol(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}

func (n *NoopRepo) GetNonTerminalOrders(_ context.Context, _ string, _ domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}

func (n *NoopRepo) GetOrderByBrokerOrderID(_ context.Context, _ string) (*domain.BrokerOrder, error) {
	return nil, nil
}

func (n *NoopRepo) GetRecordedFillQty(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol, _ string, _ time.Time) (float64, error) {
	return 0, nil
}

func (n *NoopRepo) GetRecordedExecutionIDs(_ context.Context, _ string, _ domain.EnvMode, _ time.Time) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func (n *NoopRepo) UpdateOrderStatus(_ context.Context, _ string, _ string) error { return nil }

func (n *NoopRepo) GetNetPositions(_ context.Context, _ string, _ domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}

func (n *NoopRepo) GetAvgEntryPrice(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (float64, error) {
	return 0, nil
}

func (n *NoopRepo) HasCanceledExitOrder(_ context.Context, _ string, _ domain.EnvMode, _ domain.Symbol) (bool, error) {
	return false, nil
}

func (n *NoopRepo) UpdateBarIndicators(_ context.Context, _ domain.Symbol, _ domain.Timeframe, _ time.Time, _, _, _, _ float64, _ map[string]float64) error {
	return nil
}
