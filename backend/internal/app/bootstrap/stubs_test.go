package bootstrap

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/domain/dnaapproval"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

type stubEventBus struct{}

func (stubEventBus) Publish(context.Context, domain.Event) error { return nil }
func (stubEventBus) Subscribe(context.Context, domain.EventType, ports.EventHandler) error {
	return nil
}
func (stubEventBus) SubscribeAsync(context.Context, domain.EventType, ports.EventHandler) error {
	return nil
}
func (stubEventBus) Unsubscribe(context.Context, domain.EventType, ports.EventHandler) error {
	return nil
}
func (stubEventBus) Close() {}

type stubBroker struct{}

func (stubBroker) SubmitOrder(context.Context, domain.OrderIntent) (string, error) { return "", nil }
func (stubBroker) CancelOrder(context.Context, string) error                       { return nil }
func (stubBroker) CancelOpenOrders(context.Context, domain.Symbol, string) (int, error) {
	return 0, nil
}
func (stubBroker) CancelAllOpenOrders(context.Context) (int, error)       { return 0, nil }
func (stubBroker) GetOrderStatus(context.Context, string) (string, error) { return "filled", nil }
func (stubBroker) GetPositions(context.Context, string, domain.EnvMode) ([]domain.Trade, error) {
	return nil, nil
}
func (stubBroker) GetPosition(context.Context, domain.Symbol) (float64, error) { return 0, nil }
func (stubBroker) ClosePosition(context.Context, domain.Symbol) (string, error) {
	return "", nil
}
func (stubBroker) GetOrderDetails(context.Context, string) (ports.OrderDetails, error) {
	return ports.OrderDetails{}, nil
}
func (stubBroker) GetAccountEquity(context.Context) (float64, error) { return 100000, nil }

type stubRepo struct{}

func (stubRepo) SaveMarketBar(context.Context, domain.MarketBar) error { return nil }
func (stubRepo) GetMarketBars(context.Context, domain.Symbol, domain.Timeframe, time.Time, time.Time) ([]domain.MarketBar, error) {
	return nil, nil
}
func (stubRepo) SaveTrade(context.Context, domain.Trade) error { return nil }
func (stubRepo) GetTrades(context.Context, string, domain.EnvMode, time.Time, time.Time) ([]domain.Trade, error) {
	return nil, nil
}
func (stubRepo) UpdateTradeThesis(context.Context, string, domain.EnvMode, domain.Symbol, json.RawMessage) error {
	return nil
}
func (stubRepo) SaveStrategyDNA(context.Context, domain.StrategyDNA) error { return nil }
func (stubRepo) GetLatestStrategyDNA(context.Context, string, domain.EnvMode) (*domain.StrategyDNA, error) {
	return nil, nil
}
func (stubRepo) SaveOrder(context.Context, domain.BrokerOrder) error { return nil }
func (stubRepo) UpdateOrderFill(context.Context, string, time.Time, float64, float64) error {
	return nil
}
func (stubRepo) ListTrades(context.Context, ports.TradeQuery) (ports.TradePage, error) {
	return ports.TradePage{}, nil
}
func (stubRepo) ListOrders(context.Context, ports.OrderQuery) (ports.OrderPage, error) {
	return ports.OrderPage{}, nil
}
func (stubRepo) GetMaxBarHighSince(context.Context, domain.Symbol, domain.Timeframe, time.Time) (float64, error) {
	return 0, nil
}
func (stubRepo) SaveThoughtLog(context.Context, domain.ThoughtLog) error { return nil }
func (stubRepo) GetThoughtLogsByIntentID(context.Context, string) ([]domain.ThoughtLog, error) {
	return nil, nil
}
func (stubRepo) SaveDNAVersion(context.Context, dnaapproval.DNAVersion) error { return nil }
func (stubRepo) GetDNAVersion(context.Context, string) (*dnaapproval.DNAVersion, error) {
	return nil, nil
}
func (stubRepo) GetDNAVersionByHash(context.Context, string, string) (*dnaapproval.DNAVersion, error) {
	return nil, nil
}
func (stubRepo) SaveDNAApproval(context.Context, dnaapproval.DNAApproval) error { return nil }
func (stubRepo) UpdateDNAApproval(context.Context, string, dnaapproval.DNAStatus, string, string) error {
	return nil
}
func (stubRepo) GetDNAApproval(context.Context, string) (*dnaapproval.DNAApproval, error) {
	return nil, nil
}
func (stubRepo) ListPendingApprovals(context.Context) ([]dnaapproval.DNAApproval, error) {
	return nil, nil
}
func (stubRepo) GetActiveDNAVersion(context.Context, string) (*dnaapproval.DNAVersion, error) {
	return nil, nil
}
func (stubRepo) GetLatestThesisForSymbol(context.Context, string, domain.EnvMode, domain.Symbol) (json.RawMessage, error) {
	return nil, nil
}
func (stubRepo) GetNonTerminalOrders(context.Context, string, domain.EnvMode) ([]domain.BrokerOrder, error) {
	return nil, nil
}
func (stubRepo) GetRecordedFillQty(context.Context, string, domain.EnvMode, domain.Symbol, string, time.Time) (float64, error) {
	return 0, nil
}
func (stubRepo) UpdateOrderStatus(context.Context, string, string) error { return nil }
func (stubRepo) GetNetPositions(context.Context, string, domain.EnvMode) (map[domain.Symbol]float64, error) {
	return nil, nil
}
func (stubRepo) GetAvgEntryPrice(context.Context, string, domain.EnvMode, domain.Symbol) (float64, error) {
	return 0, nil
}
