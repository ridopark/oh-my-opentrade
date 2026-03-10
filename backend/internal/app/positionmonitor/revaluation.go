package positionmonitor

import (
	"context"
	"encoding/json"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

func (s *Service) ApplyRevaluation(key string, result *domain.RiskRevaluation) []domain.ExitRuleChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos, ok := s.positions[key]
	if !ok {
		return nil
	}
	pos.LastRevaluation = result
	pos.LastRevaluationAt = result.EvaluatedAt
	if result.Action != domain.RiskActionTighten {
		return nil
	}

	oldRules := pos.ExitRules
	candidateRules := applyRiskModifierToExitRules(pos.InitialExitRules, result.UpdatedModifier)

	for i, newRule := range candidateRules {
		if i < len(oldRules) {
			for k, newV := range newRule.Params {
				if oldV, exists := oldRules[i].Params[k]; exists && newV > oldV {
					s.log.Warn().
						Str("symbol", string(pos.Symbol)).
						Str("rule", string(newRule.Type)).
						Str("param", k).
						Float64("old_value", oldV).
						Float64("new_value", newV).
						Str("modifier", string(result.UpdatedModifier)).
						Msg("TIGHTEN rejected — modifier would loosen exit rule")
					return nil
				}
			}
		}
	}

	pos.ExitRules = candidateRules

	var changes []domain.ExitRuleChange
	for i, newRule := range pos.ExitRules {
		if i < len(oldRules) {
			for k, newV := range newRule.Params {
				if oldV, exists := oldRules[i].Params[k]; exists {
					changes = append(changes, domain.ExitRuleChange{
						Rule:     string(newRule.Type),
						Param:    k,
						OldValue: oldV,
						NewValue: newV,
					})
					if oldV != newV {
						s.log.Info().
							Str("symbol", string(pos.Symbol)).
							Str("rule", string(newRule.Type)).
							Str("param", k).
							Float64("old_value", oldV).
							Float64("new_value", newV).
							Msg("exit rule tightened")
					}
				}
			}
		}
	}
	return changes
}

func (s *Service) SetEntryThesis(key string, thesis *domain.EntryThesis) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos, ok := s.positions[key]
	if !ok {
		return
	}
	pos.EntryThesis = thesis
}

// PersistThesis serializes the entry thesis to JSON and writes it to the most
// recent BUY trade for the given symbol. This ensures thesis survives restarts.
func (s *Service) PersistThesis(ctx context.Context, symbol domain.Symbol, thesis *domain.EntryThesis) {
	if s.repo == nil || thesis == nil {
		return
	}
	raw, err := json.Marshal(thesis)
	if err != nil {
		s.log.Warn().Err(err).Str("symbol", string(symbol)).Msg("persist thesis: marshal failed")
		return
	}
	if err := s.repo.UpdateTradeThesis(ctx, s.tenantID, s.envMode, symbol, raw); err != nil {
		s.log.Warn().Err(err).Str("symbol", string(symbol)).Msg("persist thesis: DB update failed")
		return
	}
	s.log.Info().Str("symbol", string(symbol)).Msg("entry thesis persisted to trade record")
}

// applyRiskModifierToExitRules scales TRAILING_STOP and MAX_LOSS pct params
// based on the AI judge's risk modifier. TIGHT tightens stops (0.9x per cycle),
// WIDE gives more room (1.5x). NORMAL/empty returns rules unchanged.
func applyRiskModifierToExitRules(rules []domain.ExitRule, modifier domain.RiskModifier) []domain.ExitRule {
	if modifier == domain.RiskModifierNormal || modifier == "" {
		return rules
	}

	var mult float64
	switch modifier {
	case domain.RiskModifierTight:
		mult = 0.90
	case domain.RiskModifierWide:
		mult = 1.50
	default:
		return rules
	}

	scaled := make([]domain.ExitRule, len(rules))
	for i, r := range rules {
		switch {
		case r.Type == domain.ExitRuleVolatilityStop && r.Params["atr_multiplier"] > 0:
			newParams := make(map[string]float64, len(r.Params))
			for k, v := range r.Params {
				newParams[k] = v
			}
			newParams["atr_multiplier"] = r.Params["atr_multiplier"] * mult
			scaled[i] = domain.ExitRule{Type: r.Type, Params: newParams}
		case r.Type == domain.ExitRuleSDTarget && r.Params["sd_level"] > 0:
			newParams := make(map[string]float64, len(r.Params))
			for k, v := range r.Params {
				newParams[k] = v
			}
			newParams["sd_level"] = r.Params["sd_level"] * mult
			scaled[i] = domain.ExitRule{Type: r.Type, Params: newParams}
		case r.Type == domain.ExitRuleStagnationExit && r.Params["minutes"] > 0:
			newParams := make(map[string]float64, len(r.Params))
			for k, v := range r.Params {
				newParams[k] = v
			}
			newParams["minutes"] = r.Params["minutes"] * mult
			scaled[i] = domain.ExitRule{Type: r.Type, Params: newParams}
		case (r.Type == domain.ExitRuleTrailingStop || r.Type == domain.ExitRuleMaxLoss) && r.Params["pct"] > 0:
			newParams := make(map[string]float64, len(r.Params))
			for k, v := range r.Params {
				newParams[k] = v
			}
			newParams["pct"] = r.Params["pct"] * mult
			scaled[i] = domain.ExitRule{Type: r.Type, Params: newParams}
		default:
			scaled[i] = r
		}
	}
	return scaled
}
