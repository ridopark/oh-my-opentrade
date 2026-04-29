package copytradereplay

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

type historyRow struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	TS     string `json:"ts"`
	Text   string `json:"text"`
}

type groundTruthRow struct {
	MessageID string  `json:"message_id"`
	LineIndex int     `json:"line_index"`
	SignalID  string  `json:"signal_id"`
	Author    string  `json:"author"`
	PostedAt  string  `json:"posted_at"`
	Action    string  `json:"action"`
	Ticker    string  `json:"ticker"`
	Expiry    string  `json:"expiry"`
	Strike    float64 `json:"strike"`
	Right     string  `json:"right"`
	Price     float64 `json:"price"`
	Tail      string  `json:"tail"`
	RawLine   string  `json:"raw_line"`
	Dropped   bool    `json:"dropped"`
	Text      string  `json:"text"`
}

func loadHistory(t *testing.T, path string) []historyRow {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	defer f.Close()
	var out []historyRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r historyRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("unmarshal history: %v", err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan history: %v", err)
	}
	return out
}

func loadGroundTruth(t *testing.T, path string) map[string][]groundTruthRow {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open ground truth: %v", err)
	}
	defer f.Close()
	out := make(map[string][]groundTruthRow)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r groundTruthRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("unmarshal ground truth: %v", err)
		}
		out[r.MessageID] = append(out[r.MessageID], r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan ground truth: %v", err)
	}
	return out
}

func TestParseMessage_GoldenAgainstPython(t *testing.T) {
	history := loadHistory(t, "testdata/history_90d.jsonl")
	truth := loadGroundTruth(t, "testdata/parsed_ground_truth.jsonl")

	var totalSignals, totalDrops int
	byAction := map[string]int{}

	for _, msg := range history {
		postedAt, err := time.Parse(time.RFC3339Nano, msg.TS)
		if err != nil {
			t.Fatalf("%s: parse ts: %v", msg.ID, err)
		}
		got := ParseMessage(msg.Text, postedAt)

		expected := truth[msg.ID]
		if len(expected) == 0 {
			t.Fatalf("%s: missing from ground truth", msg.ID)
		}

		if len(expected) == 1 && expected[0].Dropped {
			if len(got) != 0 {
				t.Errorf("%s: expected drop, got %d signals: %+v", msg.ID, len(got), got)
			}
			totalDrops++
			continue
		}

		if len(got) != len(expected) {
			t.Errorf("%s: signal count mismatch, got=%d want=%d", msg.ID, len(got), len(expected))
			continue
		}

		for i, exp := range expected {
			g := got[i]
			totalSignals++
			byAction[g.Action]++
			if g.Action != exp.Action {
				t.Errorf("%s[%d]: action got=%q want=%q", msg.ID, i, g.Action, exp.Action)
			}
			if g.Ticker != exp.Ticker {
				t.Errorf("%s[%d]: ticker got=%q want=%q", msg.ID, i, g.Ticker, exp.Ticker)
			}
			if g.Right != exp.Right {
				t.Errorf("%s[%d]: right got=%q want=%q", msg.ID, i, g.Right, exp.Right)
			}
			if !floatsEqual(g.Strike, exp.Strike) {
				t.Errorf("%s[%d]: strike got=%v want=%v", msg.ID, i, g.Strike, exp.Strike)
			}
			if !floatsEqual(g.Price, exp.Price) {
				t.Errorf("%s[%d]: price got=%v want=%v", msg.ID, i, g.Price, exp.Price)
			}
			expExpiry, err := time.Parse("2006-01-02", exp.Expiry)
			if err != nil {
				t.Fatalf("%s[%d]: parse expected expiry: %v", msg.ID, i, err)
			}
			if !g.Expiry.Equal(expExpiry) {
				t.Errorf("%s[%d]: expiry got=%s want=%s", msg.ID, i, g.Expiry.Format("2006-01-02"), exp.Expiry)
			}
			if g.Tail != exp.Tail {
				t.Errorf("%s[%d]: tail got=%q want=%q", msg.ID, i, g.Tail, exp.Tail)
			}
			if g.RawLine != exp.RawLine {
				t.Errorf("%s[%d]: raw_line got=%q want=%q", msg.ID, i, g.RawLine, exp.RawLine)
			}
		}
	}

	t.Logf("parsed %d signals (%+v), dropped %d messages", totalSignals, byAction, totalDrops)

	if totalSignals != 200 {
		t.Errorf("total signals got=%d want=200", totalSignals)
	}
	if totalDrops != 61 {
		t.Errorf("total drops got=%d want=61", totalDrops)
	}
	if byAction["BTO"] != 79 {
		t.Errorf("BTO count got=%d want=79", byAction["BTO"])
	}
	if byAction["STC"] != 106 {
		t.Errorf("STC count got=%d want=106", byAction["STC"])
	}
	if byAction["AVG"] != 15 {
		t.Errorf("AVG count got=%d want=15", byAction["AVG"])
	}
}

func TestResolveExpiry_RollsForwardPastDates(t *testing.T) {
	today := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got, err := resolveExpiry("3/20", today)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, 3, 20, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got=%s want=%s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestResolveExpiry_TwoDigitYear(t *testing.T) {
	got, err := resolveExpiry("3/20/27", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, 3, 20, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got=%s want=%s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestResolveExpiry_FourDigitYear(t *testing.T) {
	got, err := resolveExpiry("03/20/2026", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got=%s want=%s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestParseMessage_MultiLine(t *testing.T) {
	text := strings.Join([]string{
		"BTO TSLA 2/4 445c @4.45",
		"Going light. Only swinging if I can get +40% on runners",
		"TP: 20%, 40%, 100%",
	}, "\n")
	got := ParseMessage(text, time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC))
	if len(got) != 1 {
		t.Fatalf("got %d signals, want 1", len(got))
	}
	if got[0].Ticker != "TSLA" || got[0].Price != 4.45 {
		t.Errorf("unexpected signal: %+v", got[0])
	}
}

func floatsEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
