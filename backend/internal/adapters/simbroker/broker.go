// Package simbroker provides a simulated broker adapter for backtesting.
// It implements ports.BrokerPort with configurable slippage and instant fills
// using the latest bar close price.
package simbroker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/options"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// ErrParticipationCap is returned by SubmitOrder when an option fill's
// contract count would exceed OptionMaxParticipationPct of the
// contemporaneous bar volume. Distinct from price-related rejections so the
// runner can record it as a "size rejected" event in cost diagnostics.
var ErrParticipationCap = errors.New("simbroker: option participation cap exceeded")

// patientExitReasons enumerates intent.Meta["exit_reason"] values that
// should pay 1.0x impact (resting limit / patient cancel logic). Anything
// else — including missing — is treated as urgent and pays the urgency
// multiplier (default 1.5x).
var patientExitReasons = map[string]struct{}{
	"take_profit":    {},
	"profit_target":  {},
	"trailing":       {},
	"trailing_stop":  {},
	"limit":          {},
}

// Config holds SimBroker configuration.
type Config struct {
	SlippageBPS     int64   // slippage in basis points (default 5 per PRD)
	InitialEquity   float64 // starting cash/equity for the simulated account (default 100000)
	DisableFillChan bool    // skip fillCh sends; set when syncFill handles fills directly

	// IV adjustment parameters for same-day option exits
	VIXIVBeta           float64 // VIX-beta IV scaling exponent (0 = disabled; typical 0.7 for large caps)
	TODSeasonalEnabled  bool    // enable time-of-day IV seasonality multiplier (U-shape)
	EarningsRampEnabled bool    // enable earnings IV ramp model (sqrt decay)

	// Move-based IV crush for single-name spot-vol correlation (distinct
	// from index-level VIX-beta). Captures the empirical ATM-IV drop
	// when the underlying makes a directional move the option was
	// positioned for. All zero fields disable the path.
	MoveCrushEnabled bool
	MoveCrushCallK   float64 // typical 0.6
	MoveCrushPutK    float64 // typical 0.4
	MoveCrushFloor   float64 // min multiplier; typical 0.5

	// Option bid-ask spread realism knobs for fill simulation.
	// OptionExitSpreadMultiplier scales the tiered exit half-spread (0 treated as 1.0).
	// OptionEntrySpreadEnabled adds the same tiered half-spread to option entry fills.
	OptionExitSpreadMultiplier float64
	OptionEntrySpreadEnabled   bool

	// Tier 1 market-impact knobs. Both zero = OFF (helper short-circuits, no
	// port consulted). When either is non-zero the broker applies an impact
	// term on top of the tiered half-spread:
	//   impact_bps = OptionImpactScaleBps * sqrt(qty*100 / max(barVol, vol_floor))
	// Hard rejection if qty*100/barVol > OptionMaxParticipationPct
	// (returns ErrParticipationCap from SubmitOrder; barVol pre-floor on
	// the cap check so the cap engages even on very thin bars).
	OptionImpactScaleBps             float64
	OptionMaxParticipationPct        float64
	OptionImpactExitUrgencyMult      float64 // 0 => 1.5x; applied to non-patient exits
	OptionImpactVolFloor             int64   // 0 => 50

	// Sprint 7 — pluggable fill model + fee schedule. When FillModel is nil
	// the broker preserves the pre-Sprint-7 optimistic instant-close fill so
	// legacy callers keep deterministic numbers. When FeeSchedule is nil the
	// broker charges no fees (NoFees).
	FillModel     FillModel
	FeeSchedule   FeeSchedule
	LatencyMsEq   int // per-equity submission→next-bar latency budget; default 50ms
	LatencyMsOpt  int // per-option submission→next-bar latency budget; default 200ms
}

// simOrder tracks a submitted order and its fill details.
type simOrder struct {
	intent    domain.OrderIntent
	orderID   string
	fillPrice float64
	fillQty   float64
	filledAt  time.Time
	side      string
}

// position tracks aggregated position state for a symbol.
type position struct {
	symbol     domain.Symbol
	venue      domain.Venue      // execution venue; empty for equities (backward compat)
	side       string            // "buy" or "sell"
	quantity   float64
	avgCost    float64
	strategy   string            // attribution for per-strategy portfolio caps
	assetClass domain.AssetClass // populated from OrderIntent.AssetClass on fill
}

// positionKey generates the map key for position tracking.
// When venue is specified, positions are tracked per-venue so the same symbol
// can have independent positions on different exchanges (e.g., long BTC/USD on
// Coinbase + short BTC/USD on Hyperliquid). When venue is unspecified (empty),
// the key is symbol-only for backward compatibility with equity strategies.
func positionKey(symbol domain.Symbol, venue domain.Venue) string {
	if venue.IsUnspecified() {
		return string(symbol)
	}
	return string(venue) + ":" + string(symbol)
}

// Broker is a simulated broker for backtesting that implements ports.BrokerPort
// and ports.OrderStreamPort. It fills orders instantly at the last known bar
// close price with configurable slippage.
type Broker struct {
	slippageBPS         int64
	initialEquity       float64
	disableFillChan     bool
	vixIVBeta           float64
	todSeasonalEnabled  bool
	earningsRampEnabled bool
	moveCrushEnabled    bool
	moveCrushCallK      float64
	moveCrushPutK       float64
	moveCrushFloor      float64
	optionExitSpreadMult    float64
	optionEntrySpreadEnabled bool

	// Tier 1 market-impact state. nil port + both zero knobs = OFF.
	optionBarVolume         ports.OptionBarVolumePort
	optionImpactScaleBps    float64
	optionMaxParticipationPct float64
	optionImpactExitUrgencyMult float64
	optionImpactVolFloor    int64
	impactAppliedCount      atomic.Int64
	impactNoOpCount         atomic.Int64
	impactCapRejectCount    atomic.Int64

	historicalOptions   ports.HistoricalOptionsPort
	// optionLiveData supplies real bid/ask snapshots for option exit
	// pricing. Nil keeps the legacy tiered-spread BSM approximation in
	// charge — bootstrap only wires this when the operator opts in via
	// cfg.Options.UseLiveMarketData.
	optionLiveData      ports.OptionMarketDataPort
	log                 zerolog.Logger

	fillModel    FillModel
	feeSchedule  FeeSchedule
	latencyMsEq  int
	latencyMsOpt int

	mu        sync.RWMutex
	prices    map[domain.Symbol]float64
	barTimes  map[domain.Symbol]time.Time
	// bars holds the most recently reported OHLC for each symbol so fill
	// models that care about intrabar range can read it. Populated by
	// UpdateBar; UpdatePrice leaves it at zero OHLC (only close is known).
	bars      map[domain.Symbol]Bar
	orders    map[string]*simOrder
	positions map[string]*position
	cash      float64
	orderSeq  atomic.Int64

	// Funding accrual tracking for perpetual positions.
	totalFundingPaid     float64
	totalFundingReceived float64

	// Cumulative fill-realism costs for backtest reporting. Not mutex-locked
	// separately — all SubmitOrder increments happen under b.mu. Exposed via
	// CostTotals(); live paths leave these at zero since FeeSchedule=NoFees
	// and slippage attribution is tracked elsewhere.
	costCommission float64
	costExchange   float64
	costRegulatory float64
	costSlippage   float64

	fillCh chan ports.OrderUpdate
}

// CostTotals aggregates the cumulative fill-realism costs tallied by the
// broker across a backtest run.
type CostTotals struct {
	Commission float64 `json:"commission"`
	Exchange   float64 `json:"exchange"`
	Regulatory float64 `json:"regulatory"`
	Slippage   float64 `json:"slippage"`
	Total      float64 `json:"total"`
}

// ImpactStats returns the cumulative counters for the Tier 1 market-impact
// path: how many fills had impact applied, how many short-circuited (port
// no-data or knobs-off), and how many were rejected by the participation
// cap. Surfaces via the backtest data-quality summary log.
type ImpactStats struct {
	Applied    int64 `json:"applied"`
	NoOp       int64 `json:"no_op"`
	CapReject  int64 `json:"cap_reject"`
}

// ImpactStats returns a snapshot of the impact counters.
func (b *Broker) ImpactStats() ImpactStats {
	return ImpactStats{
		Applied:   b.impactAppliedCount.Load(),
		NoOp:      b.impactNoOpCount.Load(),
		CapReject: b.impactCapRejectCount.Load(),
	}
}

// CostTotals returns the cumulative fill-realism costs accumulated since
// broker construction. Safe to call from the finalizer goroutine after the
// backtest pipeline has drained.
func (b *Broker) CostTotals() CostTotals {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return CostTotals{
		Commission: b.costCommission,
		Exchange:   b.costExchange,
		Regulatory: b.costRegulatory,
		Slippage:   b.costSlippage,
		Total:      b.costCommission + b.costExchange + b.costRegulatory + b.costSlippage,
	}
}

// SetHistoricalOptions injects historical options data for realistic exit pricing.
func (b *Broker) SetHistoricalOptions(h ports.HistoricalOptionsPort) {
	b.historicalOptions = h
}

// SetOptionLiveData wires a per-contract Quote/Greeks feed used by the
// option-exit path. Nil disables the live lookup and keeps the existing
// BSM + tiered-spread approximation in charge.
func (b *Broker) SetOptionLiveData(p ports.OptionMarketDataPort) {
	b.optionLiveData = p
}

// SetOptionBarVolume wires a historical per-OCC bar-volume lookup used by
// the Tier 1 market-impact model. Nil disables the impact path even if the
// scale/max-participation knobs are non-zero — the helper short-circuits.
func (b *Broker) SetOptionBarVolume(p ports.OptionBarVolumePort) {
	b.optionBarVolume = p
}

// applyParticipationImpact scales basePrice by the size-vs-volume impact
// term and enforces the participation cap. Returns ErrParticipationCap on
// breach. Short-circuits to (basePrice, nil) when the impact knobs are off
// or the port is unwired — guarantees the OFF path is byte-identical to
// today's tiered-spread output. Worst-case-constant: a port error or
// zero-volume return is also a no-op (logged at debug, never warn).
//
// barTime is the underlying's current bar time (already computed by
// SubmitOrder); we don't re-derive it here because the broker does not
// track option-symbol bar times — option fills are scheduled against the
// underlying's bar.
//
// direction +1 = adverse impact added (long-side buy / short-side cover);
// direction -1 = adverse impact subtracted (long-side sell / short-side
// open) — symmetric with the existing tiered half-spread logic.
//
// urgent=true applies optionImpactExitUrgencyMult (default 1.5x) to the
// raw impact bps. Used for forced exits; patient exits (e.g. take-profit
// limits) call with urgent=false.
func (b *Broker) applyParticipationImpact(ctx context.Context, intent domain.OrderIntent, barTime time.Time, basePrice float64, direction float64, urgent bool) (float64, error) {
	if b.optionImpactScaleBps == 0 && b.optionMaxParticipationPct == 0 {
		return basePrice, nil
	}
	if b.optionBarVolume == nil {
		return basePrice, nil
	}
	if basePrice <= 0 {
		return basePrice, nil
	}

	qty := intent.Quantity
	if qty <= 0 {
		return basePrice, nil
	}

	// Pass the SubmitOrder ctx through. The adapter handles its own
	// per-call timeout (longer for first-touch fetch which pre-loads the
	// full backtest window for an OCC; shorter for cache hits which are
	// in-memory and effectively constant-time).
	barVol, err := b.optionBarVolume.BarVolume(ctx, intent.Symbol, barTime, domain.Timeframe("1m"))
	if err != nil || barVol <= 0 {
		b.impactNoOpCount.Add(1)
		return basePrice, nil
	}

	// Cap check uses RAW volume (pre-floor) so the cap engages on truly
	// thin bars. Impact-bps math uses the floored denominator to avoid
	// pathological infinite impact on near-empty bars.
	if b.optionMaxParticipationPct > 0 {
		rawParticipationPct := qty * 100.0 / float64(barVol) * 100.0
		if rawParticipationPct > b.optionMaxParticipationPct {
			b.impactCapRejectCount.Add(1)
			return basePrice, fmt.Errorf("%w: qty=%.0f bar_vol=%d participation=%.2f%% cap=%.2f%%",
				ErrParticipationCap, qty, barVol, rawParticipationPct, b.optionMaxParticipationPct)
		}
	}

	if b.optionImpactScaleBps == 0 {
		return basePrice, nil
	}

	denom := float64(barVol)
	if int64(denom) < b.optionImpactVolFloor {
		denom = float64(b.optionImpactVolFloor)
	}
	participation := qty * 100.0 / denom // contracts*100 shares / contract-bar
	impactBps := b.optionImpactScaleBps * math.Sqrt(participation)
	if urgent {
		impactBps *= b.optionImpactExitUrgencyMult
	}
	b.impactAppliedCount.Add(1)
	return basePrice + direction*(impactBps/10000.0)*basePrice, nil
}

// isPatientExit returns true if intent.Meta["exit_reason"] names a patient
// exit (resting limit / take-profit). Forced exits (stop-loss, EOD-flatten,
// programmatic) and missing exit_reason default to urgent.
func isPatientExit(intent domain.OrderIntent) bool {
	if intent.Meta == nil {
		return false
	}
	_, ok := patientExitReasons[intent.Meta["exit_reason"]]
	return ok
}

// New creates a new SimBroker with the given configuration.
func New(cfg Config, log zerolog.Logger) *Broker {
	if cfg.SlippageBPS == 0 {
		cfg.SlippageBPS = 5
	}
	equity := cfg.InitialEquity
	if equity == 0 {
		equity = 100_000
	}
	exitMult := cfg.OptionExitSpreadMultiplier
	if exitMult == 0 {
		exitMult = 1.0
	}
	exitUrgency := cfg.OptionImpactExitUrgencyMult
	if exitUrgency == 0 {
		exitUrgency = 1.5
	}
	volFloor := cfg.OptionImpactVolFloor
	if volFloor == 0 {
		volFloor = 50
	}
	fm := cfg.FillModel
	if fm == nil {
		fm = OptimisticFillModel{}
	}
	fs := cfg.FeeSchedule
	if fs == nil {
		fs = NoFees{}
	}
	latEq := cfg.LatencyMsEq
	if latEq == 0 {
		latEq = 50
	}
	latOpt := cfg.LatencyMsOpt
	if latOpt == 0 {
		latOpt = 200
	}
	return &Broker{
		slippageBPS:         cfg.SlippageBPS,
		initialEquity:       equity,
		disableFillChan:     cfg.DisableFillChan,
		vixIVBeta:           cfg.VIXIVBeta,
		todSeasonalEnabled:  cfg.TODSeasonalEnabled,
		earningsRampEnabled: cfg.EarningsRampEnabled,
		moveCrushEnabled:    cfg.MoveCrushEnabled,
		moveCrushCallK:      cfg.MoveCrushCallK,
		moveCrushPutK:       cfg.MoveCrushPutK,
		moveCrushFloor:      cfg.MoveCrushFloor,
		optionExitSpreadMult:     exitMult,
		optionEntrySpreadEnabled: cfg.OptionEntrySpreadEnabled,
		optionImpactScaleBps:        cfg.OptionImpactScaleBps,
		optionMaxParticipationPct:   cfg.OptionMaxParticipationPct,
		optionImpactExitUrgencyMult: exitUrgency,
		optionImpactVolFloor:        volFloor,
		log:                 log.With().Str("component", "simbroker").Logger(),
		fillModel:       fm,
		feeSchedule:     fs,
		latencyMsEq:     latEq,
		latencyMsOpt:    latOpt,
		prices:          make(map[domain.Symbol]float64),
		barTimes:        make(map[domain.Symbol]time.Time),
		bars:            make(map[domain.Symbol]Bar),
		orders:          make(map[string]*simOrder),
		positions:       make(map[string]*position),
		cash:            equity,
		fillCh:          make(chan ports.OrderUpdate, 256),
	}
}

// UpdateBar records the latest OHLC bar for a symbol. Callers that have the
// full bar (backtest replay) should prefer this over UpdatePrice so realistic
// fill models can see the intrabar range.
func (b *Broker) UpdateBar(symbol domain.Symbol, bar Bar) {
	b.mu.Lock()
	b.bars[symbol] = bar
	b.prices[symbol] = bar.Close
	b.barTimes[symbol] = bar.Time
	b.mu.Unlock()
}

// UpdatePrice sets the latest close price for a symbol. Called by the replay loop
// before publishing each bar event so SimBroker has the current price for fills.
func (b *Broker) UpdatePrice(symbol domain.Symbol, price float64, barTime time.Time) {
	b.mu.Lock()
	b.prices[symbol] = price
	b.barTimes[symbol] = barTime
	b.mu.Unlock()
}

// SubmitOrder fills the order immediately at the current bar close ± slippage.
// Returns a generated order ID. If no price is available for the symbol,
// the order is rejected with an error.
func (b *Broker) SubmitOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Sprint 5: combo BAG intents are not simulated atomically in simbroker.
	// Today no strategy emits combo intents; backtest/paper combo support is a
	// TODO(sprint5) follow-up that will synthesize two sequential leg fills.
	if intent.IsCombo() {
		return "", fmt.Errorf("simbroker: combo BAG orders not yet simulated (sprint5 TODO)")
	}

	isOption := intent.Instrument != nil && intent.Instrument.Type == domain.InstrumentTypeOption

	priceSymbol := intent.Symbol
	if isOption && intent.Instrument.UnderlyingSymbol != "" {
		priceSymbol = intent.Instrument.UnderlyingSymbol
	}

	lastPrice, ok := b.prices[priceSymbol]
	if (!ok || lastPrice <= 0) && !isOption {
		if intent.Direction.IsExit() {
			if pos, posOk := b.positions[positionKey(intent.Symbol, intent.Venue)]; posOk && pos.avgCost > 0 {
				lastPrice = pos.avgCost
				ok = true
			}
		}
		if !ok || lastPrice <= 0 {
			return "", fmt.Errorf("simbroker: no price available for %s — cannot fill order", priceSymbol)
		}
	}

	barTime := b.barTimes[priceSymbol]

	var fillPrice float64
	var side string
	var preSlippagePrice float64 // tracked to attribute slippage cost
	if isOption {
		slippage := float64(b.slippageBPS) / 10000.0
		switch {
		case intent.Direction.IsExit():
			// Compute BSM exit price using current underlying price
			fillPrice = b.computeOptionExitPrice(intent, lastPrice, barTime)
			if fillPrice <= 0 {
				fillPrice = 0.01
			}
			// Tier 1 market impact: forced exits cross the spread; resting
			// take-profit limits do not. Direction = -1 (we sell into the bid,
			// adverse impact subtracted from the base price).
			urgent := !isPatientExit(intent)
			impacted, impactErr := b.applyParticipationImpact(ctx, intent, barTime, fillPrice, -1.0, urgent)
			if impactErr != nil {
				return "", impactErr
			}
			fillPrice = impacted
			preSlippagePrice = fillPrice
			fillPrice *= (1 - slippage) // selling: slippage works against us
			side = "sell"
		default:
			if intent.LimitPrice <= 0 {
				return "", fmt.Errorf("simbroker: options entry has no limit price for %s", intent.Symbol)
			}
			// Sprint 6.2 — symmetric entry spread realism.
			// Gate metric: ORB option backtest win rate must drop 2-8% once
			// OptionEntrySpreadEnabled=true, proving the old mid-ish entry
			// fills were overstating backtest edge. See
			// docs/plans/EQUITY-OPTIONS-GAP-PLAN.md §Sprint 6.2.
			isShortEntry := intent.Direction == domain.DirectionShort
			fillPrice = b.computeOptionEntryPrice(intent, isShortEntry)
			// Tier 1 market impact: long entry pays adverse impact (direction=+1);
			// short entry receives less (direction=-1). Entries are not "urgent"
			// in the exit sense — no urgency multiplier.
			direction := 1.0
			if isShortEntry {
				direction = -1.0
			}
			impacted, impactErr := b.applyParticipationImpact(ctx, intent, barTime, fillPrice, direction, false)
			if impactErr != nil {
				return "", impactErr
			}
			fillPrice = impacted
			preSlippagePrice = fillPrice
			if isShortEntry {
				fillPrice *= (1 - slippage) // selling premium: slippage hurts the short
				side = "sell"
			} else {
				fillPrice *= (1 + slippage) // buying premium: slippage hurts the buyer
				side = "buy"
			}
		}
	} else {
		// Determine side and effective order type for this intent before
		// handing off to the pluggable fill model.
		var orderSide string
		var orderType string
		var limitPx float64
		switch intent.Direction {
		case domain.DirectionLong:
			orderSide = "BUY"
		case domain.DirectionShort:
			orderSide = "SELL"
		default:
			pos, hasPos := b.positions[positionKey(intent.Symbol, intent.Venue)]
			posSideForLog := ""
			if hasPos {
				posSideForLog = pos.side
			}
			b.log.Info(). // FIX_B_INSTRUMENT - remove after short-leak diagnosis
					Str("symbol", string(intent.Symbol)).
					Str("venue", string(intent.Venue)).
					Str("intent_direction", string(intent.Direction)).
					Bool("pos_found", hasPos).
					Str("pos_side", posSideForLog).
					Msg("simbroker exit-direction default branch")
			if hasPos && pos.side == "sell" {
				orderSide = "BUY"
			} else {
				orderSide = "SELL"
			}
			if intent.LimitPrice > 0 {
				orderType = "LMT"
				limitPx = intent.LimitPrice
			}
		}
		if orderType == "" {
			if intent.LimitPrice > 0 && intent.OrderType == "limit" {
				orderType = "LMT"
				limitPx = intent.LimitPrice
			} else {
				orderType = "MKT"
			}
		}

		curBar := b.bars[priceSymbol]
		if curBar.Close == 0 {
			curBar = Bar{Time: barTime, Close: lastPrice}
		}
		fctx := FillContext{
			Symbol:      string(intent.Symbol),
			Side:        orderSide,
			Qty:         intent.Quantity,
			OrderType:   orderType,
			LimitPrice:  limitPx,
			CurrentBar:  curBar,
			IsOption:    false,
			SlippageBPS: b.slippageBPS,
			SubmitTime:  barTime,
			LatencyMs:   b.latencyMsEq,
		}
		res, err := b.fillModel.FillPrice(fctx)
		if err != nil {
			return "", fmt.Errorf("simbroker: fill model %s: %w", b.fillModel.Name(), err)
		}
		if !res.Filled {
			return "", fmt.Errorf("simbroker: fill model %s did not fill order for %s: %s",
				b.fillModel.Name(), intent.Symbol, res.Reason)
		}
		fillPrice = res.Price
		// Fill model applies slippage internally; reconstruct the pre-slippage
		// mid by undoing the bps multiplier so downstream slippage attribution
		// is consistent with the option branch above.
		slip := float64(b.slippageBPS) / 10000.0
		if isBuy(orderSide) {
			side = "buy"
			if slip > 0 {
				preSlippagePrice = fillPrice / (1 + slip)
			} else {
				preSlippagePrice = fillPrice
			}
		} else {
			side = "sell"
			if slip > 0 {
				preSlippagePrice = fillPrice / (1 - slip)
			} else {
				preSlippagePrice = fillPrice
			}
		}
	}

	// Compute fees for this fill. Notional convention: options use the
	// standard 100-share multiplier so premium * qty * 100 reflects dollar
	// exposure; equities are straight price*qty. Caller-facing ports.Fees
	// rides on the OrderUpdate emitted below.
	notionalMult := 1.0
	if isOption {
		notionalMult = 100.0
	}
	feeCtx := FeeContext{
		Symbol:    string(intent.Symbol),
		Venue:     intent.Venue,
		IsOption:  isOption,
		Side:      side,
		Qty:       intent.Quantity,
		Notional:  fillPrice * intent.Quantity * notionalMult,
		FillPrice: fillPrice,
		OrderType: intent.OrderType,
	}
	fees := b.feeSchedule.Compute(feeCtx)

	// Slippage is attributed from the pre-slippage price so the dollar value
	// reflects the intended bps friction instead of compounding with the
	// later fee embed.
	b.costCommission += fees.Commission
	b.costExchange += fees.Exchange
	b.costRegulatory += fees.Regulatory
	if preSlippagePrice > 0 && b.slippageBPS > 0 {
		slipMag := float64(b.slippageBPS) / 10000.0
		b.costSlippage += slipMag * preSlippagePrice * intent.Quantity * notionalMult
	}

	// Backtest path only: bake fee.Total into fillPrice so downstream PnL
	// accounting reflects commissions without threading a separate fee field
	// through execution, FillReceived, and the collector. Live adapters
	// return fees out-of-band from the broker and this branch is a no-op
	// because FeeSchedule=NoFees in production execution paths.
	if fees.Total > 0 && intent.Quantity > 0 {
		perUnitAdj := fees.Total / (intent.Quantity * notionalMult)
		if side == "buy" {
			fillPrice += perUnitAdj
		} else {
			fillPrice -= perUnitAdj
			if fillPrice < 0.01 {
				fillPrice = 0.01
			}
		}
	}

	// Generate order ID.
	seq := b.orderSeq.Add(1)
	orderID := fmt.Sprintf("sim-%d", seq)

	// Record the order.
	b.orders[orderID] = &simOrder{
		intent:    intent,
		orderID:   orderID,
		fillPrice: fillPrice,
		fillQty:   intent.Quantity,
		filledAt:  barTime,
		side:      side,
	}

	// Update position tracking.
	posKey := positionKey(intent.Symbol, intent.Venue)
	pos, exists := b.positions[posKey]
	if !exists {
		pos = &position{symbol: intent.Symbol, venue: intent.Venue, assetClass: intent.AssetClass}
		b.positions[posKey] = pos
	}

	switch side {
	case "buy":
		b.cash -= fillPrice * intent.Quantity
		switch {
		case pos.quantity == 0:
			pos.side = "buy"
			pos.avgCost = fillPrice
			pos.quantity = intent.Quantity
			pos.strategy = intent.Strategy
		case pos.side == "buy":
			totalCost := pos.avgCost*pos.quantity + fillPrice*intent.Quantity
			pos.quantity += intent.Quantity
			pos.avgCost = totalCost / pos.quantity
		default:
			pos.quantity -= intent.Quantity
			if pos.quantity <= 0 {
				pos.quantity = -pos.quantity
				pos.side = "buy"
				pos.avgCost = fillPrice
				pos.strategy = intent.Strategy
			} else if pos.quantity == 0 {
				pos.strategy = ""
			}
		}
	case "sell":
		b.cash += fillPrice * intent.Quantity
		switch {
		case pos.quantity == 0:
			pos.side = "sell"
			pos.avgCost = fillPrice
			pos.quantity = intent.Quantity
			pos.strategy = intent.Strategy
		case pos.side == "sell":
			totalCost := pos.avgCost*pos.quantity + fillPrice*intent.Quantity
			pos.quantity += intent.Quantity
			pos.avgCost = totalCost / pos.quantity
		default:
			pos.quantity -= intent.Quantity
			if pos.quantity <= 0 {
				pos.quantity = -pos.quantity
				pos.side = "sell"
				pos.avgCost = fillPrice
				pos.strategy = intent.Strategy
			} else if pos.quantity == 0 {
				pos.strategy = ""
			}
		}
	}

	b.log.Debug().
		Str("order_id", orderID).
		Str("symbol", string(intent.Symbol)).
		Str("side", side).
		Float64("fill_price", fillPrice).
		Float64("last_price", lastPrice).
		Float64("quantity", intent.Quantity).
		Int64("slippage_bps", b.slippageBPS).
		Msg("order filled")

	// Non-blocking send to fill channel for OrderStreamPort consumers.
	// Skipped when DisableFillChan is set (syncFill mode handles fills inline).
	if !b.disableFillChan {
		select {
		case b.fillCh <- ports.OrderUpdate{
			BrokerOrderID:  orderID,
			Event:          "fill",
			Qty:            intent.Quantity,
			Price:          fillPrice,
			FilledQty:      intent.Quantity,
			FilledAvgPrice: fillPrice,
			FilledAt:       barTime,
			Commission:     fees.Commission,
			RegulatoryFee:  fees.Regulatory,
			ExchangeFee:    fees.Exchange,
			FeesTotal:      fees.Total,
		}:
		default:
		}
	}

	return orderID, nil
}

// CancelOrder is a no-op for SimBroker since all orders fill instantly.
func (b *Broker) CancelOrder(_ context.Context, orderID string) error {
	b.mu.RLock()
	_, exists := b.orders[orderID]
	b.mu.RUnlock()
	if !exists {
		return fmt.Errorf("simbroker: order %s not found", orderID)
	}
	return nil
}

// ModifyOrder satisfies ports.OrderModifier so backtests can exercise the
// caller's modify-first code path; simbroker fills instantly so there is
// nothing to modify, and ErrUnsupportedModify routes the caller through the
// existing cancel+place fallback.
func (b *Broker) ModifyOrder(_ context.Context, _ string, _, _ float64) error {
	return ports.ErrUnsupportedModify
}

// GetOrderStatus always returns "filled" for known orders since SimBroker fills instantly.
func (b *Broker) GetOrderStatus(_ context.Context, orderID string) (string, error) {
	b.mu.RLock()
	_, exists := b.orders[orderID]
	b.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("simbroker: order %s not found", orderID)
	}
	return "filled", nil
}

// GetPositions returns the current simulated positions as domain.Trade slices.
func (b *Broker) GetPositions(_ context.Context, _ string, _ domain.EnvMode) ([]domain.Trade, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	trades := make([]domain.Trade, 0, len(b.positions))
	for _, pos := range b.positions {
		if pos.quantity <= 0 {
			continue
		}
		trades = append(trades, domain.Trade{
			Symbol:   pos.symbol,
			Venue:    pos.venue,
			Side:     pos.side,
			Quantity: pos.quantity,
			Price:    pos.avgCost,
			Status:   "open",
			Strategy: pos.strategy,
		})
	}
	return trades, nil
}

func (b *Broker) CancelOpenOrders(_ context.Context, _ domain.Symbol, _ string) (int, error) {
	return 0, nil
}

func (b *Broker) CancelAllOpenOrders(_ context.Context) (int, error) {
	return 0, nil
}

// GetOpenOrders always returns an empty slice. SimBroker is an in-process
// deterministic simulator; there are no cross-session working orders to
// reconcile at startup.
func (b *Broker) GetOpenOrders(_ context.Context) ([]ports.OpenOrder, error) {
	return []ports.OpenOrder{}, nil
}

func (b *Broker) GetPosition(_ context.Context, symbol domain.Symbol) (float64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	// Backward compatible: symbol-only lookup (empty venue).
	pos, ok := b.positions[positionKey(symbol, domain.VenueUnspecified)]
	if !ok || pos.quantity <= 0 {
		return 0, nil
	}
	return pos.quantity, nil
}

// GetPositionsByVenue returns all open positions for a specific venue.
func (b *Broker) GetPositionsByVenue(_ context.Context, venue domain.Venue) ([]domain.Trade, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var trades []domain.Trade
	for _, pos := range b.positions {
		if pos.quantity <= 0 {
			continue
		}
		if pos.venue != venue {
			continue
		}
		trades = append(trades, domain.Trade{
			Symbol:   pos.symbol,
			Venue:    pos.venue,
			Side:     pos.side,
			Quantity: pos.quantity,
			Price:    pos.avgCost,
			Status:   "open",
			Strategy: pos.strategy,
		})
	}
	return trades, nil
}

func (b *Broker) CloseAtMarket(_ context.Context, _ domain.Symbol) (string, error) {
	return "", nil
}

func (b *Broker) GetOrderDetails(_ context.Context, orderID string) (ports.OrderDetails, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ord, ok := b.orders[orderID]
	if !ok {
		return ports.OrderDetails{}, fmt.Errorf("simbroker: order %s: %w", orderID, ports.ErrOrderNotFound)
	}
	return ports.OrderDetails{
		Status:         "filled",
		FilledQty:      ord.fillQty,
		FilledAvgPrice: ord.fillPrice,
		FilledAt:       ord.filledAt,
	}, nil
}

// GetFillPrice returns the fill price for a given order ID. Used by the backtest
// collector to access actual fill details without relying on status string parsing.
func (b *Broker) GetFillPrice(orderID string) (float64, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ord, ok := b.orders[orderID]
	if !ok {
		return 0, false
	}
	return ord.fillPrice, true
}

// Stats returns summary statistics about the SimBroker's activity.
func (b *Broker) Stats() (totalOrders int, symbolsTraded int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.orders), len(b.positions)
}

// GetPrice returns the last known price for a symbol. Used by the passthrough
// QuoteProvider in backtest mode so the SlippageGuard can check bid/ask.
func (b *Broker) GetPrice(symbol domain.Symbol) (float64, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	p, ok := b.prices[symbol]
	return p, ok
}

func (b *Broker) GetQuote(_ context.Context, symbol domain.Symbol) (bid float64, ask float64, err error) {
	b.mu.RLock()
	lastPrice, ok := b.prices[symbol]
	b.mu.RUnlock()
	if !ok {
		return 0, 0, fmt.Errorf("simbroker: no price available for %s", symbol)
	}
	spreadHalf := lastPrice * float64(b.slippageBPS) / 10000.0 / 2.0
	return lastPrice - spreadHalf, lastPrice + spreadHalf, nil
}

func (b *Broker) GetAccountBuyingPower(_ context.Context) (ports.BuyingPower, error) {
	b.mu.RLock()
	cash := b.cash
	b.mu.RUnlock()
	return ports.BuyingPower{
		DayTradingBuyingPower:    cash,
		EffectiveBuyingPower:     cash,
		NonMarginableBuyingPower: cash,
	}, nil
}

func (b *Broker) GetAccountEquity(_ context.Context) (float64, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	equity := b.cash
	for _, pos := range b.positions {
		if pos.quantity <= 0 {
			continue
		}
		currentPrice, ok := b.prices[pos.symbol]
		if !ok {
			currentPrice = pos.avgCost
		}
		switch pos.side {
		case "buy":
			equity += currentPrice * pos.quantity
		case "sell":
			equity += (2*pos.avgCost - currentPrice) * pos.quantity
		}
	}
	return equity, nil
}

// SubscribeOrderUpdates returns a per-call output channel that forwards
// fills from the shared producer-side b.fillCh until ctx is canceled, at
// which point the output channel is closed. This honors the
// OrderStreamPort lifecycle contract (close-on-ctx-cancel) without
// disturbing producers, which keep using the non-blocking send to
// b.fillCh.
//
// Single-subscriber by design: a second concurrent caller would race the
// first wrapper for fills off b.fillCh (each fill goes to exactly one
// subscriber, not fanned out). The sole production consumer is
// app/execution.Service, which subscribes once on startup, so this is
// fine; do not add a second subscriber without first replacing this
// wrapper with a fan-out registry.
//
// Producer-side back-pressure is unchanged: SubmitOrder paths use a
// non-blocking `select { case b.fillCh <- u: default: }` and will drop
// fills silently when no subscriber drains b.fillCh fast enough. IBKR's
// producer (order_stream.go) has a guarded ctx-aware send rather than a
// silent drop; that LSP-shaped gap is out of scope here.
//
// Output buffer size mirrors ibkr/order_stream.go.
func (b *Broker) SubscribeOrderUpdates(ctx context.Context) (<-chan ports.OrderUpdate, error) {
	out := make(chan ports.OrderUpdate, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case u, ok := <-b.fillCh:
				if !ok {
					return
				}
				select {
				case out <- u:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// AccrueFunding applies a funding payment to all open perp positions for symbol.
// Called by the backtest replay loop at each funding interval (typically every 8h).
// rate is the funding rate for the period (e.g. 0.0001 = 1 bps).
// Positive rate: longs pay shorts. Negative rate: shorts pay longs.
func (b *Broker) AccrueFunding(symbol domain.Symbol, rate float64, ts time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Find all positions for this symbol across all venues. Funding applies
	// to every venue holding a perp position for the symbol.
	var posKeys []string
	for k, p := range b.positions {
		if p.symbol == symbol && p.quantity != 0 {
			posKeys = append(posKeys, k)
		}
	}
	if len(posKeys) == 0 {
		return
	}

	for _, pk := range posKeys {
		b.accrueFundingForPosition(pk, rate, ts)
	}
}

// accrueFundingForPosition applies a funding payment to a single position.
// Caller must hold b.mu.
func (b *Broker) accrueFundingForPosition(posKey string, rate float64, ts time.Time) {
	pos, ok := b.positions[posKey]
	if !ok || pos.quantity == 0 {
		return
	}
	if pos.assetClass != domain.AssetClassCryptoPerp {
		return
	}

	markPrice, ok := b.prices[pos.symbol]
	if !ok || markPrice <= 0 {
		markPrice = pos.avgCost // fallback to entry cost
	}

	payment := pos.quantity * markPrice * rate

	if pos.side == "buy" {
		// Long pays when rate > 0, receives when rate < 0
		b.cash -= payment
		if payment > 0 {
			b.totalFundingPaid += payment
		} else {
			b.totalFundingReceived += -payment
		}
	} else {
		// Short receives when rate > 0, pays when rate < 0
		b.cash += payment
		if payment > 0 {
			b.totalFundingReceived += payment
		} else {
			b.totalFundingPaid += -payment
		}
	}

	b.log.Debug().
		Str("symbol", string(pos.symbol)).
		Float64("rate", rate).
		Float64("mark_price", markPrice).
		Float64("payment", payment).
		Str("side", pos.side).
		Time("ts", ts).
		Msg("funding accrued")

	// Emit funding event so the P&L tracker records it.
	if !b.disableFillChan {
		select {
		case b.fillCh <- ports.OrderUpdate{
			Event:    "funding",
			Price:    payment,
			FilledAt: ts,
		}:
		default:
		}
	}
}

// FundingPnL returns the net funding P&L (received - paid).
func (b *Broker) FundingPnL() float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.totalFundingReceived - b.totalFundingPaid
}

// FundingStats returns the total funding paid and received.
func (b *Broker) FundingStats() (paid, received float64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.totalFundingPaid, b.totalFundingReceived
}

// computeOptionExitPrice computes the BSM price for an options exit using the
// current underlying price and the entry metadata (strike, DTE, IV, right).
// Applies dynamic IV adjustments (VIX-beta, time-of-day, earnings ramp) and
// bid-ask spread for realistic pricing.
func (b *Broker) computeOptionExitPrice(intent domain.OrderIntent, underlyingPrice float64, barTime time.Time) float64 {
	if intent.Meta == nil {
		return 0
	}

	if refStr := intent.Meta["copytrade_exit_ref_premium"]; refStr != "" {
		var ref float64
		if _, err := fmt.Sscanf(refStr, "%f", &ref); err == nil && ref > 0 {
			return ref
		}
	}

	strikeStr := intent.Meta["strike"]
	expiryStr := intent.Meta["expiry"]
	rightStr := intent.Meta["option_right"]

	if strikeStr == "" || expiryStr == "" {
		if pos, ok := b.positions[positionKey(intent.Symbol, intent.Venue)]; ok && pos.avgCost > 0 {
			return pos.avgCost
		}
		return 0
	}

	var strike float64
	_, _ = fmt.Sscanf(strikeStr, "%f", &strike)

	expiry, err := time.Parse("2006-01-02", expiryStr)
	if err != nil {
		return 0
	}

	underlying := intent.Meta["underlying"]
	if underlying == "" {
		underlying = string(domain.UnderlyingFromOCC(intent.Symbol))
	}

	// Live per-contract bid (Theta Data) takes priority when wired.
	// On any error we silently fall through to the historical/BSM
	// branches below — keeps legacy backtests reproducible bit-for-bit.
	if b.optionLiveData != nil {
		right := "C"
		if rightStr == "PUT" {
			right = "P"
		}
		q, qErr := b.optionLiveData.Quote(context.Background(), underlying, expiry, strike, right)
		if qErr == nil && q.Bid > 0 {
			return q.Bid
		}
	}

	// Multi-day holds: use historical bid from DoltHub (different daily snapshot).
	entryDateStr := intent.Meta["entry_date"]
	exitDate := barTime.Format("2006-01-02")
	isMultiDay := entryDateStr != "" && entryDateStr != exitDate

	if isMultiDay && b.historicalOptions != nil {
		right := domain.OptionRightCall
		if rightStr == "PUT" {
			right = domain.OptionRightPut
		}
		row, err := b.historicalOptions.GetHistoricalContract(
			context.Background(), domain.Symbol(underlying), barTime,
			strike, expiry, right)
		if err == nil && row != nil && row.Bid > 0 {
			return row.Bid
		}
	}

	// Same-day exits: BSM recalculation using entry IV for accurate pricing.
	// This replaces the previous delta-linear approximation which had 5-25% error
	// for ATM options on 1-3% underlying moves.
	var iv float64
	_, _ = fmt.Sscanf(intent.Meta["iv_at_entry"], "%f", &iv)
	isCall := rightStr != "PUT"

	const riskFreeRate = 0.045

	// Compute remaining DTE in years
	dteYears := expiry.Sub(barTime).Hours() / (365.25 * 24)
	if dteYears <= 0 {
		// Expired — intrinsic value only. OTM-at-expiry returns 0
		// (worthless), not a $0.01 floor that silently adds
		// contracts * $0.01 * 100 per universe to the backtest P&L.
		var intrinsic float64
		if isCall {
			intrinsic = underlyingPrice - strike
		} else {
			intrinsic = strike - underlyingPrice
		}
		if intrinsic < 0 {
			intrinsic = 0
		}
		return intrinsic
	}

	if iv > 0 && strike > 0 {
		// Apply dynamic IV adjustments for same-day exits
		adj := options.IVAdjustment{
			VIXBeta:             b.vixIVBeta,
			TODSeasonalEnabled:  b.todSeasonalEnabled,
			EarningsRampEnabled: b.earningsRampEnabled,
			MoveCrushEnabled:    b.moveCrushEnabled,
			MoveCrushCallK:      b.moveCrushCallK,
			MoveCrushPutK:       b.moveCrushPutK,
			MoveCrushFloor:      b.moveCrushFloor,
			IsCall:              isCall,
		}
		// VIX-beta: read current VIX and entry VIX from meta
		if b.vixIVBeta > 0 {
			adj.VIXNow = b.prices[domain.SymbolVIX]
			var vixEntry float64
			_, _ = fmt.Sscanf(intent.Meta["vix_at_entry"], "%f", &vixEntry)
			adj.VIXAtEntry = vixEntry
		}
		// Time-of-day seasonality: compute minutes since 9:30 ET
		if b.todSeasonalEnabled {
			adj.MinutesSinceOpen = minutesSinceMarketOpen(barTime)
		}
		// Earnings IV ramp
		if b.earningsRampEnabled {
			var dte int
			_, _ = fmt.Sscanf(intent.Meta["days_to_earnings"], "%d", &dte)
			adj.DaysToEarnings = dte
		}
		// Move-based IV crush: signed return since entry, expressed as decimal.
		if b.moveCrushEnabled {
			var entryUnderlying float64
			_, _ = fmt.Sscanf(intent.Meta["entry_underlying"], "%f", &entryUnderlying)
			if entryUnderlying > 0 && underlyingPrice > 0 {
				adj.UnderlyingRetPct = (underlyingPrice - entryUnderlying) / entryUnderlying
			}
		}
		iv = options.AdjustIV(iv, adj)

		exitPremium := options.BSMPriceAtTime(underlyingPrice, strike, dteYears, riskFreeRate, iv, isCall)

		// Apply half-spread cost (selling at bid) — tiered by premium level
		var entryPremium float64
		_, _ = fmt.Sscanf(intent.Meta["premium"], "%f", &entryPremium)
		if entryPremium <= 0 {
			if pos, ok := b.positions[positionKey(intent.Symbol, intent.Venue)]; ok && pos.avgCost > 0 {
				entryPremium = pos.avgCost
			}
		}
		if entryPremium <= 0 {
			entryPremium = exitPremium // use BSM price for spread tier
		}
		spreadPct := optionSpreadPct(entryPremium) * b.optionExitSpreadMult
		exitPremium -= entryPremium * spreadPct

		if exitPremium < 0.01 {
			exitPremium = 0.01
		}
		return exitPremium
	}

	// Fallback: delta-linear if IV not available
	var entryPremium, delta float64
	_, _ = fmt.Sscanf(intent.Meta["premium"], "%f", &entryPremium)
	_, _ = fmt.Sscanf(intent.Meta["delta_at_entry"], "%f", &delta)

	if entryPremium <= 0 {
		if pos, ok := b.positions[positionKey(intent.Symbol, intent.Venue)]; ok && pos.avgCost > 0 {
			entryPremium = pos.avgCost
		}
	}
	if entryPremium <= 0 || delta == 0 {
		return entryPremium
	}

	var entryUnderlying float64
	_, _ = fmt.Sscanf(intent.Meta["entry_underlying"], "%f", &entryUnderlying)
	if entryUnderlying <= 0 {
		entryUnderlying = strike
	}

	underlyingMove := underlyingPrice - entryUnderlying
	exitPremium := entryPremium + delta*underlyingMove

	spreadPct := optionSpreadPct(entryPremium) * b.optionExitSpreadMult
	exitPremium -= entryPremium * spreadPct

	if exitPremium < 0.01 {
		exitPremium = 0.01
	}

	return exitPremium
}

// computeOptionEntryPrice returns the fill price for an option entry leg,
// mirroring computeOptionExitPrice so entries and exits pay symmetric
// bid-ask costs. BUY entries are taker trades that pay the ask
// (mid + half_spread); SELL (short) entries receive the bid
// (mid - half_spread). When the live option data port is wired and
// returns a usable Quote, the real ask/bid is used directly; otherwise
// the tiered-spread approximation is applied around intent.LimitPrice
// (treated as the mid).
//
// Gated by Broker.optionEntrySpreadEnabled so legacy backtests (flag
// off) keep the previous mid-fill behavior and remain byte-identical.
func (b *Broker) computeOptionEntryPrice(intent domain.OrderIntent, isShortEntry bool) float64 {
	mid := intent.LimitPrice
	if !b.optionEntrySpreadEnabled {
		return mid
	}

	// Live per-contract quote takes priority when available.
	if b.optionLiveData != nil && intent.Meta != nil {
		strikeStr := intent.Meta["strike"]
		expiryStr := intent.Meta["expiry"]
		rightStr := intent.Meta["option_right"]
		underlying := intent.Meta["underlying"]
		if underlying == "" {
			underlying = string(domain.UnderlyingFromOCC(intent.Symbol))
		}
		if strikeStr != "" && expiryStr != "" {
			var strike float64
			_, _ = fmt.Sscanf(strikeStr, "%f", &strike)
			if expiry, err := time.Parse("2006-01-02", expiryStr); err == nil {
				right := "C"
				if rightStr == "PUT" {
					right = "P"
				}
				q, qErr := b.optionLiveData.Quote(context.Background(), underlying, expiry, strike, right)
				if qErr == nil {
					if isShortEntry && q.Bid > 0 {
						return q.Bid
					}
					if !isShortEntry && q.Ask > 0 {
						return q.Ask
					}
				}
			}
		}
	}

	// Fallback: tiered half-spread around the mid (intent limit).
	// optionExitSpreadMult is intentionally reused here — the knob
	// scales the tiered-spread model on BOTH entry and exit fills so
	// a single config value controls symmetric spread realism. The
	// field is still named "exit" for backward compatibility; fold
	// into an OptionSpreadMultiplier rename when the HTTP surface
	// next changes.
	spreadPct := optionSpreadPct(mid) * b.optionExitSpreadMult
	half := mid * spreadPct
	if isShortEntry {
		fill := mid - half
		if fill < 0.01 {
			fill = 0.01
		}
		return fill
	}
	return mid + half
}

// optionSpreadPct returns the tiered half-spread percentage applied to an
// option fill given the premium level. Shared by entry and exit paths.
func optionSpreadPct(premium float64) float64 {
	switch {
	case premium >= 10.0:
		return 0.003
	case premium >= 5.0:
		return 0.005
	case premium >= 2.0:
		return 0.008
	default:
		return 0.015
	}
}

// SubmitGroup submits all intents as an atomic unit for backtesting paired
// strategies. If any leg fails to fill, all legs are rejected. Order IDs are
// returned in the same order as intents.
func (b *Broker) SubmitGroup(ctx context.Context, intents []domain.OrderIntent) ([]string, error) {
	if len(intents) == 0 {
		return nil, fmt.Errorf("simbroker: empty group submission")
	}

	// Pre-check: all symbols must have a price available before we commit
	// to filling any leg. This avoids partial-fill states.
	b.mu.RLock()
	for _, intent := range intents {
		sym := intent.Symbol
		if intent.Instrument != nil && intent.Instrument.UnderlyingSymbol != "" {
			sym = intent.Instrument.UnderlyingSymbol
		}
		if _, ok := b.prices[sym]; !ok {
			b.mu.RUnlock()
			return nil, fmt.Errorf("simbroker: group rejected — no price for %s", sym)
		}
	}
	b.mu.RUnlock()

	// Submit each leg. If any fails, reject the whole group.
	orderIDs := make([]string, 0, len(intents))
	for i, intent := range intents {
		oid, err := b.SubmitOrder(ctx, intent)
		if err != nil {
			return nil, fmt.Errorf("simbroker: group leg[%d] (%s) failed: %w", i, intent.Symbol, err)
		}
		orderIDs = append(orderIDs, oid)
	}
	return orderIDs, nil
}

// minutesSinceMarketOpen returns minutes elapsed since 9:30 ET on the bar's date.
// Returns 0 for pre-market, 390 for post-close.
func minutesSinceMarketOpen(barTime time.Time) int {
	et, err := time.LoadLocation("America/New_York")
	if err != nil {
		return 195 // midday default
	}
	t := barTime.In(et)
	marketOpen := time.Date(t.Year(), t.Month(), t.Day(), 9, 30, 0, 0, et)
	diff := int(t.Sub(marketOpen).Minutes())
	if diff < 0 {
		return 0
	}
	if diff > 390 {
		return 390
	}
	return diff
}
