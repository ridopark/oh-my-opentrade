package screener

import (
	"strings"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

func TestAnonymize(t *testing.T) {
	symbols := []string{"AAPL", "TSLA", "MSFT"}
	anon, mapping := Anonymize(symbols)

	if len(anon) != 3 {
		t.Fatalf("expected 3 anon IDs, got %d", len(anon))
	}
	if anon[0] != "ASSET_A" || anon[1] != "ASSET_B" || anon[2] != "ASSET_C" {
		t.Fatalf("unexpected anon IDs: %v", anon)
	}
	if mapping["ASSET_A"] != "AAPL" {
		t.Fatalf("expected ASSET_A -> AAPL, got %s", mapping["ASSET_A"])
	}
	if mapping["ASSET_B"] != "TSLA" {
		t.Fatalf("expected ASSET_B -> TSLA, got %s", mapping["ASSET_B"])
	}
	if mapping["ASSET_C"] != "MSFT" {
		t.Fatalf("expected ASSET_C -> MSFT, got %s", mapping["ASSET_C"])
	}
}

func TestAnonymize_MoreThan26(t *testing.T) {
	symbols := make([]string, 28)
	for i := range symbols {
		symbols[i] = "SYM" + string(rune('A'+i))
	}
	anon, _ := Anonymize(symbols)

	if anon[25] != "ASSET_Z" {
		t.Fatalf("expected ASSET_Z at index 25, got %s", anon[25])
	}
	if anon[26] != "ASSET_AA" {
		t.Fatalf("expected ASSET_AA at index 26, got %s", anon[26])
	}
	if anon[27] != "ASSET_AB" {
		t.Fatalf("expected ASSET_AB at index 27, got %s", anon[27])
	}
}

func TestDeanonymize(t *testing.T) {
	mapping := map[string]string{"ASSET_A": "AAPL", "ASSET_B": "TSLA"}

	sym, ok := Deanonymize(mapping, "ASSET_A")
	if !ok || sym != "AAPL" {
		t.Fatalf("expected AAPL, got %s (ok=%v)", sym, ok)
	}

	_, ok = Deanonymize(mapping, "ASSET_Z")
	if ok {
		t.Fatalf("expected false for unknown ID")
	}
}

func TestPass0Filter(t *testing.T) {
	cfg := config.AIScreenerConfig{
		Pass0MinPrice:  5.0,
		Pass0MinVolume: 1000,
		Pass0MinADV:    500_000,
		Pass0MinGapPct: 1.0,
	}

	snaps := map[string]ports.Snapshot{
		"PASS": {
			Symbol:          "PASS",
			PrevClose:       f64(10),
			PreMarketPrice:  f64(11.5),
			PreMarketVolume: i64(5000),
			PrevDailyVolume: i64(1_000_000),
			LastTradePrice:  f64(11),
		},
		"LOW_PRICE": {
			Symbol:          "LOW_PRICE",
			PrevClose:       f64(3),
			PreMarketPrice:  f64(3.5),
			PreMarketVolume: i64(5000),
			PrevDailyVolume: i64(1_000_000),
			LastTradePrice:  f64(3.2),
		},
		"LOW_VOL": {
			Symbol:          "LOW_VOL",
			PrevClose:       f64(10),
			PreMarketPrice:  f64(12),
			PreMarketVolume: i64(500),
			PrevDailyVolume: i64(1_000_000),
			LastTradePrice:  f64(11),
		},
		"LOW_GAP": {
			Symbol:          "LOW_GAP",
			PrevClose:       f64(100),
			PreMarketPrice:  f64(100.5),
			PreMarketVolume: i64(5000),
			PrevDailyVolume: i64(1_000_000),
			LastTradePrice:  f64(100.3),
		},
		"NIL_PM_VOL": {
			Symbol:          "NIL_PM_VOL",
			PrevClose:       f64(10),
			PreMarketPrice:  f64(11.5),
			PreMarketVolume: nil,
			PrevDailyVolume: i64(1_000_000),
			LastTradePrice:  f64(11),
		},
		"LOW_ADV": {
			Symbol:          "LOW_ADV",
			PrevClose:       f64(10),
			PreMarketPrice:  f64(11.5),
			PreMarketVolume: i64(5000),
			PrevDailyVolume: i64(100_000),
			LastTradePrice:  f64(11),
		},
		"NIL_ADV": {
			Symbol:          "NIL_ADV",
			PrevClose:       f64(10),
			PreMarketPrice:  f64(11.5),
			PreMarketVolume: i64(5000),
			PrevDailyVolume: nil,
			LastTradePrice:  f64(11),
		},
	}

	passed := Pass0Filter(snaps, cfg)

	if len(passed) != 1 {
		t.Fatalf("expected 1 pass, got %d: %v", len(passed), passed)
	}
	if passed[0] != "PASS" {
		t.Fatalf("expected PASS, got %s", passed[0])
	}
}

func TestPass0Filter_EmptySnapshots(t *testing.T) {
	cfg := config.AIScreenerConfig{Pass0MinPrice: 5.0, Pass0MinVolume: 1000, Pass0MinADV: 500_000, Pass0MinGapPct: 1.0}
	passed := Pass0Filter(map[string]ports.Snapshot{}, cfg)
	if len(passed) != 0 {
		t.Fatalf("expected empty, got %d", len(passed))
	}
}

func TestPass0Filter_ADVDisabledWhenZero(t *testing.T) {
	cfg := config.AIScreenerConfig{
		Pass0MinPrice:  5.0,
		Pass0MinVolume: 1000,
		Pass0MinADV:    0,
		Pass0MinGapPct: 1.0,
	}
	snaps := map[string]ports.Snapshot{
		"NO_ADV_CHECK": {
			Symbol:          "NO_ADV_CHECK",
			PrevClose:       f64(10),
			PreMarketPrice:  f64(11.5),
			PreMarketVolume: i64(5000),
			PrevDailyVolume: nil,
			LastTradePrice:  f64(11),
		},
	}
	passed := Pass0Filter(snaps, cfg)
	if len(passed) != 1 {
		t.Fatalf("expected 1 pass with ADV disabled, got %d", len(passed))
	}
}

func TestBuildAIScreenerPrompt(t *testing.T) {
	candidates := []CandidateData{
		{AnonID: "ASSET_A", Price: 152.30, PrevClose: 144.76, GapPct: 5.2, PMVol: 1_500_000, AvgVol: 650_000, RVOL: 2.3},
		{AnonID: "ASSET_B", Price: 42.10, PrevClose: 43.44, GapPct: -3.1, PMVol: 320_000, AvgVol: 290_909, RVOL: 1.1},
	}
	asOf := time.Date(2026, time.March, 10, 8, 35, 0, 0, mustNY())

	prompt := BuildAIScreenerPrompt("Momentum breakout strategy", candidates, asOf)

	checks := []string{
		"Momentum breakout strategy",
		"ASSET_A",
		"ASSET_B",
		"+5.20%",
		"-3.10%",
		"152.30",
		"42.10",
		"1.5M",
		"JSON array",
		"score",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("prompt missing %q", c)
		}
	}
}

func TestParseAIScreenerResponse_ValidJSON(t *testing.T) {
	raw := `[{"id":"ASSET_A","score":4,"rationale":"strong gap"},{"id":"ASSET_B","score":2,"rationale":"weak"}]`
	scores, err := ParseAIScreenerResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}
	if scores[0].AnonID != "ASSET_A" || scores[0].Score != 4 {
		t.Fatalf("unexpected first score: %+v", scores[0])
	}
	if scores[1].AnonID != "ASSET_B" || scores[1].Score != 2 {
		t.Fatalf("unexpected second score: %+v", scores[1])
	}
}

func TestParseAIScreenerResponse_MarkdownFenced(t *testing.T) {
	raw := "Here are the results:\n```json\n[{\"id\":\"ASSET_A\",\"score\":5,\"rationale\":\"perfect\"}]\n```\nDone."
	scores, err := ParseAIScreenerResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 1 || scores[0].Score != 5 {
		t.Fatalf("unexpected result: %+v", scores)
	}
}

func TestParseAIScreenerResponse_RawFenced(t *testing.T) {
	raw := "```\n[{\"id\":\"ASSET_A\",\"score\":3,\"rationale\":\"ok\"}]\n```"
	scores, err := ParseAIScreenerResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 1 || scores[0].Score != 3 {
		t.Fatalf("unexpected result: %+v", scores)
	}
}

func TestParseAIScreenerResponse_InvalidScore(t *testing.T) {
	raw := `[{"id":"ASSET_A","score":6,"rationale":"over"}]`
	_, err := ParseAIScreenerResponse(raw)
	if err == nil {
		t.Fatalf("expected error for score > 5")
	}
}

func TestParseAIScreenerResponse_EmptyID(t *testing.T) {
	raw := `[{"id":"","score":3,"rationale":"missing"}]`
	_, err := ParseAIScreenerResponse(raw)
	if err == nil {
		t.Fatalf("expected error for empty id")
	}
}

func TestParseAIScreenerResponse_NoJSON(t *testing.T) {
	raw := "I cannot process this request."
	_, err := ParseAIScreenerResponse(raw)
	if err == nil {
		t.Fatalf("expected error for no JSON")
	}
}

func TestExcludeStatic(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string
		static     []string
		want       []string
	}{
		{
			name:       "removes static symbols",
			candidates: []string{"AAPL", "TSLA", "NVDA", "AMD"},
			static:     []string{"AAPL", "TSLA"},
			want:       []string{"NVDA", "AMD"},
		},
		{
			name:       "empty static returns all",
			candidates: []string{"AAPL", "NVDA"},
			static:     []string{},
			want:       []string{"AAPL", "NVDA"},
		},
		{
			name:       "all static returns empty",
			candidates: []string{"AAPL", "TSLA"},
			static:     []string{"AAPL", "TSLA"},
			want:       []string{},
		},
		{
			name:       "no overlap returns all",
			candidates: []string{"NVDA", "AMD"},
			static:     []string{"AAPL", "TSLA"},
			want:       []string{"NVDA", "AMD"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := excludeStatic(tc.candidates, tc.static)
			if len(got) != len(tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
			wantSet := make(map[string]struct{}, len(tc.want))
			for _, s := range tc.want {
				wantSet[s] = struct{}{}
			}
			for _, s := range got {
				if _, ok := wantSet[s]; !ok {
					t.Errorf("unexpected symbol %q in result", s)
				}
			}
		})
	}
}
