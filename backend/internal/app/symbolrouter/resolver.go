package symbolrouter

import "github.com/oh-my-opentrade/backend/internal/domain/screener"

// ResolveEffectiveSymbols computes the effective watchlist for a strategy given:
//   - mode: "static"|"replace"|"intersection"|"union" (default/unknown → intersection)
//   - base: static symbols from DNA TOML [routing].symbols
//   - ranked: screener-ranked symbols from EventScreenerCompleted
//
// Returns the effective symbol list and a source label for observability.
func ResolveEffectiveSymbols(mode string, base []string, ranked []screener.RankedSymbol) ([]string, string) {
	screenerSyms := make([]string, len(ranked))
	for i, r := range ranked {
		screenerSyms[i] = r.Symbol
	}

	if len(screenerSyms) == 0 {
		if len(base) == 0 {
			return []string{}, "fallback:no_screener_results"
		}
		return copySlice(base), "fallback:no_screener_results"
	}

	switch mode {
	case "static":
		return copySlice(base), "static"
	case "replace":
		return screenerSyms, "screener"
	case "union":
		return union(screenerSyms, base), "union"
	default:
		return intersection(screenerSyms, base), "intersection"
	}
}

func intersection(screenerOrder, base []string) []string {
	set := make(map[string]struct{}, len(base))
	for _, s := range base {
		set[s] = struct{}{}
	}
	var out []string
	for _, s := range screenerOrder {
		if _, ok := set[s]; ok {
			out = append(out, s)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

func union(screenerOrder, base []string) []string {
	seen := make(map[string]struct{}, len(screenerOrder)+len(base))
	var out []string
	for _, s := range screenerOrder {
		if _, ok := seen[s]; !ok {
			out = append(out, s)
			seen[s] = struct{}{}
		}
	}
	for _, s := range base {
		if _, ok := seen[s]; !ok {
			out = append(out, s)
			seen[s] = struct{}{}
		}
	}
	return out
}

func copySlice(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}
