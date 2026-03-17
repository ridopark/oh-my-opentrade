package ibkr

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/scmhub/ibsync"
)

func (a *Adapter) SubmitOrder(_ context.Context, intent domain.OrderIntent) (string, error) {
	ib := a.conn.IB()
	if ib == nil {
		return "", fmt.Errorf("ibkr: not connected")
	}
	if intent.Quantity <= 0 {
		return "", fmt.Errorf("ibkr: SubmitOrder: quantity must be positive, got %f", intent.Quantity)
	}

	a.log.Debug().
		Str("symbol", string(intent.Symbol)).
		Float64("qty", intent.Quantity).
		Str("direction", string(intent.Direction)).
		Msg("ibkr: SubmitOrder called")

	contract := newContract(intent.Symbol)

	qty := intent.Quantity
	isCrypto := intent.Symbol.IsCryptoSymbol()
	isFractional := !isCrypto && qty != math.Floor(qty)

	order := ibsync.NewOrder()
	order.Action = directionToAction(intent.Direction)
	order.OrderType = intentOrderType(intent.OrderType)
	order.TIF = intentTIF(intent.TimeInForce)
	order.RouteMarketableToBbo = 0

	switch {
	case isFractional && !a.cfg.PaperMode && intent.LimitPrice > 0:
		cashAmount := math.Round(qty*intent.LimitPrice*100) / 100
		order.CashQty = cashAmount
		order.TotalQuantity = ibsync.StringToDecimal("0")
		a.log.Info().Float64("cash_qty", cashAmount).Float64("shares", qty).Msg("ibkr: live fractional order via cashQty")
	case !isCrypto:
		qty = math.Floor(qty)
		if qty <= 0 {
			return "", fmt.Errorf("ibkr: equity order quantity rounds to zero (original: %f)", intent.Quantity)
		}
		order.TotalQuantity = ibsync.StringToDecimal(strconv.FormatFloat(qty, 'f', -1, 64))
	default:
		order.TotalQuantity = ibsync.StringToDecimal(strconv.FormatFloat(qty, 'f', -1, 64))
	}

	if order.OrderType == "LMT" || order.OrderType == "STP LMT" {
		order.LmtPrice = intent.LimitPrice
	}
	if order.OrderType == "STP LMT" {
		order.AuxPrice = intent.StopLoss
	}

	trade := ib.PlaceOrder(contract, order)
	if trade == nil {
		return "", fmt.Errorf("ibkr: PlaceOrder returned nil trade")
	}

	orderID := strconv.FormatInt(trade.Order.OrderID, 10)
	a.log.Info().
		Str("order_id", orderID).
		Str("symbol", string(intent.Symbol)).
		Str("action", order.Action).
		Float64("qty", qty).
		Float64("limit_price", intent.LimitPrice).
		Msg("ibkr: order placed")
	return orderID, nil
}

func (a *Adapter) CancelOrder(_ context.Context, orderID string) error {
	ib := a.conn.IB()
	if ib == nil {
		return fmt.Errorf("ibkr: not connected")
	}

	a.log.Debug().Str("order_id", orderID).Msg("ibkr: CancelOrder called")

	id, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return fmt.Errorf("ibkr: invalid orderID %q: %w", orderID, err)
	}

	for _, t := range ib.OpenTrades() {
		if t.Order.OrderID == id {
			ib.CancelOrder(t.Order, ibsync.NewOrderCancel())
			return nil
		}
	}
	return fmt.Errorf("ibkr: open order %s not found", orderID)
}

func (a *Adapter) CancelOpenOrders(_ context.Context, symbol domain.Symbol, side string) (int, error) {
	ib := a.conn.IB()
	if ib == nil {
		return 0, fmt.Errorf("ibkr: not connected")
	}

	sym := strings.ToUpper(string(symbol))
	action := strings.ToUpper(side)
	switch action {
	case "LONG":
		action = "BUY"
	case "SHORT":
		action = "SELL"
	}

	count := 0
	for _, t := range ib.OpenTrades() {
		if t.Contract == nil || t.Order == nil {
			continue
		}
		if strings.EqualFold(t.Contract.Symbol, sym) && strings.EqualFold(t.Order.Action, action) {
			ib.CancelOrder(t.Order, ibsync.NewOrderCancel())
			count++
		}
	}
	return count, nil
}

func (a *Adapter) CancelAllOpenOrders(_ context.Context) (int, error) {
	ib := a.conn.IB()
	if ib == nil {
		return 0, fmt.Errorf("ibkr: not connected")
	}
	open := ib.OpenTrades()
	ib.ReqGlobalCancel()
	return len(open), nil
}

func (a *Adapter) GetOrderStatus(_ context.Context, orderID string) (string, error) {
	ib := a.conn.IB()
	if ib == nil {
		return "", fmt.Errorf("ibkr: not connected")
	}

	id, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("ibkr: invalid orderID %q: %w", orderID, err)
	}

	for _, t := range ib.Trades() {
		if t.Order.OrderID == id {
			return mapStatus(t.OrderStatus.Status), nil
		}
	}
	return "", fmt.Errorf("ibkr: order %s not found", orderID)
}

func (a *Adapter) GetOrderDetails(_ context.Context, orderID string) (ports.OrderDetails, error) {
	ib := a.conn.IB()
	if ib == nil {
		return ports.OrderDetails{}, fmt.Errorf("ibkr: not connected")
	}

	id, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return ports.OrderDetails{}, fmt.Errorf("ibkr: invalid orderID %q: %w", orderID, err)
	}

	for _, t := range ib.Trades() {
		if t.Order.OrderID != id {
			continue
		}
		os := t.OrderStatus
		details := ports.OrderDetails{
			BrokerOrderID:  orderID,
			Status:         mapStatus(os.Status),
			FilledQty:      os.Filled.Float(),
			FilledAvgPrice: os.AvgFillPrice,
			Symbol:         t.Contract.Symbol,
			Side:           t.Order.Action,
			Qty:            t.Order.TotalQuantity.Float(),
		}
		fills := t.Fills()
		if len(fills) > 0 {
			details.FilledAt = fills[len(fills)-1].Time
		}
		return details, nil
	}
	return ports.OrderDetails{}, fmt.Errorf("ibkr: order %s not found", orderID)
}

func (a *Adapter) GetPositions(_ context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error) {
	ib := a.conn.IB()
	if ib == nil {
		return nil, fmt.Errorf("ibkr: not connected")
	}

	positions := ib.Positions()
	trades := make([]domain.Trade, 0, len(positions))
	for _, p := range positions {
		if a.cfg.AccountID != "" && p.Account != a.cfg.AccountID {
			a.log.Debug().
				Str("account", p.Account).
				Str("wanted", a.cfg.AccountID).
				Msg("ibkr: skipping position from different account")
			continue
		}
		qty := p.Position.Float()
		if qty == 0 {
			continue
		}
		side := "BUY"
		if qty < 0 {
			side = "SELL"
			qty = -qty
		}
		trades = append(trades, domain.Trade{
			Time:     time.Now(),
			TenantID: tenantID,
			EnvMode:  envMode,
			Symbol:   domain.Symbol(p.Contract.Symbol),
			Side:     side,
			Quantity: qty,
			Price:    p.AvgCost,
			Status:   "FILLED",
			Strategy: "unknown",
		})
	}
	return trades, nil
}

func (a *Adapter) GetPosition(_ context.Context, symbol domain.Symbol) (float64, error) {
	ib := a.conn.IB()
	if ib == nil {
		return 0, fmt.Errorf("ibkr: not connected")
	}

	sym := strings.ToUpper(string(symbol))
	for _, p := range ib.Positions() {
		if strings.EqualFold(p.Contract.Symbol, sym) {
			return p.Position.Float(), nil
		}
	}
	return 0, nil
}

func (a *Adapter) ClosePosition(_ context.Context, symbol domain.Symbol) (string, error) {
	ib := a.conn.IB()
	if ib == nil {
		return "", fmt.Errorf("ibkr: not connected")
	}

	sym := strings.ToUpper(string(symbol))
	var qty float64
	for _, p := range ib.Positions() {
		if strings.EqualFold(p.Contract.Symbol, sym) {
			qty = p.Position.Float()
			break
		}
	}
	if qty == 0 {
		return "", nil
	}

	action := "SELL"
	if qty < 0 {
		action = "BUY"
		qty = -qty
	}

	contract := newContract(symbol)
	order := &ibsync.Order{}
	order.Action = action
	order.TotalQuantity = ibsync.StringToDecimal(strconv.FormatFloat(qty, 'f', -1, 64))
	order.OrderType = "MKT"
	order.TIF = "DAY"

	trade := ib.PlaceOrder(contract, order)
	if trade == nil {
		return "", fmt.Errorf("ibkr: ClosePosition PlaceOrder returned nil")
	}
	return strconv.FormatInt(trade.Order.OrderID, 10), nil
}

func directionToAction(d domain.Direction) string {
	switch d {
	case domain.DirectionLong:
		return "BUY"
	default:
		return "SELL"
	}
}

func intentOrderType(ot string) string {
	switch strings.ToLower(ot) {
	case "market":
		return "MKT"
	case "stop_limit":
		return "STP LMT"
	default:
		return "LMT"
	}
}

func intentTIF(tif string) string {
	switch strings.ToLower(tif) {
	case "day":
		return "DAY"
	case "ioc":
		return "IOC"
	default:
		return "GTC"
	}
}

func mapStatus(s ibsync.Status) string {
	switch s {
	case ibsync.Filled:
		return "filled"
	case ibsync.Cancelled, ibsync.ApiCancelled: //nolint:misspell // external ibsync constant
		return "canceled"
	case ibsync.Inactive:
		return "expired"
	case ibsync.Submitted:
		return "new"
	case ibsync.PreSubmitted:
		return "accepted"
	case ibsync.PendingSubmit, ibsync.ApiPending:
		return "pending_new"
	case ibsync.PendingCancel:
		return "pending_cancel"
	default:
		return strings.ToLower(string(s))
	}
}
