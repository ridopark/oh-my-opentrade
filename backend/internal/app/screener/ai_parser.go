package screener

import (
	"encoding/json"
	"fmt"
	"strings"
)

type AISymbolScore struct {
	AnonID    string `json:"id"`
	Score     int    `json:"score"`
	Rationale string `json:"rationale"`
}

func ParseAIScreenerResponse(raw string) ([]AISymbolScore, error) {
	cleaned := extractJSON(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("ai_parser: no JSON array found in response")
	}

	var scores []AISymbolScore
	if err := json.Unmarshal([]byte(cleaned), &scores); err != nil {
		return nil, fmt.Errorf("ai_parser: failed to parse JSON: %w", err)
	}

	for i, s := range scores {
		if s.AnonID == "" {
			return nil, fmt.Errorf("ai_parser: entry %d has empty id", i)
		}
		if s.Score < 0 || s.Score > 5 {
			return nil, fmt.Errorf("ai_parser: entry %d (%s) score %d out of range 0-5", i, s.AnonID, s.Score)
		}
	}

	return scores, nil
}

func extractJSON(raw string) string {
	if idx := strings.Index(raw, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			return strings.TrimSpace(raw[start : start+end])
		}
	}
	if idx := strings.Index(raw, "```"); idx >= 0 {
		start := idx + len("```")
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			candidate := strings.TrimSpace(raw[start : start+end])
			if strings.HasPrefix(candidate, "[") {
				return candidate
			}
		}
	}

	if start := strings.Index(raw, "["); start >= 0 {
		if end := strings.LastIndex(raw, "]"); end > start {
			return strings.TrimSpace(raw[start : end+1])
		}
	}

	return ""
}
