package builtin

import (
	"encoding/json"
	"fmt"
	"time"

	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// AIScalperStrategy implements RSI/Stochastic mean-reversion scalping
// with optional AI debate overlay for sizing/veto.
type AIScalperStrategy struct {
	meta start.Meta
}

// NewAIScalperStrategy creates a new AI-Enhanced Scalping strategy.
func NewAIScalperStrategy() *AIScalperStrategy {
	id, _ := start.NewStrategyID("ai_scalping_v1")
	ver, _ := start.NewVersion("1.0.0")
	return &AIScalperStrategy{
		meta: start.Meta{
			ID:          id,
			Version:     ver,
			Name:        "AI-Enhanced Scalping",
			Description: "RSI/Stochastic mean-reversion scalping aligned with 5m regime, AI debate overlay",
			Author:      "system",
		},
	}
}

func (s *AIScalperStrategy) Meta() start.Meta { return s.meta }
func (s *AIScalperStrategy) WarmupBars() int  { return 30 }

// AIScalperConfig holds strategy parameters parsed from DNA.
type AIScalperConfig struct {
	RSILong          float64
	RSIShort         float64
	StochLong        float64
	StochShort       float64
	RSIExitMid       float64
	AllowRegimes     []string
	CooldownSeconds  int
	MaxTradesPerDay  int
	AIEnabled        bool
	AITimeoutSeconds int
	AIMinConfidence  float64
	SizeMultMin      float64
	SizeMultBase     float64
	SizeMultMax      float64
}

// AIScalperState is the per-symbol state for the AI scalping strategy.
type AIScalperState struct {
	Symbol             string
	Indicators         start.IndicatorData
	PrevStochK         float64
	PrevStochD         float64
	TradesToday        int
	CooldownUntil      time.Time
	PositionSide       start.Side
	PendingEntry       start.Side // set on signal emission, cleared on fill/rejection
	PendingEntryAt     time.Time  // when PendingEntry was set (for timeout recovery)
	PendingAIRequestID string
	LastAIVerdict      string
	LastAIConfidence   float64
	LastAIAt           time.Time
	LastSizeMult       float64
	LastBarClose       float64
	Config             AIScalperConfig
}

// SetIndicators implements the indicatorSetter interface.
func (s *AIScalperState) SetIndicators(ind start.IndicatorData) {
	s.Indicators = ind
}

func (s *AIScalperState) ClearPendingEntry() {
	s.PendingEntry = ""
	s.PendingEntryAt = time.Time{}
}

// AIDebateResult is the event payload returned by the AI debate system.
type AIDebateResult struct {
	RequestID  string
	Verdict    string // "bull", "bear", "neutral", "veto"
	Confidence float64
}

func parseAIScalperConfig(params map[string]any) AIScalperConfig {
	return AIScalperConfig{
		RSILong:          getFloat64(params, "rsi_long", 30),
		RSIShort:         getFloat64(params, "rsi_short", 70),
		StochLong:        getFloat64(params, "stoch_long", 20),
		StochShort:       getFloat64(params, "stoch_short", 80),
		RSIExitMid:       getFloat64(params, "rsi_exit_mid", 50),
		AllowRegimes:     getStringSlice(params, "allow_regimes", []string{"BALANCE", "REVERSAL"}),
		CooldownSeconds:  getInt(params, "cooldown_seconds", 60),
		MaxTradesPerDay:  getInt(params, "max_trades_per_day", 10),
		AIEnabled:        getBool(params, "ai_enabled", false),
		AITimeoutSeconds: getInt(params, "ai_timeout_seconds", 5),
		AIMinConfidence:  getFloat64(params, "ai_min_confidence", 0.65),
		SizeMultMin:      getFloat64(params, "size_mult_min", 0.5),
		SizeMultBase:     getFloat64(params, "size_mult_base", 1.0),
		SizeMultMax:      getFloat64(params, "size_mult_max", 1.5),
	}
}

// Init creates initial state for a symbol.
func (s *AIScalperStrategy) Init(ctx start.Context, symbol string, params map[string]any, prior start.State) (start.State, error) {
	cfg := parseAIScalperConfig(params)

	st := &AIScalperState{
		Symbol: symbol,
		Config: cfg,
	}

	if prior != nil {
		if aiPrior, ok := prior.(*AIScalperState); ok {
			st.PrevStochK = aiPrior.PrevStochK
			st.PrevStochD = aiPrior.PrevStochD
			st.TradesToday = aiPrior.TradesToday
			st.CooldownUntil = aiPrior.CooldownUntil
			st.PositionSide = aiPrior.PositionSide
			st.PendingEntry = aiPrior.PendingEntry
			st.PendingEntryAt = aiPrior.PendingEntryAt
			st.PendingAIRequestID = aiPrior.PendingAIRequestID
			st.LastAIVerdict = aiPrior.LastAIVerdict
			st.LastAIConfidence = aiPrior.LastAIConfidence
			st.LastAIAt = aiPrior.LastAIAt
			st.LastSizeMult = aiPrior.LastSizeMult
			st.LastBarClose = aiPrior.LastBarClose
			st.Config = cfg
		} else if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("AIScalperStrategy: incompatible prior state, starting fresh", "symbol", symbol)
		}
	}

	return st, nil
}

// OnBar processes a bar and emits scalping entry/exit signals.
func (s *AIScalperStrategy) OnBar(ctx start.Context, symbol string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
	aiSt, ok := st.(*AIScalperState)
	if !ok {
		return st, nil, fmt.Errorf("AIScalperStrategy.OnBar: expected *AIScalperState, got %T", st)
	}
	cfg := aiSt.Config

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
	}

	// Pending-entry timeout: if we've been waiting for a fill/rejection for more
	// than 5 minutes, assume the event was lost and clear the pending state.
	const pendingEntryTimeout = 5 * time.Minute
	if aiSt.PendingEntry != "" && now.Sub(aiSt.PendingEntryAt) > pendingEntryTimeout {
		if ctx != nil && ctx.Logger() != nil {
			ctx.Logger().Warn("PendingEntry timeout — clearing",
				"side", aiSt.PendingEntry,
				"pending_since", aiSt.PendingEntryAt,
			)
		}
		aiSt.PendingEntry = ""
		aiSt.PendingEntryAt = time.Time{}
	}

	// Read current indicators.
	rsi := aiSt.Indicators.RSI
	stochK := aiSt.Indicators.StochK
	stochD := aiSt.Indicators.StochD

	// Stoch cross detection uses prev values from prior bar.
	crossUp := aiSt.PrevStochK <= aiSt.PrevStochD && stochK > stochD
	crossDown := aiSt.PrevStochK >= aiSt.PrevStochD && stochK < stochD

	// Save current as prev for next bar (do this after reading prev).
	defer func() {
		aiSt.PrevStochK = stochK
		aiSt.PrevStochD = stochD
	}()

	// Regime detection.
	regimeTag := "none"
	regimeIsTrend := false
	regimeAllowed := true
	if ar, ok2 := aiSt.Indicators.AnchorRegimes["5m"]; ok2 {
		regimeTag = ar.Type
		if ar.Type == "TREND" {
			regimeIsTrend = true
		}
		regimeAllowed = false
		for _, allowed := range cfg.AllowRegimes {
			if ar.Type == allowed {
				regimeAllowed = true
				break
			}
		}
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second

	// 1. Exit signals (always checked, even during cooldown).
	if aiSt.PositionSide == start.SideBuy && aiSt.PendingEntry == "" {
		exitLong := rsi >= cfg.RSIExitMid || stochK >= cfg.StochShort || regimeIsTrend
		if exitLong {
			sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideSell, 0.8, map[string]string{
				"ref_price": fmt.Sprintf("%.10f", bar.Close),
				"setup":     "ai_scalp_exit",
				"regime_5m": regimeTag,
			})
			if err != nil {
				return aiSt, nil, err
			}
			aiSt.PositionSide = ""
			aiSt.CooldownUntil = now.Add(cooldown)
			return aiSt, []start.Signal{sig}, nil
		}
	}
	if aiSt.PositionSide == start.SideSell && aiSt.PendingEntry == "" {
		exitShort := rsi <= (100-cfg.RSIExitMid) || stochK <= cfg.StochLong || regimeIsTrend
		if exitShort {
			sig, err := start.NewSignal(instanceID, symbol, start.SignalExit, start.SideBuy, 0.8, map[string]string{
				"ref_price": fmt.Sprintf("%.10f", bar.Close),
				"setup":     "ai_scalp_exit",
				"regime_5m": regimeTag,
			})
			if err != nil {
				return aiSt, nil, err
			}
			aiSt.PositionSide = ""
			aiSt.CooldownUntil = now.Add(cooldown)
			return aiSt, []start.Signal{sig}, nil
		}
	}

	// 2. Cooldown / max trades gate (only blocks entries).
	if now.Before(aiSt.CooldownUntil) {
		return aiSt, nil, nil
	}
	if aiSt.TradesToday >= cfg.MaxTradesPerDay {
		return aiSt, nil, nil
	}

	// 3. Only entries if flat and regime allowed.
	if aiSt.PositionSide != "" || aiSt.PendingEntry != "" {
		return aiSt, nil, nil
	}
	if !regimeAllowed {
		return aiSt, nil, nil
	}

	// 4. Long entry: RSI < RSILong AND StochK < StochLong AND crossUp.
	if rsi < cfg.RSILong && stochK < cfg.StochLong && crossUp {
		tags := map[string]string{
			"ref_price": fmt.Sprintf("%.10f", bar.Close),
			"setup":     "ai_scalp_long",
			"cross":     "up",
			"rsi":       fmt.Sprintf("%.2f", rsi),
			"stochk":    fmt.Sprintf("%.2f", stochK),
			"stochd":    fmt.Sprintf("%.2f", stochD),
			"regime_5m": regimeTag,
		}
		if cfg.AIEnabled {
			reqID := fmt.Sprintf("%s:%s:%s:%d", s.meta.ID, s.meta.Version, symbol, now.UnixNano())
			aiSt.PendingAIRequestID = reqID
			tags["ai_requested"] = "true"
			tags["ai_request_id"] = reqID
		}
		sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideBuy, 0.7, tags)
		if err != nil {
			return aiSt, nil, err
		}
		aiSt.PendingEntry = start.SideBuy
		aiSt.PendingEntryAt = now
		aiSt.TradesToday++
		aiSt.CooldownUntil = now.Add(cooldown)
		aiSt.LastBarClose = bar.Close
		return aiSt, []start.Signal{sig}, nil
	}

	// 5. Short entry: RSI > RSIShort AND StochK > StochShort AND crossDown.
	if rsi > cfg.RSIShort && stochK > cfg.StochShort && crossDown {
		tags := map[string]string{
			"ref_price": fmt.Sprintf("%.10f", bar.Close),
			"setup":     "ai_scalp_short",
			"cross":     "down",
			"rsi":       fmt.Sprintf("%.2f", rsi),
			"stochk":    fmt.Sprintf("%.2f", stochK),
			"stochd":    fmt.Sprintf("%.2f", stochD),
			"regime_5m": regimeTag,
		}
		if cfg.AIEnabled {
			reqID := fmt.Sprintf("%s:%s:%s:%d", s.meta.ID, s.meta.Version, symbol, now.UnixNano())
			aiSt.PendingAIRequestID = reqID
			tags["ai_requested"] = "true"
			tags["ai_request_id"] = reqID
		}
		sig, err := start.NewSignal(instanceID, symbol, start.SignalEntry, start.SideSell, 0.7, tags)
		if err != nil {
			return aiSt, nil, err
		}
		aiSt.PendingEntry = start.SideSell
		aiSt.PendingEntryAt = now
		aiSt.TradesToday++
		aiSt.CooldownUntil = now.Add(cooldown)
		aiSt.LastBarClose = bar.Close
		return aiSt, []start.Signal{sig}, nil
	}

	return aiSt, nil, nil
}

// OnEvent handles fill confirmations, entry rejections, AI debate results,
// and other async events.
func (s *AIScalperStrategy) OnEvent(ctx start.Context, symbol string, evt any, st start.State) (start.State, []start.Signal, error) {
	aiSt, ok := st.(*AIScalperState)
	if !ok {
		return st, nil, fmt.Errorf("AIScalperStrategy.OnEvent: expected *AIScalperState, got %T", st)
	}

	switch e := evt.(type) {
	case start.FillConfirmation:
		// Entry was filled — promote PendingEntry to confirmed PositionSide.
		if aiSt.PendingEntry != "" {
			aiSt.PositionSide = aiSt.PendingEntry
			aiSt.PendingEntry = ""
			aiSt.PendingEntryAt = time.Time{}
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Info("entry filled — position confirmed",
					"side", aiSt.PositionSide,
					"price", e.Price,
				)
			}
		}
		return aiSt, nil, nil

	case start.EntryRejection:
		// Entry was rejected — clear PendingEntry so strategy can re-evaluate.
		if aiSt.PendingEntry != "" {
			if ctx != nil && ctx.Logger() != nil {
				ctx.Logger().Warn("entry rejected — clearing pending state",
					"side", aiSt.PendingEntry,
					"reason", e.Reason,
				)
			}
			aiSt.PendingEntry = ""
			aiSt.PendingEntryAt = time.Time{}
		}
		return aiSt, nil, nil

	case AIDebateResult:
		return s.handleAIDebate(ctx, symbol, e, aiSt)

	default:
		return st, nil, nil
	}
}

// handleAIDebate processes AI debate results (extracted from OnEvent for clarity).
func (s *AIScalperStrategy) handleAIDebate(ctx start.Context, symbol string, result AIDebateResult, aiSt *AIScalperState) (start.State, []start.Signal, error) {
	// Ignore if request ID doesn't match pending.
	if result.RequestID != aiSt.PendingAIRequestID {
		return aiSt, nil, nil
	}

	instanceID, _ := start.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))

	now := time.Now()
	if ctx != nil {
		now = ctx.Now()
	}

	// Clear pending and update AI state.
	aiSt.PendingAIRequestID = ""
	aiSt.LastAIVerdict = result.Verdict
	aiSt.LastAIConfidence = result.Confidence
	aiSt.LastAIAt = now

	switch result.Verdict {
	case "veto":
		// Veto: flatten position.
		exitSide := start.SideSell
		if aiSt.PositionSide == start.SideSell {
			exitSide = start.SideBuy
		}
		sig, err := start.NewSignal(instanceID, symbol, start.SignalFlat, exitSide, 0.9, map[string]string{
			"ref_price":  fmt.Sprintf("%.10f", aiSt.LastBarClose),
			"setup":      "ai_veto",
			"ai_verdict": "veto",
			"ai_conf":    fmt.Sprintf("%.2f", result.Confidence),
		})
		if err != nil {
			return aiSt, nil, err
		}
		aiSt.PositionSide = ""
		return aiSt, []start.Signal{sig}, nil

	case "bull", "bear":
		// Adjust sizing based on agreement with position direction.
		var sizeMult float64
		agrees := (result.Verdict == "bull" && aiSt.PositionSide == start.SideBuy) ||
			(result.Verdict == "bear" && aiSt.PositionSide == start.SideSell)
		if agrees {
			sizeMult = aiSt.Config.SizeMultMax
		} else {
			sizeMult = aiSt.Config.SizeMultMin
		}
		aiSt.LastSizeMult = sizeMult

		sig, err := start.NewSignal(instanceID, symbol, start.SignalAdjust, aiSt.PositionSide, 0.7, map[string]string{
			"ref_price":  fmt.Sprintf("%.10f", aiSt.LastBarClose),
			"setup":      "ai_adjust",
			"ai_verdict": result.Verdict,
			"ai_conf":    fmt.Sprintf("%.2f", result.Confidence),
			"size_mult":  fmt.Sprintf("%.2f", sizeMult),
		})
		if err != nil {
			return aiSt, nil, err
		}
		return aiSt, []start.Signal{sig}, nil

	default:
		// neutral or unknown — no action.
		return aiSt, nil, nil
	}
}

// --- Serialization ---

type aiScalperStateJSON struct {
	Symbol             string              `json:"symbol"`
	Config             AIScalperConfig     `json:"config"`
	Indicators         start.IndicatorData `json:"indicators"`
	PrevStochK         float64             `json:"prev_stoch_k"`
	PrevStochD         float64             `json:"prev_stoch_d"`
	TradesToday        int                 `json:"trades_today"`
	CooldownUntil      time.Time           `json:"cooldown_until"`
	PositionSide       start.Side          `json:"position_side"`
	PendingEntry       start.Side          `json:"pending_entry"`
	PendingEntryAt     time.Time           `json:"pending_entry_at"`
	PendingAIRequestID string              `json:"pending_ai_request_id"`
	LastAIVerdict      string              `json:"last_ai_verdict"`
	LastAIConfidence   float64             `json:"last_ai_confidence"`
	LastAIAt           time.Time           `json:"last_ai_at"`
	LastSizeMult       float64             `json:"last_size_mult"`
	LastBarClose       float64             `json:"last_bar_close"`
}

func (s *AIScalperState) Marshal() ([]byte, error) {
	j := aiScalperStateJSON{
		Symbol:             s.Symbol,
		Config:             s.Config,
		Indicators:         s.Indicators,
		PrevStochK:         s.PrevStochK,
		PrevStochD:         s.PrevStochD,
		TradesToday:        s.TradesToday,
		CooldownUntil:      s.CooldownUntil,
		PositionSide:       s.PositionSide,
		PendingEntry:       s.PendingEntry,
		PendingEntryAt:     s.PendingEntryAt,
		PendingAIRequestID: s.PendingAIRequestID,
		LastAIVerdict:      s.LastAIVerdict,
		LastAIConfidence:   s.LastAIConfidence,
		LastAIAt:           s.LastAIAt,
		LastSizeMult:       s.LastSizeMult,
		LastBarClose:       s.LastBarClose,
	}
	return json.Marshal(j)
}

func (s *AIScalperState) Unmarshal(data []byte) error {
	var j aiScalperStateJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("AIScalperState.Unmarshal: %w", err)
	}
	s.Symbol = j.Symbol
	s.Config = j.Config
	s.Indicators = j.Indicators
	s.PrevStochK = j.PrevStochK
	s.PrevStochD = j.PrevStochD
	s.TradesToday = j.TradesToday
	s.CooldownUntil = j.CooldownUntil
	s.PositionSide = j.PositionSide
	s.PendingEntry = j.PendingEntry
	s.PendingEntryAt = j.PendingEntryAt
	s.PendingAIRequestID = j.PendingAIRequestID
	s.LastAIVerdict = j.LastAIVerdict
	s.LastAIConfidence = j.LastAIConfidence
	s.LastAIAt = j.LastAIAt
	s.LastSizeMult = j.LastSizeMult
	s.LastBarClose = j.LastBarClose
	return nil
}
