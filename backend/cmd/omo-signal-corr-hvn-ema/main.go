// omo-signal-corr-hvn-ema: read-only harness that grades the HVN/EMA
// diagnostic tags shipped on avwap_v4_equity (PR #91) and avwap_v4
// options (this plan's Track C). Consumes a backtest result JSON, pairs
// entries to exits to attach realized PnL, buckets by HVN/EMA tag values,
// and reports per-bucket profit-factor lift on a time-OOS split AND a
// symbol-OOS split. Decision rule (locked in plan section 5.5): PF lift
// >= 0.10 absolute on BOTH holdouts AND directional agreement -> PASS;
// either fails or signs disagree -> FAIL.
//
// No engine changes, no DB writes, no signal rebuild. Forked-but-much-
// smaller from omo-signal-corr (which serves the inducement / cofire /
// MACD harness on a different I/O contract).
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// trade is one fill row from a backtest result JSON's "trades" array.
// Direction values seen: "LONG", "SHORT" (entry rows), "CLOSE_LONG",
// "CLOSE_SHORT" (exit rows).
type trade struct {
	Symbol    string            `json:"symbol"`
	Side      string            `json:"side"`
	Direction string            `json:"direction"`
	Quantity  float64           `json:"quantity"`
	Price     float64           `json:"price"`
	FilledAt  time.Time         `json:"filled_at"`
	Strategy  string            `json:"strategy"`
	PnL       *float64          `json:"pnl,omitempty"`
	Tags      map[string]string `json:"tags"`
}

type tradeLog struct {
	Trades []trade `json:"trades"`
}

// sample is one round-trip: entry tags + realized exit PnL.
type sample struct {
	Symbol    string
	Direction string // LONG or SHORT (the entry direction; NOT the CLOSE_*)
	EntryAt   time.Time
	PnL       float64
	Tags      map[string]string
}

// bucket holds aggregate stats for one classification cell.
type bucket struct {
	Name     string
	N        int
	Wins     int
	Losses   int
	GrossWin float64
	GrossLos float64 // accumulated as positive number
	TotalPnL float64
}

func (b *bucket) add(pnl float64) {
	b.N++
	b.TotalPnL += pnl
	if pnl > 0 {
		b.Wins++
		b.GrossWin += pnl
	} else if pnl < 0 {
		b.Losses++
		b.GrossLos += -pnl
	}
}

func (b *bucket) winRate() float64 {
	if b.N == 0 {
		return 0
	}
	return float64(b.Wins) / float64(b.N)
}

func (b *bucket) pf() float64 {
	if b.GrossLos == 0 {
		if b.GrossWin == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return b.GrossWin / b.GrossLos
}

func (b *bucket) avgPnL() float64 {
	if b.N == 0 {
		return 0
	}
	return b.TotalPnL / float64(b.N)
}

func main() {
	var (
		tradeLogArg     = flag.String("trade-log", "", "Path(s) to trade-log JSON or directory (comma-separated)")
		strategyArg     = flag.String("strategy", "avwap_v4_equity", "Strategy id to filter on")
		outArg          = flag.String("out", "", "Output dir (default _tmp/signal_corr_hvn_ema/<timestamp>)")
		holdoutDaysArg  = flag.Int("time-holdout-days", 30, "OOS window (days) before max-trade-date")
		symHoldoutArg   = flag.Float64("symbol-holdout-fraction", 0.30, "Fraction of symbols (smallest by N) for symbol-OOS")
		minSymTradesArg = flag.Int("symbol-holdout-min-trades", 5, "Drop a held-out symbol with fewer than this many entries")
		minNNearArg     = flag.Int("min-n-near", 50, "Minimum n_near per holdout for the decision rule to evaluate; below this, return INSUFFICIENT_DATA")
	)
	flag.Parse()

	if *tradeLogArg == "" {
		fmt.Fprintln(os.Stderr, "usage: omo-signal-corr-hvn-ema --trade-log <path-or-dir>[,...] [--strategy ID] [--out DIR]")
		os.Exit(2)
	}

	outDir := *outArg
	if outDir == "" {
		outDir = filepath.Join("_tmp", "signal_corr_hvn_ema", time.Now().UTC().Format("20060102_150405"))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail("mkdir %s: %v", outDir, err)
	}

	paths, err := expandTradeLogPaths(*tradeLogArg)
	if err != nil {
		fail("expand trade-log paths: %v", err)
	}
	if len(paths) == 0 {
		fail("no trade-log files found under %s", *tradeLogArg)
	}

	var allTrades []trade
	for _, p := range paths {
		ts, err := loadTrades(p)
		if err != nil {
			fail("load %s: %v", p, err)
		}
		allTrades = append(allTrades, ts...)
	}
	fmt.Fprintf(os.Stderr, "loaded %d trade rows from %d file(s)\n", len(allTrades), len(paths))

	samples := pairTrades(allTrades, *strategyArg)
	fmt.Fprintf(os.Stderr, "%d round-trip samples for strategy=%s\n", len(samples), *strategyArg)

	if len(samples) == 0 {
		if err := writeStringFile(filepath.Join(outDir, "summary.txt"),
			fmt.Sprintf("# omo-signal-corr-hvn-ema\n\nNo qualifying entries for strategy=%q across the supplied trade logs.\n",
				*strategyArg)); err != nil {
			fail("write empty summary: %v", err)
		}
		fmt.Fprintln(os.Stderr, "no samples to grade; wrote empty summary and exiting")
		return
	}

	// Time-OOS split: trades on/after (max - holdoutDays) are OOS.
	maxTime := samples[0].EntryAt
	for _, s := range samples {
		if s.EntryAt.After(maxTime) {
			maxTime = s.EntryAt
		}
	}
	cutoff := maxTime.Add(-time.Duration(*holdoutDaysArg) * 24 * time.Hour)
	timeIS, timeOOS := splitByTime(samples, cutoff)

	// Symbol-OOS split: pick the smallest-N symbols up to the requested
	// fraction, requiring at least minSymTrades entries each.
	symOOSet := pickSymbolHoldout(samples, *symHoldoutArg, *minSymTradesArg)
	symIS, symOOS := splitBySymbolSet(samples, symOOSet)

	// Per-trade.csv first so the auditor can re-bucket without re-running.
	if err := writePerTradeCSV(filepath.Join(outDir, "per_trade.csv"), samples, cutoff, symOOSet); err != nil {
		fail("write per_trade.csv: %v", err)
	}

	// summary.txt: per-bucket stats across full / time-OOS / symbol-OOS.
	summary := renderSummary(samples, timeIS, timeOOS, symIS, symOOS, cutoff, symOOSet, *strategyArg)
	if err := writeStringFile(filepath.Join(outDir, "summary.txt"), summary); err != nil {
		fail("write summary.txt: %v", err)
	}

	// pf_lift.txt: decision-rule view.
	lift := renderPFLift(timeOOS, symOOS, *minNNearArg)
	if err := writeStringFile(filepath.Join(outDir, "pf_lift.txt"), lift); err != nil {
		fail("write pf_lift.txt: %v", err)
	}

	fmt.Fprintf(os.Stderr, "wrote %s\n", outDir)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func expandTradeLogPaths(spec string) ([]string, error) {
	var out []string
	for _, raw := range strings.Split(spec, ",") {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
					out = append(out, filepath.Join(p, e.Name()))
				}
			}
		} else {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func loadTrades(path string) ([]trade, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tl tradeLog
	if err := json.Unmarshal(data, &tl); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return tl.Trades, nil
}

// pairTrades walks all trades chronologically per symbol and pairs each
// entry (direction LONG|SHORT) with the next exit (direction CLOSE_LONG|
// CLOSE_SHORT) on the same symbol, emitting one sample per round-trip.
// PnL comes from the exit row's "pnl" field (the realized round-trip
// PnL written by the backtest ledger).
func pairTrades(all []trade, strategy string) []sample {
	bySym := map[string][]trade{}
	for _, t := range all {
		if t.Strategy != strategy {
			continue
		}
		bySym[t.Symbol] = append(bySym[t.Symbol], t)
	}
	var out []sample
	for sym, ts := range bySym {
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].FilledAt.Before(ts[j].FilledAt) })
		var openEntry *trade
		for i := range ts {
			t := ts[i]
			switch t.Direction {
			case "LONG", "SHORT":
				openEntry = &ts[i]
			case "CLOSE_LONG", "CLOSE_SHORT":
				if openEntry == nil || t.PnL == nil {
					openEntry = nil
					continue
				}
				out = append(out, sample{
					Symbol:    sym,
					Direction: openEntry.Direction,
					EntryAt:   openEntry.FilledAt,
					PnL:       *t.PnL,
					Tags:      openEntry.Tags,
				})
				openEntry = nil
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EntryAt.Before(out[j].EntryAt) })
	return out
}

func splitByTime(samples []sample, cutoff time.Time) (is, oos []sample) {
	for _, s := range samples {
		if s.EntryAt.Before(cutoff) {
			is = append(is, s)
		} else {
			oos = append(oos, s)
		}
	}
	return
}

func pickSymbolHoldout(samples []sample, frac float64, minTrades int) map[string]bool {
	count := map[string]int{}
	for _, s := range samples {
		count[s.Symbol]++
	}
	type sc struct {
		sym string
		n   int
	}
	var pool []sc
	for k, v := range count {
		if v >= minTrades {
			pool = append(pool, sc{k, v})
		}
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].n != pool[j].n {
			return pool[i].n < pool[j].n
		}
		return pool[i].sym < pool[j].sym
	})
	target := int(math.Round(frac * float64(len(count))))
	if target > len(pool) {
		target = len(pool)
	}
	out := map[string]bool{}
	for i := 0; i < target; i++ {
		out[pool[i].sym] = true
	}
	return out
}

func splitBySymbolSet(samples []sample, oos map[string]bool) (is, oosOut []sample) {
	for _, s := range samples {
		if oos[s.Symbol] {
			oosOut = append(oosOut, s)
		} else {
			is = append(is, s)
		}
	}
	return
}

// hvnNearFar splits samples by the locked plan threshold hvn_dist_atr <= 0.5
// (near) vs > 0.5 (far). Samples with no hvn_dist_atr tag fall into "far"
// (conservative — entries pre-PR-91 won't grade as near-HVN).
func hvnNearFar(samples []sample) (near, far *bucket) {
	near, far = &bucket{Name: "hvn_dist_atr<=0.5"}, &bucket{Name: "hvn_dist_atr>0.5"}
	for _, s := range samples {
		v, ok := parseFloatTag(s.Tags, "hvn_dist_atr")
		if ok && v <= 0.5 {
			near.add(s.PnL)
		} else {
			far.add(s.PnL)
		}
	}
	return
}

// emaWithinOutside splits by |ema_dist_atr| <= 1.0 vs > 1.0. Samples with
// no ema_dist_atr tag fall into "outside".
func emaWithinOutside(samples []sample) (within, outside *bucket) {
	within, outside = &bucket{Name: "|ema_dist_atr|<=1.0"}, &bucket{Name: "|ema_dist_atr|>1.0"}
	for _, s := range samples {
		v, ok := parseFloatTag(s.Tags, "ema_dist_atr")
		if ok && math.Abs(v) <= 1.0 {
			within.add(s.PnL)
		} else {
			outside.add(s.PnL)
		}
	}
	return
}

func parseFloatTag(tags map[string]string, key string) (float64, bool) {
	if tags == nil {
		return 0, false
	}
	raw, ok := tags[key]
	if !ok || raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func renderSummary(all, timeIS, timeOOS, symIS, symOOS []sample, cutoff time.Time, symOOSet map[string]bool, strategy string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HVN/EMA tag harness summary -- strategy=%s\n\n", strategy)
	fmt.Fprintf(&b, "Samples: %d total\n", len(all))
	fmt.Fprintf(&b, "Time split: cutoff=%s -> IS=%d, OOS=%d\n", cutoff.Format(time.RFC3339), len(timeIS), len(timeOOS))
	fmt.Fprintf(&b, "Symbol holdout (%d symbols): %s -> IS=%d, OOS=%d\n\n",
		len(symOOSet), formatSymbolSet(symOOSet), len(symIS), len(symOOS))

	fmt.Fprintln(&b, "## HVN bucket: hvn_dist_atr near (<= 0.5 ATR) vs far")
	for _, scope := range []struct {
		name string
		set  []sample
	}{{"ALL", all}, {"TIME_OOS", timeOOS}, {"SYMBOL_OOS", symOOS}} {
		near, far := hvnNearFar(scope.set)
		writeBucketRows(&b, scope.name, near, far)
	}

	fmt.Fprintln(&b, "\n## EMA bucket: |ema_dist_atr| within 1.0 ATR vs outside")
	for _, scope := range []struct {
		name string
		set  []sample
	}{{"ALL", all}, {"TIME_OOS", timeOOS}, {"SYMBOL_OOS", symOOS}} {
		within, outside := emaWithinOutside(scope.set)
		writeBucketRows(&b, scope.name, within, outside)
	}
	return b.String()
}

func writeBucketRows(b *strings.Builder, scope string, buckets ...*bucket) {
	for _, bk := range buckets {
		fmt.Fprintf(b, "  [%s] %-20s n=%4d wr=%5.1f%% pf=%6s avg=%8.2f total=%9.2f\n",
			scope, bk.Name, bk.N, bk.winRate()*100, formatPF(bk.pf()), bk.avgPnL(), bk.TotalPnL)
	}
}

func formatPF(pf float64) string {
	if math.IsInf(pf, 1) {
		return "inf"
	}
	return fmt.Sprintf("%.3f", pf)
}

func formatSymbolSet(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// renderPFLift produces the decision-rule view: per factor, lift on
// time-OOS and symbol-OOS independently, PASS only if both lift >=
// 0.10 absolute AND the lift signs agree.
func renderPFLift(timeOOS, symOOS []sample, minNNear int) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Decision-rule view (HVN, EMA)")
	fmt.Fprintf(&b, "Rule: PF lift = full_PF - near/within_PF; PASS iff |lift| >= 0.10 on BOTH holdouts AND signs agree AND n_near >= %d on BOTH holdouts.\n", minNNear)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## HVN: full_PF - near_HVN_PF (where near := hvn_dist_atr <= 0.5)")
	hvnTime := liftRow(timeOOS, hvnNearFar)
	hvnSym := liftRow(symOOS, hvnNearFar)
	verdict := decide(hvnTime, hvnSym, minNNear)
	fmt.Fprintf(&b, "  TIME_OOS:   full=%s near=%s lift=%+0.3f n_near=%d n_far=%d\n",
		formatPF(hvnTime.fullPF), formatPF(hvnTime.nearPF), hvnTime.lift, hvnTime.nNear, hvnTime.nFar)
	fmt.Fprintf(&b, "  SYMBOL_OOS: full=%s near=%s lift=%+0.3f n_near=%d n_far=%d\n",
		formatPF(hvnSym.fullPF), formatPF(hvnSym.nearPF), hvnSym.lift, hvnSym.nNear, hvnSym.nFar)
	fmt.Fprintf(&b, "  VERDICT: %s\n\n", verdict)

	fmt.Fprintln(&b, "## EMA: full_PF - within_EMA_PF (where within := |ema_dist_atr| <= 1.0)")
	emaTime := liftRow(timeOOS, emaWithinOutside)
	emaSym := liftRow(symOOS, emaWithinOutside)
	verdictEMA := decide(emaTime, emaSym, minNNear)
	fmt.Fprintf(&b, "  TIME_OOS:   full=%s near=%s lift=%+0.3f n_near=%d n_far=%d\n",
		formatPF(emaTime.fullPF), formatPF(emaTime.nearPF), emaTime.lift, emaTime.nNear, emaTime.nFar)
	fmt.Fprintf(&b, "  SYMBOL_OOS: full=%s near=%s lift=%+0.3f n_near=%d n_far=%d\n",
		formatPF(emaSym.fullPF), formatPF(emaSym.nearPF), emaSym.lift, emaSym.nNear, emaSym.nFar)
	fmt.Fprintf(&b, "  VERDICT: %s\n", verdictEMA)
	return b.String()
}

type liftStat struct {
	fullPF float64
	nearPF float64 // PF for the "near" / "within" bucket
	lift   float64
	nNear  int
	nFar   int
}

func liftRow(samples []sample, splitter func([]sample) (*bucket, *bucket)) liftStat {
	near, far := splitter(samples)
	full := &bucket{}
	full.N = near.N + far.N
	full.Wins = near.Wins + far.Wins
	full.Losses = near.Losses + far.Losses
	full.GrossWin = near.GrossWin + far.GrossWin
	full.GrossLos = near.GrossLos + far.GrossLos
	full.TotalPnL = near.TotalPnL + far.TotalPnL
	return liftStat{
		fullPF: full.pf(),
		nearPF: near.pf(),
		lift:   full.pf() - near.pf(),
		nNear:  near.N,
		nFar:   far.N,
	}
}

func decide(timeOOS, symOOS liftStat, minNNear int) string {
	const thresh = 0.10
	if timeOOS.nNear < minNNear || symOOS.nNear < minNNear {
		return fmt.Sprintf("INSUFFICIENT_DATA (n_near < %d min-n-near floor: time=%d sym=%d)",
			minNNear, timeOOS.nNear, symOOS.nNear)
	}
	if math.IsInf(timeOOS.lift, 0) || math.IsInf(symOOS.lift, 0) {
		return "INSUFFICIENT_DATA (degenerate PF; need more samples)"
	}
	if math.Abs(timeOOS.lift) < thresh || math.Abs(symOOS.lift) < thresh {
		return "FAIL (lift below 0.10 absolute on at least one holdout)"
	}
	if (timeOOS.lift > 0) != (symOOS.lift > 0) {
		return "FAIL (lift signs disagree across holdouts)"
	}
	return "PASS (draft active-promotion plan; locked decision rule satisfied)"
}

func writePerTradeCSV(path string, samples []sample, cutoff time.Time, symOOSet map[string]bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	header := []string{
		"entry_at", "symbol", "direction", "pnl",
		"hvn_dist_atr", "hvn_density_above", "hvn_density_below", "poc_dist_bps",
		"ema_value", "ema_dist_bps", "ema_dist_atr",
		"ema_low_below_ema", "ema_high_above_ema",
		"bars_since_avwap_cross_session_open", "bars_since_avwap_cross_pd_high", "bars_since_avwap_cross_pd_low",
		"avwap_cross_breach_max_atr_session_open", "avwap_cross_breach_max_atr_pd_high", "avwap_cross_breach_max_atr_pd_low",
		"hvn_bucket", "ema_bucket", "time_split", "symbol_split",
	}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, s := range samples {
		hvnBucket := "far"
		if v, ok := parseFloatTag(s.Tags, "hvn_dist_atr"); ok && v <= 0.5 {
			hvnBucket = "near"
		}
		emaBucket := "outside"
		if v, ok := parseFloatTag(s.Tags, "ema_dist_atr"); ok && math.Abs(v) <= 1.0 {
			emaBucket = "within"
		}
		timeSplit := "IS"
		if !s.EntryAt.Before(cutoff) {
			timeSplit = "OOS"
		}
		symSplit := "IS"
		if symOOSet[s.Symbol] {
			symSplit = "OOS"
		}
		row := []string{
			s.EntryAt.Format(time.RFC3339), s.Symbol, s.Direction, fmt.Sprintf("%.4f", s.PnL),
			s.Tags["hvn_dist_atr"], s.Tags["hvn_density_above"], s.Tags["hvn_density_below"], s.Tags["poc_dist_bps"],
			s.Tags["ema_value"], s.Tags["ema_dist_bps"], s.Tags["ema_dist_atr"],
			s.Tags["ema_low_below_ema"], s.Tags["ema_high_above_ema"],
			s.Tags["bars_since_avwap_cross_session_open"], s.Tags["bars_since_avwap_cross_pd_high"], s.Tags["bars_since_avwap_cross_pd_low"],
			s.Tags["avwap_cross_breach_max_atr_session_open"], s.Tags["avwap_cross_breach_max_atr_pd_high"], s.Tags["avwap_cross_breach_max_atr_pd_low"],
			hvnBucket, emaBucket, timeSplit, symSplit,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func writeStringFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
