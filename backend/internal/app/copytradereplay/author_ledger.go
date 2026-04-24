package copytradereplay

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
)

// PartialFractionEntry is a single (keyword, fraction) rule from the copytrade
// spec's [[params.partial_fractions]] table. The Ledger uses it to resolve
// STC closing fractions from the author's tail text.
type PartialFractionEntry struct {
	Keyword  string
	Fraction float64
}

// AuthorTrade is one reconstructed position from the author's stated prices:
// their BTO premium vs. the volume-weighted STC premium applied to the same
// position. PnL units are dollar per contract per unit underlying (i.e. what
// the copier would have made on the per-contract basis using the author's
// posted fills). Multiply by contracts actually filled to get dollar PnL in
// the real ledger.
type AuthorTrade struct {
	Base            string
	Author          string
	Ticker          string
	Expiry          time.Time
	Strike          float64
	Right           string
	OpenedAt        time.Time
	BTOPrice        float64
	RemainingFrac   float64
	TotalSoldFrac   float64
	VWAPExitPrice   float64
	STCCount        int
	Closed          bool
	AuthorPnLPerCtr float64 // (VWAPExit - BTO) * TotalSoldFrac, per contract
}

// WriteAuthorStatedLedger walks the pre-loaded signal queue, pairs BTOs with
// subsequent same-key STCs, applies partial-fraction keyword rules, and
// writes one CSV row per reconstructed position. Returns the list of trades
// so the caller can print a summary alongside the actual fill ledger.
//
// This does NOT invoke the bus or the live strategy — it's a pure read over
// the parsed queue, computing what the copytrade edge looks like in the
// author's own words, independent of any replay-simulation decisions the
// strategy made.
func (s *Service) WriteAuthorStatedLedger(path string, partials []PartialFractionEntry, defaultFrac float64) ([]AuthorTrade, error) {
	sorted := append([]PartialFractionEntry(nil), partials...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].Keyword) > len(sorted[j].Keyword)
	})

	type posKey struct {
		author, ticker, right string
		expiry                time.Time
		strike                float64
	}
	positions := make(map[posKey]*AuthorTrade)
	var order []posKey

	for _, qs := range s.queue {
		p := qs.payload
		key := posKey{
			author: strings.ToLower(strings.TrimSpace(p.Author)),
			ticker: strings.ToUpper(string(p.Ticker)),
			right:  string(p.Right),
			expiry: p.Expiry,
			strike: p.Strike,
		}

		switch p.Action {
		case domain.CopytradeActionBTO:
			if _, exists := positions[key]; exists {
				continue
			}
			positions[key] = &AuthorTrade{
				Base:          baseKey(key.author, key.ticker, p.Expiry, p.Strike, key.right),
				Author:        p.Author,
				Ticker:        string(p.Ticker),
				Expiry:        p.Expiry,
				Strike:        p.Strike,
				Right:         string(p.Right),
				OpenedAt:      p.PostedAt,
				BTOPrice:      p.Price,
				RemainingFrac: 1.0,
			}
			order = append(order, key)

		case domain.CopytradeActionSTC:
			pos, ok := positions[key]
			if !ok || pos.RemainingFrac <= 0 {
				continue
			}
			frac, _ := resolveKeywordFraction(p.Tail, sorted, defaultFrac)
			closing := pos.RemainingFrac * frac
			if closing > pos.RemainingFrac {
				closing = pos.RemainingFrac
			}
			newTotal := pos.TotalSoldFrac + closing
			if newTotal > 0 {
				pos.VWAPExitPrice = (pos.VWAPExitPrice*pos.TotalSoldFrac + p.Price*closing) / newTotal
			}
			pos.TotalSoldFrac = newTotal
			pos.RemainingFrac -= closing
			if pos.RemainingFrac < 1e-9 {
				pos.RemainingFrac = 0
				pos.Closed = true
			}
			pos.STCCount++

		case domain.CopytradeActionAVG:
			// Retrospective note — ignored by live strategy (skip_avg=true).
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("author ledger: open %s: %w", path, err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{
		"base",
		"author",
		"ticker",
		"expiry",
		"strike",
		"right",
		"opened_at",
		"bto_price",
		"vwap_exit_price",
		"total_sold_frac",
		"remaining_frac",
		"stc_count",
		"closed",
		"author_pnl_per_contract",
	}); err != nil {
		return nil, fmt.Errorf("author ledger: header: %w", err)
	}

	out := make([]AuthorTrade, 0, len(order))
	for _, key := range order {
		pos := positions[key]
		pos.AuthorPnLPerCtr = (pos.VWAPExitPrice - pos.BTOPrice) * pos.TotalSoldFrac * 100.0
		out = append(out, *pos)
		if err := w.Write([]string{
			pos.Base,
			pos.Author,
			pos.Ticker,
			pos.Expiry.Format("2006-01-02"),
			strconv.FormatFloat(pos.Strike, 'f', -1, 64),
			pos.Right,
			pos.OpenedAt.UTC().Format(time.RFC3339),
			strconv.FormatFloat(pos.BTOPrice, 'f', 4, 64),
			strconv.FormatFloat(pos.VWAPExitPrice, 'f', 4, 64),
			strconv.FormatFloat(pos.TotalSoldFrac, 'f', 4, 64),
			strconv.FormatFloat(pos.RemainingFrac, 'f', 4, 64),
			strconv.Itoa(pos.STCCount),
			strconv.FormatBool(pos.Closed),
			strconv.FormatFloat(pos.AuthorPnLPerCtr, 'f', 2, 64),
		}); err != nil {
			return nil, fmt.Errorf("author ledger: row %s: %w", pos.Base, err)
		}
	}
	return out, nil
}

// AuthorStatedSummary rolls up reconstructed positions into aggregate stats
// comparable to the backtest.Collector output: win rate, PnL per contract.
// Positions still open at end-of-run contribute only their realized portion.
type AuthorStatedSummary struct {
	Positions       int
	Closed          int
	Open            int
	PnLPerContract  float64
	Wins            int
	Losses          int
	WinRate         float64
	AvgWinPerCtr    float64
	AvgLossPerCtr   float64
	LargestWinPer   float64
	LargestLossPer  float64
}

// SummarizeAuthorStated computes aggregate stats across a set of reconstructed
// trades. PnL is expressed per contract (multiplier 100 already applied in
// AuthorPnLPerCtr); multiply by actual contract count for dollar totals.
func SummarizeAuthorStated(trades []AuthorTrade) AuthorStatedSummary {
	var s AuthorStatedSummary
	s.Positions = len(trades)
	if s.Positions == 0 {
		return s
	}
	var totalWin, totalLoss float64
	for _, t := range trades {
		if t.Closed {
			s.Closed++
		} else {
			s.Open++
		}
		s.PnLPerContract += t.AuthorPnLPerCtr
		switch {
		case t.AuthorPnLPerCtr > 0:
			s.Wins++
			totalWin += t.AuthorPnLPerCtr
			if t.AuthorPnLPerCtr > s.LargestWinPer {
				s.LargestWinPer = t.AuthorPnLPerCtr
			}
		case t.AuthorPnLPerCtr < 0:
			s.Losses++
			totalLoss += -t.AuthorPnLPerCtr
			if -t.AuthorPnLPerCtr > s.LargestLossPer {
				s.LargestLossPer = -t.AuthorPnLPerCtr
			}
		}
	}
	decided := s.Wins + s.Losses
	if decided > 0 {
		s.WinRate = float64(s.Wins) / float64(decided) * 100.0
	}
	if s.Wins > 0 {
		s.AvgWinPerCtr = totalWin / float64(s.Wins)
	}
	if s.Losses > 0 {
		s.AvgLossPerCtr = totalLoss / float64(s.Losses)
	}
	return s
}

func baseKey(author, ticker string, expiry time.Time, strike float64, right string) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s",
		author, ticker,
		expiry.Format("2006-01-02"),
		strconv.FormatFloat(strike, 'f', -1, 64),
		right)
}

func resolveKeywordFraction(tail string, sorted []PartialFractionEntry, def float64) (float64, string) {
	lowered := strings.ToLower(tail)
	for _, p := range sorted {
		if strings.Contains(lowered, p.Keyword) {
			return clampFraction(p.Fraction), p.Keyword
		}
	}
	return clampFraction(def), "default"
}

func clampFraction(f float64) float64 {
	if math.IsNaN(f) || f <= 0 {
		return 0
	}
	if f > 1.0 {
		return 1.0
	}
	return f
}
