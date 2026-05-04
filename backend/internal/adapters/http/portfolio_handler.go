package http

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// PortfolioBroker is the subset of broker capabilities needed by the portfolio handler.
type PortfolioBroker interface {
	GetPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)
	GetFreshPositions(ctx context.Context, tenantID string, envMode domain.EnvMode) ([]domain.Trade, error)
	CloseAtMarket(ctx context.Context, symbol domain.Symbol) (string, error)
	GetPosition(ctx context.Context, symbol domain.Symbol) (float64, error)
	CancelOpenOrders(ctx context.Context, symbol domain.Symbol, side string) (int, error)
	RefreshPositions()
}

// OptionQuoteProvider fetches bid/ask/last for option contract symbols.
type OptionQuoteProvider interface {
	GetOptionPrices(ctx context.Context, symbols []domain.Symbol) (map[domain.Symbol]domain.OptionQuote, error)
}

// LastPriceFn returns the last known close price for an equity symbol from in-memory bar data.
type LastPriceFn func(symbol string) (close float64, ok bool)

// PositionMonitorReader is the read-only view of positionmonitor.Service that
// the portfolio HTTP handler needs. Narrow on purpose: keeps the http adapter
// from importing the full positionmonitor package and lets the handler stay
// nil-safe when the monitor isn't wired (e.g. backtest binaries).
type PositionMonitorReader interface {
	ListPositions() []domain.MonitoredPosition
	BootstrapReady() bool
}

// PortfolioHandler serves portfolio endpoints: positions, account summary, and close actions.
type PortfolioHandler struct {
	broker   PortfolioBroker
	account  ports.AccountPort
	optQuoter    OptionQuoteProvider
	lastPriceFn  LastPriceFn
	dailyPnLFn   func(ctx context.Context) (realized, unrealized float64, err error)
	repo         ports.RepositoryPort // for trade history lookups
	pendingClose map[string]time.Time // symbol → when close was requested
	posMonitor   PositionMonitorReader // optional; nil → /monitored returns 503
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

// SetRepo provides a repository for trade history lookups (opened_at times).
func (h *PortfolioHandler) SetRepo(r ports.RepositoryPort) { h.repo = r }

// SetPositionMonitor wires the OMO position monitor read view consumed by
// GET /api/portfolio/monitored. Optional: when nil, that route returns 503.
func (h *PortfolioHandler) SetPositionMonitor(m PositionMonitorReader) { h.posMonitor = m }

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
	case path == "monitored" && r.Method == http.MethodGet:
		h.handleGetMonitored(w, r)
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
	positions, err := h.broker.GetFreshPositions(r.Context(), h.tenantID, h.envMode)
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
		// Timing
		OpenedAt string `json:"opened_at,omitempty"` // RFC3339 when position was opened
		// Options fields (empty for equity positions)
		InstrumentType string  `json:"instrument_type,omitempty"` // "OPTION" or ""
		Underlying     string  `json:"underlying,omitempty"`
		Strike         float64 `json:"strike,omitempty"`
		OptionRight    string  `json:"option_right,omitempty"` // "CALL" or "PUT"
		Expiry         string  `json:"expiry,omitempty"`       // "2026-04-24"
		DTE            int     `json:"dte,omitempty"`
		Closing        bool    `json:"closing,omitempty"` // true when a close order is pending
	}

	// Look up entry times from trade history
	entryTimes := make(map[string]time.Time)
	if h.repo != nil {
		now := time.Now().UTC()
		trades, tErr := h.repo.GetTrades(r.Context(), h.tenantID, h.envMode, now.Add(-30*24*time.Hour), now)
		if tErr == nil {
			for _, t := range trades {
				sym := string(t.Symbol)
				if strings.EqualFold(t.Side, "BUY") {
					if existing, ok := entryTimes[sym]; !ok || t.Time.Before(existing) {
						entryTimes[sym] = t.Time
					}
				}
			}
		}
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
		// Both currentPrice and p.Price stay in per-share units — matches
		// IBKR's "Avg Price" column convention. The 100x contract
		// multiplier is applied once, inside marketValue/entryValue, so
		// the two sides share scale and P&L doesn't inflate 100x.
		currentPrice := p.Price
		if livePrice, ok := optionPrices[p.Symbol]; ok && livePrice > 0 {
			currentPrice = livePrice
		} else if h.lastPriceFn != nil {
			if eqPrice, ok := h.lastPriceFn(string(p.Symbol)); ok {
				currentPrice = eqPrice
			}
		}
		multiplier := 1.0
		if p.InstrumentType == domain.InstrumentTypeOption {
			multiplier = 100.0
		}
		marketValue := p.Quantity * currentPrice * multiplier
		entryValue := p.Quantity * p.Price * multiplier
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
		// Set entry time from trade history
		if t, ok := entryTimes[string(p.Symbol)]; ok {
			pj.OpenedAt = t.UTC().Format(time.RFC3339)
		}
		// Mark positions with pending close orders (expire after 5 minutes)
		if h.pendingClose != nil {
			if t, ok := h.pendingClose[string(p.Symbol)]; ok {
				if time.Since(t) < 5*time.Minute {
					pj.Closing = true
				} else {
					delete(h.pendingClose, string(p.Symbol))
				}
			}
		}
		out = append(out, pj)
	}

	// Clean up pending close entries for positions that no longer exist
	if h.pendingClose != nil {
		posSymbols := make(map[string]bool, len(positions))
		for _, p := range positions {
			posSymbols[string(p.Symbol)] = true
		}
		for sym := range h.pendingClose {
			if !posSymbols[sym] {
				delete(h.pendingClose, sym)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"positions": out})
}

// handleGetMonitored returns the OMO position monitor's view of open positions.
// The dashboard outer-joins this with /api/portfolio/positions to surface
// drift between broker reality and OMO's monitored set.
//
// Response is a private snake_case struct rather than a direct encode of
// domain.MonitoredPosition: the domain type's tags are inconsistent, and
// its CustomState/PendingExitOrderIDs maps are shared with the live monitor
// — the encoder's reflective iteration would race the tick loop.
func (h *PortfolioHandler) handleGetMonitored(w http.ResponseWriter, r *http.Request) {
	if h.posMonitor == nil {
		jsonErr(w, "position monitor not configured", http.StatusServiceUnavailable)
		return
	}

	type monitoredJSON struct {
		Symbol         string   `json:"symbol"`
		Strategy       string   `json:"strategy"`
		Side           string   `json:"side"`
		Quantity       float64  `json:"quantity"`
		EntryPrice     float64  `json:"entry_price"`
		HighWaterMark  float64  `json:"high_water_mark"`
		LowWaterMark   float64  `json:"low_water_mark"`
		EntryTime      string   `json:"entry_time"`
		ExitRules      []string `json:"exit_rules"`
		InstrumentType string   `json:"instrument_type"`
		Underlying     string   `json:"underlying"`
		Strike         *float64 `json:"strike,omitempty"`
		OptionRight    string   `json:"option_right,omitempty"`
		Expiry         string   `json:"expiry,omitempty"`
		DTE            *int     `json:"dte,omitempty"`
		IVAtEntry      *float64 `json:"iv_at_entry,omitempty"`
		AssetClass     string   `json:"asset_class"`
		// Clean-trigger estimate from PREMIUM_STOP only; nil when no
		// PREMIUM_STOP rule is attached (or position is non-option).
		// Excludes slippage, gap risk, and time-based exits — see UI tooltip.
		EstMaxLossUSD *float64 `json:"est_max_loss_usd,omitempty"`
	}

	positions := h.posMonitor.ListPositions()
	out := make([]monitoredJSON, 0, len(positions))
	for _, p := range positions {
		ruleNames := make([]string, 0, len(p.ExitRules))
		for _, rule := range p.ExitRules {
			ruleNames = append(ruleNames, string(rule.Type))
		}
		mj := monitoredJSON{
			Symbol:         string(p.Symbol),
			Strategy:       p.Strategy,
			Side:           p.Side,
			Quantity:       p.Quantity,
			EntryPrice:     p.EntryPrice,
			HighWaterMark:  p.HighWaterMark,
			LowWaterMark:   p.LowWaterMark,
			EntryTime:      p.EntryTime.UTC().Format(time.RFC3339),
			ExitRules:      ruleNames,
			InstrumentType: string(p.InstrumentType),
			AssetClass:     string(p.AssetClass),
		}
		if p.InstrumentType == domain.InstrumentTypeOption {
			mj.Underlying = string(domain.UnderlyingFromOCC(p.Symbol))
			mj.OptionRight = p.OptionRight
			if !p.OptionExpiry.IsZero() {
				mj.Expiry = p.OptionExpiry.Format("2006-01-02")
				dte := int(time.Until(p.OptionExpiry).Hours() / 24)
				if dte < 0 {
					dte = 0
				}
				mj.DTE = &dte
			}
			if v, ok := p.CustomState["strike"]; ok {
				strike := v
				mj.Strike = &strike
			}
			if v, ok := p.CustomState["iv_at_entry"]; ok {
				iv := v
				mj.IVAtEntry = &iv
			}
			if entry, ok := p.CustomState["option_premium"]; ok && entry > 0 {
				for _, rule := range p.ExitRules {
					if rule.Type == domain.ExitRulePremiumStop {
						if t, exists := rule.Params["threshold"]; exists && t > 0 {
							loss := entry * t * p.Quantity * 100
							mj.EstMaxLossUSD = &loss
						}
						break
					}
				}
			}
		} else {
			mj.Underlying = string(p.Symbol)
		}
		out = append(out, mj)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"bootstrap_complete": h.posMonitor.BootstrapReady(),
		"monitored":          out,
	})
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
	// Capture signed position before the close so we can record side + quantity.
	signedQty, posErr := h.broker.GetPosition(r.Context(), sym)
	if posErr != nil {
		h.log.Warn().Err(posErr).Str("symbol", symbol).Msg("failed to read position before close")
	}
	// Cancel any existing open sell orders to avoid stacking duplicates
	if canceled, cancelErr := h.broker.CancelOpenOrders(r.Context(), sym, "sell"); cancelErr == nil && canceled > 0 {
		h.log.Info().Str("symbol", symbol).Int("canceled", canceled).Msg("canceled existing sell orders before close")
	}
	orderID, err := h.broker.CloseAtMarket(r.Context(), sym)
	if err != nil {
		h.log.Error().Err(err).Str("symbol", symbol).Msg("failed to close position")
		jsonErr(w, "failed to close "+symbol+": "+err.Error(), http.StatusInternalServerError)
		return
	}

	if h.pendingClose == nil {
		h.pendingClose = make(map[string]time.Time)
	}
	h.pendingClose[symbol] = time.Now()

	h.recordManualClose(r.Context(), sym, signedQty, orderID)

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
		signedQty, posErr := h.broker.GetPosition(r.Context(), p.Symbol)
		if posErr != nil {
			h.log.Warn().Err(posErr).Str("symbol", string(p.Symbol)).Msg("failed to read position before close")
		}
		// Cancel existing sell orders first
		_, _ = h.broker.CancelOpenOrders(r.Context(), p.Symbol, "sell")
		orderID, closeErr := h.broker.CloseAtMarket(r.Context(), p.Symbol)
		if closeErr != nil {
			h.log.Error().Err(closeErr).Str("symbol", string(p.Symbol)).Msg("failed to close position")
			results = append(results, closeResult{Symbol: string(p.Symbol), Error: closeErr.Error()})
		} else {
			results = append(results, closeResult{Symbol: string(p.Symbol), OrderID: orderID})
			if h.pendingClose == nil {
				h.pendingClose = make(map[string]time.Time)
			}
			h.pendingClose[string(p.Symbol)] = time.Now()
			h.recordManualClose(r.Context(), p.Symbol, signedQty, orderID)
		}
	}

	h.log.Info().Int("total", len(positions)).Msg("close all positions requested")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
}

// recordManualClose persists a synthetic BrokerOrder row so manual closes
// appear on the Execution Monitor alongside strategy-driven executions.
// The orderID is the broker order ID returned by CloseAtMarket; when the
// fill lands, the existing fill-recording path will update this row in
// place via broker_order_id (see repository.queryInsertOrder ON CONFLICT).
func (h *PortfolioHandler) recordManualClose(ctx context.Context, sym domain.Symbol, signedQty float64, orderID string) {
	if h.repo == nil || orderID == "" || signedQty == 0 {
		return
	}
	side := "SELL"
	if signedQty < 0 {
		side = "BUY"
	}
	order := domain.BrokerOrder{
		Time:          time.Now(),
		TenantID:      h.tenantID,
		EnvMode:       h.envMode,
		IntentID:      uuid.New(),
		BrokerOrderID: orderID,
		Symbol:        sym,
		Side:          side,
		Quantity:      math.Abs(signedQty),
		Status:        "submitted",
		Strategy:      "manual",
		Rationale:     "Manual close via portfolio UI",
	}
	if domain.IsOCCSymbol(sym) {
		underlying, expiry, right, strike, ok := domain.ParseOCC(sym)
		if ok {
			order.InstrumentType = domain.InstrumentTypeOption
			order.OptionSymbol = string(sym)
			order.Underlying = underlying
			order.Strike = strike
			order.Expiry = expiry
			order.OptionRight = right
		}
	} else {
		order.InstrumentType = domain.InstrumentTypeEquity
	}
	if err := h.repo.SaveOrder(ctx, order); err != nil {
		h.log.Error().Err(err).Str("symbol", string(sym)).Str("order_id", orderID).Msg("failed to persist manual close order")
	}
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
