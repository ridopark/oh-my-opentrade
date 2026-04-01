package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

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

// OptionQuoteProvider fetches bid/ask/last for option contract symbols.
type OptionQuoteProvider interface {
	GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error)
}

// LastPriceFn returns the last known close price for an equity symbol from in-memory bar data.
type LastPriceFn func(symbol string) (close float64, ok bool)

// PortfolioHandler serves portfolio endpoints: positions, account summary, and close actions.
type PortfolioHandler struct {
	broker   PortfolioBroker
	account  ports.AccountPort
	optQuoter   OptionQuoteProvider
	lastPriceFn LastPriceFn
	dailyPnLFn  func(ctx context.Context) (realized, unrealized float64, err error)
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

// SetOptionQuoteProvider enables option contract quotes via Alpaca.
func (h *PortfolioHandler) SetOptionQuoteProvider(q OptionQuoteProvider) { h.optQuoter = q }

// SetLastPriceFn provides in-memory price lookup as a fast fallback for equity quotes.
func (h *PortfolioHandler) SetLastPriceFn(fn LastPriceFn) { h.lastPriceFn = fn }

// SetDailyPnLFn provides a function to fetch daily realized + unrealized P&L.
func (h *PortfolioHandler) SetDailyPnLFn(fn func(ctx context.Context) (realized, unrealized float64, err error)) {
	h.dailyPnLFn = fn
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
	case strings.HasPrefix(path, "quote/") && r.Method == http.MethodGet:
		symbol := strings.TrimPrefix(path, "quote/")
		h.handleGetQuote(w, r, symbol)
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
		// Options fields (empty for equity positions)
		InstrumentType string  `json:"instrument_type,omitempty"` // "OPTION" or ""
		Underlying     string  `json:"underlying,omitempty"`
		Strike         float64 `json:"strike,omitempty"`
		OptionRight    string  `json:"option_right,omitempty"` // "CALL" or "PUT"
		Expiry         string  `json:"expiry,omitempty"`       // "2026-04-24"
		DTE            int     `json:"dte,omitempty"`
	}

	// Batch-fetch live option prices for all option positions
	var optionSymbols []domain.Symbol
	for _, p := range positions {
		if p.InstrumentType == domain.InstrumentTypeOption {
			optionSymbols = append(optionSymbols, p.Symbol)
		}
	}
	optionPrices := make(map[domain.Symbol]float64)
	if h.optQuoter != nil && len(optionSymbols) > 0 {
		if quotes, err := h.optQuoter.GetOptionPrices(r.Context(), optionSymbols); err == nil {
			for sym, q := range quotes {
				mid := (q.Bid + q.Ask) / 2
				if mid <= 0 {
					mid = q.Last
				}
				optionPrices[sym] = mid
			}
		}
	}

	out := make([]positionJSON, 0, len(positions))
	for _, p := range positions {
		side := "long"
		if strings.EqualFold(p.Side, "short") || strings.EqualFold(p.Side, "sell") {
			side = "short"
		}
		currentPrice := p.Price // entry price as fallback
		// Use live price if available
		if livePrice, ok := optionPrices[p.Symbol]; ok && livePrice > 0 {
			currentPrice = livePrice * 100 // options: price per share → per contract
		} else if h.lastPriceFn != nil {
			if eqPrice, ok := h.lastPriceFn(string(p.Symbol)); ok {
				currentPrice = eqPrice
			}
		}
		marketValue := p.Quantity * currentPrice
		entryValue := p.Quantity * p.Price
		pnl := 0.0
		pnlPct := 0.0
		if p.Price > 0 && p.Quantity > 0 {
			if side == "long" {
				pnl = marketValue - entryValue
			} else {
				pnl = entryValue - marketValue
			}
			if entryValue > 0 {
				pnlPct = (pnl / entryValue) * 100
			}
		}
		pj := positionJSON{
			Symbol:          string(p.Symbol),
			Side:            side,
			Quantity:        p.Quantity,
			AvgEntryPrice:   p.Price,
			CurrentPrice:    currentPrice,
			MarketValue:     marketValue,
			UnrealizedPnl:   pnl,
			UnrealizedPnlPct: pnlPct,
		}
		if p.InstrumentType == domain.InstrumentTypeOption {
			pj.InstrumentType = "OPTION"
			pj.Underlying = p.Underlying
			pj.Strike = p.Strike
			pj.OptionRight = p.OptionRight
			if !p.Expiry.IsZero() {
				pj.Expiry = p.Expiry.Format("2006-01-02")
				pj.DTE = int(time.Until(p.Expiry).Hours() / 24)
				if pj.DTE < 0 {
					pj.DTE = 0
				}
			}
		}
		out = append(out, pj)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })

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

	var dailyPnl, dailyPnlPct float64
	if h.dailyPnLFn != nil {
		realized, unrealized, pnlErr := h.dailyPnLFn(r.Context())
		if pnlErr == nil {
			dailyPnl = realized + unrealized
			if equity > 0 {
				dailyPnlPct = (dailyPnl / equity) * 100
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"equity":       equity,
		"buying_power": bp.EffectiveBuyingPower,
		"daily_pnl":    dailyPnl,
		"daily_pnl_pct": dailyPnlPct,
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

func (h *PortfolioHandler) handleGetQuote(w http.ResponseWriter, r *http.Request, symbol string) {
	sym := domain.Symbol(symbol)

	// For equities: use fast in-memory last bar price (no IBKR round-trip)
	if !domain.IsOCCSymbol(sym) {
		if h.lastPriceFn == nil {
			jsonErr(w, "price source not configured", http.StatusServiceUnavailable)
			return
		}
		close, ok := h.lastPriceFn(symbol)
		if !ok {
			jsonErr(w, "no price data for "+symbol, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"symbol": symbol,
			"bid":    close,
			"ask":    close,
			"mid":    close,
			"source": "bar",
		})
		return
	}

	// For options contracts: use Alpaca options quote
	if h.optQuoter == nil {
		jsonErr(w, "options quote provider not configured", http.StatusServiceUnavailable)
		return
	}
	quotes, err := h.optQuoter.GetOptionPrices(r.Context(), []domain.Symbol{sym})
	if err != nil {
		h.log.Warn().Err(err).Str("symbol", symbol).Msg("options quote failed")
		jsonErr(w, "quote failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	q, ok := quotes[sym]
	if !ok {
		jsonErr(w, "no quote for "+symbol, http.StatusNotFound)
		return
	}
	mid := (q.Bid + q.Ask) / 2
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"symbol": symbol,
		"bid":    q.Bid,
		"ask":    q.Ask,
		"mid":    mid,
		"last":   q.Last,
		"source": "alpaca",
	})
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
