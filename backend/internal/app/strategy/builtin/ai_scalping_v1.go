package builtin

import (
	"encoding/json"
	"fmt"
	"time"

	strat "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// AIScalperStrategy implements RSI/Stochastic mean-reversion scalping
// with optional AI debate overlay for sizing/veto.
type AIScalperStrategy struct {
	meta strat.Meta
}

// NewAIScalperStrategy creates a new AI-Enhanced Scalping strategy.
func NewAIScalperStrategy() *AIScalperStrategy {
	id, _ := strat.NewStrategyID("ai_scalping_v1")
	ver, _ := strat.NewVersion("1.0.0")
	return &AIScalperStrategy{
		meta: strat.Meta{
			ID:          id,
			Version:     ver,
			Name:        "AI-Enhanced Scalping",
			Description: "RSI/Stochastic mean-reversion scalping aligned with 5m regime, AI debate overlay",
			Author:      "system",
		},
	}
}

func (s *AIScalperStrategy) Meta() strat.Meta { return s.meta }
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
	Indicators         strat.IndicatorData
	PrevStochK         float64
	PrevStochD         float64
	TradesToday        int
	CooldownUntil      time.Time
	PositionSide       strat.Side
	PendingAIRequestID string
	LastAIVerdict      string
	LastAIConfidence   float64
	LastAIAt           time.Time
	LastSizeMult       float64
	LastBarClose       float64
	Config             AIScalperConfig
}

// SetIndicators implements the indicatorSetter interface.
func (s *AIScalperState) SetIndicators(ind strat.IndicatorData) {
	s.Indicators = ind
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
func (s *AIScalperStrategy) Init(ctx strat.Context, symbol string, params map[string]any, prior strat.State) (strat.State, error) {
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
func (s *AIScalperStrategy) OnBar(ctx strat.Context, symbol string, bar strat.Bar, st strat.State) (strat.State, []strat.Signal, error) {
	aiSt, ok := st.(*AIScalperState)
	if !ok {
		return st, nil, fmt.Errorf("AIScalperStrategy.OnBar: expected *AIScalperState, got %T", st)
	}
	cfg := aiSt.Config

	now := bar.Time
	if ctx != nil {
		now = ctx.Now()
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

	instanceID, _ := strat.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second

	// 1. Exit signals (always checked, even during cooldown).
	if aiSt.PositionSide == strat.SideBuy {
		exitLong := rsi >= cfg.RSIExitMid || stochK >= cfg.StochShort || regimeIsTrend
		if exitLong {
			sig, err := strat.NewSignal(instanceID, symbol, strat.SignalExit, strat.SideSell, 0.8, map[string]string{
				"ref_price": fmt.Sprintf("%.4f", bar.Close),
				"setup":     "ai_scalp_exit",
				"regime_5m": regimeTag,
			})
			if err != nil {
				return aiSt, nil, err
			}
			aiSt.PositionSide = ""
			aiSt.CooldownUntil = now.Add(cooldown)
			return aiSt, []strat.Signal{sig}, nil
		}
	}
	if aiSt.PositionSide == strat.SideSell {
		exitShort := rsi <= (100-cfg.RSIExitMid) || stochK <= cfg.StochLong || regimeIsTrend
		if exitShort {
			sig, err := strat.NewSignal(instanceID, symbol, strat.SignalExit, strat.SideBuy, 0.8, map[string]string{
				"ref_price": fmt.Sprintf("%.4f", bar.Close),
				"setup":     "ai_scalp_exit",
				"regime_5m": regimeTag,
			})
			if err != nil {
				return aiSt, nil, err
			}
			aiSt.PositionSide = ""
			aiSt.CooldownUntil = now.Add(cooldown)
			return aiSt, []strat.Signal{sig}, nil
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
	if aiSt.PositionSide != "" {
		return aiSt, nil, nil
	}
	if !regimeAllowed {
		return aiSt, nil, nil
	}

	// 4. Long entry: RSI < RSILong AND StochK < StochLong AND crossUp.
	if rsi < cfg.RSILong && stochK < cfg.StochLong && crossUp {
		tags := map[string]string{
			"ref_price": fmt.Sprintf("%.4f", bar.Close),
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
		sig, err := strat.NewSignal(instanceID, symbol, strat.SignalEntry, strat.SideBuy, 0.7, tags)
		if err != nil {
			return aiSt, nil, err
		}
		aiSt.PositionSide = strat.SideBuy
		aiSt.TradesToday++
		aiSt.CooldownUntil = now.Add(cooldown)
		aiSt.LastBarClose = bar.Close
		return aiSt, []strat.Signal{sig}, nil
	}

	// 5. Short entry: RSI > RSIShort AND StochK > StochShort AND crossDown.
	if rsi > cfg.RSIShort && stochK > cfg.StochShort && crossDown {
		tags := map[string]string{
			"ref_price": fmt.Sprintf("%.4f", bar.Close),
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
		sig, err := strat.NewSignal(instanceID, symbol, strat.SignalEntry, strat.SideSell, 0.7, tags)
		if err != nil {
			return aiSt, nil, err
		}
		aiSt.PositionSide = strat.SideSell
		aiSt.TradesToday++
		aiSt.CooldownUntil = now.Add(cooldown)
		aiSt.LastBarClose = bar.Close
		return aiSt, []strat.Signal{sig}, nil
	}

	return aiSt, nil, nil
}

// OnEvent handles AI debate results and other async events.
func (s *AIScalperStrategy) OnEvent(ctx strat.Context, symbol string, evt any, st strat.State) (strat.State, []strat.Signal, error) {
	aiSt, ok := st.(*AIScalperState)
	if !ok {
		return st, nil, fmt.Errorf("AIScalperStrategy.OnEvent: expected *AIScalperState, got %T", st)
	}

	result, ok := evt.(AIDebateResult)
	if !ok {
		return st, nil, nil
	}

	// Ignore if request ID doesn't match pending.
	if result.RequestID != aiSt.PendingAIRequestID {
		return aiSt, nil, nil
	}

	instanceID, _ := strat.NewInstanceID(fmt.Sprintf("%s:%s:%s", s.meta.ID, s.meta.Version, symbol))

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
		exitSide := strat.SideSell
		if aiSt.PositionSide == strat.SideSell {
			exitSide = strat.SideBuy
		}
		sig, err := strat.NewSignal(instanceID, symbol, strat.SignalFlat, exitSide, 0.9, map[string]string{
			"ref_price":  fmt.Sprintf("%.4f", aiSt.LastBarClose),
			"setup":      "ai_veto",
			"ai_verdict": "veto",
			"ai_conf":    fmt.Sprintf("%.2f", result.Confidence),
		})
		if err != nil {
			return aiSt, nil, err
		}
		aiSt.PositionSide = ""
		return aiSt, []strat.Signal{sig}, nil

	case "bull", "bear":
		// Adjust sizing based on agreement with position direction.
		var sizeMult float64
		agrees := (result.Verdict == "bull" && aiSt.PositionSide == strat.SideBuy) ||
			(result.Verdict == "bear" && aiSt.PositionSide == strat.SideSell)
		if agrees {
			sizeMult = aiSt.Config.SizeMultMax
		} else {
			sizeMult = aiSt.Config.SizeMultMin
		}
		aiSt.LastSizeMult = sizeMult

		sig, err := strat.NewSignal(instanceID, symbol, strat.SignalAdjust, aiSt.PositionSide, 0.7, map[string]string{
			"ref_price":  fmt.Sprintf("%.4f", aiSt.LastBarClose),
			"setup":      "ai_adjust",
			"ai_verdict": result.Verdict,
			"ai_conf":    fmt.Sprintf("%.2f", result.Confidence),
			"size_mult":  fmt.Sprintf("%.2f", sizeMult),
		})
		if err != nil {
			return aiSt, nil, err
		}
		return aiSt, []strat.Signal{sig}, nil

	default:
		// neutral or unknown — no action.
		return aiSt, nil, nil
	}
}

// --- Serialization ---

type aiScalperStateJSON struct {
	Symbol             string              `json:"symbol"`
	Config             AIScalperConfig     `json:"config"`
	Indicators         strat.IndicatorData `json:"indicators"`
	PrevStochK         float64             `json:"prev_stoch_k"`
	PrevStochD         float64             `json:"prev_stoch_d"`
	TradesToday        int                 `json:"trades_today"`
	CooldownUntil      time.Time           `json:"cooldown_until"`
	PositionSide       strat.Side          `json:"position_side"`
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
	s.PendingAIRequestID = j.PendingAIRequestID
	s.LastAIVerdict = j.LastAIVerdict
	s.LastAIConfidence = j.LastAIConfidence
	s.LastAIAt = j.LastAIAt
	s.LastSizeMult = j.LastSizeMult
	s.LastBarClose = j.LastBarClose
	return nil
}
