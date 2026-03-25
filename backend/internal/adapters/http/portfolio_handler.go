package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// PortfolioBroker is the subset of broker capabilities needed by the portfolio handler.
type PortfolioBroker interface {
	GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)
	ClosePosition(ctx context.Context, symbol domain.Symbol) (string, error)
	GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error)
}

// PortfolioHandler serves portfolio endpoints: positions, account summary, and close actions.
type PortfolioHandler struct {
	broker   PortfolioBroker
	account  ports.AccountPort
	equityFn func(ctx context.Context) (float64, error)
	tenantID string
	envMode  domain.EnvMode
	log      zerolog.Logger
}

// NewPortfolioHandler creates a new portfolio handler.
func NewPortfolioHandler(
	broker PortfolioBroker,
	account ports.AccountPort,
	equityFn func(ctx context.Context) (float64, error),
	tenantID string,
	envMode domain.EnvMode,
	log zerolog.Logger,
) *PortfolioHandler {
	return &PortfolioHandler{
		broker:   broker,
		account:  account,
		equityFn: equityFn,
		tenantID: tenantID,
		envMode:  envMode,
		log:      log.With().Str("component", "portfolio_http").Logger(),
	}
}

func (h *PortfolioHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/portfolio")
	path = strings.TrimPrefix(path, "/")

	switch {
	case path == "positions" && r.Method == http.MethodGet:
		h.handleGetPositions(w, r)
	case path == "positions" && r.Method == http.MethodDelete:
		h.handleCloseAll(w, r)
	case strings.HasPrefix(path, "positions/") && r.Method == http.MethodDelete:
		symbol := strings.TrimPrefix(path, "positions/")
		h.handleClosePosition(w, r, symbol)
	case path == "account" && r.Method == http.MethodGet:
		h.handleGetAccount(w, r)
	default:
		jsonErr(w, "not found", http.StatusNotFound)
	}
}

func (h *PortfolioHandler) handleGetPositions(w http.ResponseWriter, r *http.Request) {
	positions, err := h.broker.GetPositions(r.Context(), h.tenantID, h.envMode)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get positions")
		jsonErr(w, "failed to get positions: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type positionJSON struct {
		Symbol          string  `json:"symbol"`
		Side            string  `json:"side"`
		Quantity        float64 `json:"quantity"`
		AvgEntryPrice   float64 `json:"avg_entry_price"`
		CurrentPrice    float64 `json:"current_price"`
		MarketValue     float64 `json:"market_value"`
		UnrealizedPnl   float64 `json:"unrealized_pnl"`
		UnrealizedPnlPct float64 `json:"unrealized_pnl_pct"`
	}

	out := make([]positionJSON, 0, len(positions))
	for _, p := range positions {
		side := "long"
		if strings.EqualFold(p.Side, "short") || strings.EqualFold(p.Side, "sell") {
			side = "short"
		}
		currentPrice := p.Price // best available
		marketValue := p.Quantity * currentPrice
		pnl := 0.0
		pnlPct := 0.0
		if p.Price > 0 && p.Quantity > 0 {
			entryValue := p.Quantity * p.Price
			if side == "long" {
				pnl = marketValue - entryValue
			} else {
				pnl = entryValue - marketValue
			}
			if entryValue > 0 {
				pnlPct = (pnl / entryValue) * 100
			}
		}
		out = append(out, positionJSON{
			Symbol:          string(p.Symbol),
			Side:            side,
			Quantity:        p.Quantity,
			AvgEntryPrice:   p.Price,
			CurrentPrice:    currentPrice,
			MarketValue:     marketValue,
			UnrealizedPnl:   pnl,
			UnrealizedPnlPct: pnlPct,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"positions": out})
}

func (h *PortfolioHandler) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	equity := 0.0
	if h.equityFn != nil {
		if eq, err := h.equityFn(r.Context()); err == nil {
			equity = eq
		}
	}

	bp := ports.BuyingPower{}
	if h.account != nil {
		if bpRes, err := h.account.GetAccountBuyingPower(r.Context()); err == nil {
			bp = bpRes
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"equity":       equity,
		"buying_power": bp.EffectiveBuyingPower,
		"daily_pnl":    0.0,
		"daily_pnl_pct": 0.0,
	})
}

func (h *PortfolioHandler) handleClosePosition(w http.ResponseWriter, r *http.Request, symbol string) {
	sym := domain.Symbol(symbol)
	orderID, err := h.broker.ClosePosition(r.Context(), sym)
	if err != nil {
		h.log.Error().Err(err).Str("symbol", symbol).Msg("failed to close position")
		jsonErr(w, "failed to close "+symbol+": "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info().Str("symbol", symbol).Str("order_id", orderID).Msg("position close requested")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"symbol":   symbol,
		"order_id": orderID,
		"status":   "closing",
	})
}

func (h *PortfolioHandler) handleCloseAll(w http.ResponseWriter, r *http.Request) {
	positions, err := h.broker.GetPositions(r.Context(), h.tenantID, h.envMode)
	if err != nil {
		jsonErr(w, "failed to get positions: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type closeResult struct {
		Symbol  string `json:"symbol"`
		OrderID string `json:"order_id,omitempty"`
		Error   string `json:"error,omitempty"`
	}

	results := make([]closeResult, 0, len(positions))
	for _, p := range positions {
		orderID, closeErr := h.broker.ClosePosition(r.Context(), p.Symbol)
		if closeErr != nil {
			h.log.Error().Err(closeErr).Str("symbol", string(p.Symbol)).Msg("failed to close position")
			results = append(results, closeResult{Symbol: string(p.Symbol), Error: closeErr.Error()})
		} else {
			results = append(results, closeResult{Symbol: string(p.Symbol), OrderID: orderID})
		}
	}

	h.log.Info().Int("total", len(positions)).Msg("close all positions requested")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
