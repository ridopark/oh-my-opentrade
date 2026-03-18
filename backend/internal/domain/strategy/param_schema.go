package strategy

import "sort"

// ParamMeta describes a single strategy parameter for UI form rendering.
type ParamMeta struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Default     any      `json:"default"`
	Description string   `json:"description,omitempty"`
	Group       string   `json:"group"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Step        *float64 `json:"step,omitempty"`
}

// paramRange defines min/max/step for a known numeric parameter.
type paramRange struct {
	Min, Max, Step float64
}

// knownRanges maps parameter keys to their valid ranges.
var knownRanges = map[string]paramRange{
	"hold_bars":            {Min: 1, Max: 50, Step: 1},
	"exit_hold_bars":       {Min: 1, Max: 20, Step: 1},
	"volume_mult":          {Min: 0, Max: 5, Step: 0.1},
	"rsi_bounce_max":       {Min: 10, Max: 80, Step: 1},
	"stop_bps":             {Min: 10, Max: 500, Step: 5},
	"cooldown_bars":        {Min: 0, Max: 50, Step: 1},
	"max_trades_per_day":   {Min: 1, Max: 20, Step: 1},
	"atr_multiplier":       {Min: 0.5, Max: 10, Step: 0.5},
	"sd_level":             {Min: 0.5, Max: 5, Step: 0.1},
	"min_hold_bars":        {Min: 1, Max: 30, Step: 1},
	"minutes":              {Min: 5, Max: 120, Step: 5},
	"sd_threshold":         {Min: 0.5, Max: 5, Step: 0.1},
	"profit_gate_pct":      {Min: 0, Max: 5, Step: 0.1},
	"pct":                  {Min: 0, Max: 100, Step: 1},
	"minutes_before_close": {Min: 5, Max: 60, Step: 5},
}

// knownDescriptions maps parameter keys to human-readable descriptions.
var knownDescriptions = map[string]string{
	"hold_bars":            "Bars price must stay above/below AVWAP before entry",
	"exit_hold_bars":       "Bars below/above AVWAP before strategy-level exit",
	"volume_mult":          "Volume multiplier filter (0 = disabled)",
	"rsi_bounce_max":       "Max RSI for bounce entry qualification",
	"stop_bps":             "Hard stop loss in basis points",
	"cooldown_bars":        "Bars to wait after exit before new entry",
	"max_trades_per_day":   "Maximum trades allowed per trading day",
	"atr_multiplier":       "ATR multiplier for volatility stop distance",
	"sd_level":             "Standard deviation level for profit target",
	"min_hold_bars":        "Minimum bars before step stop can fire",
	"minutes":              "Duration in minutes for stagnation exit",
	"sd_threshold":         "Standard deviation threshold",
	"profit_gate_pct":      "Minimum profit percentage before trailing activates",
	"pct":                  "Percentage value",
	"minutes_before_close": "Minutes before market close for EOD exit",
}

// groupPrefixes maps key prefixes to UI group names.
var groupPrefixes = []struct {
	prefix string
	group  string
}{
	{"regime_filter.", "Regime Filter"},
	{"dynamic_risk.", "Dynamic Risk"},
}

// InferParamSchema builds typed parameter metadata from a raw params map.
// The descriptions map provides optional overrides for parameter descriptions.
func InferParamSchema(params map[string]any, descriptions map[string]string) []ParamMeta {
	if len(params) == 0 {
		return nil
	}

	metas := make([]ParamMeta, 0, len(params))
	for key, val := range params {
		m := ParamMeta{
			Key:     key,
			Type:    inferType(val),
			Default: val,
			Group:   inferGroup(key),
		}

		if descriptions != nil {
			if desc, ok := descriptions[key]; ok {
				m.Description = desc
			}
		}
		if m.Description == "" {
			lookupKey := key
			for _, gp := range groupPrefixes {
				if len(key) > len(gp.prefix) && key[:len(gp.prefix)] == gp.prefix {
					lookupKey = key[len(gp.prefix):]
					break
				}
			}
			if desc, ok := knownDescriptions[lookupKey]; ok {
				m.Description = desc
			}
		}

		lookupKey := key
		for _, gp := range groupPrefixes {
			if len(key) > len(gp.prefix) && key[:len(gp.prefix)] == gp.prefix {
				lookupKey = key[len(gp.prefix):]
				break
			}
		}
		if r, ok := knownRanges[lookupKey]; ok && (m.Type == "integer" || m.Type == "number") {
			min, max, step := r.Min, r.Max, r.Step
			m.Min = &min
			m.Max = &max
			m.Step = &step
		}

		metas = append(metas, m)
	}

	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Group != metas[j].Group {
			return metas[i].Group < metas[j].Group
		}
		return metas[i].Key < metas[j].Key
	})

	return metas
}

func inferType(v any) string {
	switch v.(type) {
	case int, int64:
		return "integer"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []string:
		return "string_array"
	case []any:
		return "string_array"
	default:
		return "string"
	}
}

func inferGroup(key string) string {
	for _, gp := range groupPrefixes {
		if len(key) > len(gp.prefix) && key[:len(gp.prefix)] == gp.prefix {
			return gp.group
		}
	}
	return "Strategy Params"
}
