// option-chain-snapshot verifies that IBKR can produce a full option-chain
// snapshot for a given underlying. Pipeline:
//
//  1. Qualify the STK contract (resolves ConID + multiplier).
//  2. Snapshot the underlying for spot price.
//  3. ReqSecDefOptParams to enumerate strikes + expirations.
//  4. Pick the SMART chain, choose expiry closest to --target-dte.
//  5. Filter strikes to ATM ± --strike-window.
//  6. Parallel-snapshot each (call, put) and read bid/ask/greeks.
//
// Used to validate the live-chain workflow before wiring IBKR into the
// existing OptionsMarketDataPort.
//
// Usage:
//
//	go run ./backend/cmd/option-chain-snapshot \
//	    --underlying NVDA --target-dte 18 --strike-window 5
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
)

func main() {
	var (
		underlying    string
		targetDTE     int
		strikeWindow  int
		host           string
		port           int
		clientID       int
		concurrency    int
		alpacaCompare  bool
		configPath     string
		envPath        string
	)
	flag.StringVar(&underlying, "underlying", "NVDA", "Underlying ticker")
	flag.IntVar(&targetDTE, "target-dte", 18, "Target days-to-expiration; closest available expiry is used")
	flag.IntVar(&strikeWindow, "strike-window", 5, "Number of strikes each side of ATM to fetch")
	flag.StringVar(&host, "ibkr-host", "localhost", "IB Gateway host")
	flag.IntVar(&port, "ibkr-port", 4002, "IB Gateway port (4002 paper, 4001 live)")
	flag.IntVar(&clientID, "ibkr-client-id", 99, "IBKR client ID (must not collide with running services)")
	flag.IntVar(&concurrency, "concurrency", 22, "Max parallel snapshot goroutines")
	flag.BoolVar(&alpacaCompare, "alpaca-compare", true, "Also fetch the same chain via Alpaca and compare side-by-side")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config (only needed when --alpaca-compare)")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file (only needed when --alpaca-compare)")
	flag.Parse()

	zerolog.SetGlobalLevel(zerolog.WarnLevel)

	ib := ibsync.NewIB()
	if err := ib.Connect(ibsync.NewConfig(
		ibsync.WithHost(host),
		ibsync.WithPort(port),
		ibsync.WithClientID(int64(clientID)),
	)); err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = ib.Disconnect() }()
	fmt.Printf("connected to %s:%d (clientID=%d)\n", host, port, clientID)

	// 1. Qualify underlying STK to resolve ConID.
	stk := ibsync.NewStock(underlying, "SMART", "USD")
	t0 := time.Now()
	if err := ib.QualifyContract(stk); err != nil {
		fmt.Fprintf(os.Stderr, "qualify %s: %v\n", underlying, err)
		os.Exit(1)
	}
	fmt.Printf("qualified %s ConID=%d in %dms\n", underlying, stk.ConID, time.Since(t0).Milliseconds())

	// 2. Spot price.
	t0 = time.Now()
	spotTicker, err := ib.Snapshot(stk)
	if err != nil && !errors.Is(err, ibsync.WarnDelayedMarketData) {
		fmt.Fprintf(os.Stderr, "spot snapshot: %v\n", err)
		os.Exit(1)
	}
	spot := spotTicker.MarketPrice()
	if math.IsNaN(spot) || spot == 0 {
		spot = (spotTicker.Bid() + spotTicker.Ask()) / 2
	}
	fmt.Printf("spot %s=%.4f in %dms\n", underlying, spot, time.Since(t0).Milliseconds())
	if spot == 0 || math.IsNaN(spot) {
		fmt.Fprintln(os.Stderr, "spot price unavailable — cannot pick ATM")
		os.Exit(1)
	}

	// 3. Enumerate strikes + expirations.
	t0 = time.Now()
	chains, err := ib.ReqSecDefOptParams(stk.Symbol, "", "STK", stk.ConID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ReqSecDefOptParams: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ReqSecDefOptParams returned %d chains in %dms\n", len(chains), time.Since(t0).Milliseconds())
	if len(chains) == 0 {
		fmt.Fprintln(os.Stderr, "no chains returned")
		os.Exit(1)
	}

	// 4. Pick SMART chain (prefer TradingClass=underlying).
	var smart *ibsync.OptionChain
	for i := range chains {
		c := &chains[i]
		if c.Exchange == "SMART" {
			if smart == nil || c.TradingClass == underlying {
				smart = c
			}
		}
	}
	if smart == nil {
		fmt.Fprintln(os.Stderr, "no SMART chain found")
		for i, c := range chains {
			fmt.Fprintf(os.Stderr, "  chain[%d]: exch=%s trading_class=%s mult=%s exps=%d strikes=%d\n",
				i, c.Exchange, c.TradingClass, c.Multiplier, len(c.Expirations), len(c.Strikes))
		}
		os.Exit(1)
	}
	fmt.Printf("SMART chain: trading_class=%s multiplier=%s expirations=%d strikes=%d\n",
		smart.TradingClass, smart.Multiplier, len(smart.Expirations), len(smart.Strikes))

	// 5. Pick expiry closest to target DTE.
	now := time.Now().UTC()
	target := now.AddDate(0, 0, targetDTE)
	var bestExpiry string
	var bestExpiryDate time.Time
	var bestDelta = math.MaxFloat64
	for _, exp := range smart.Expirations {
		d, err := time.Parse("20060102", exp)
		if err != nil {
			continue
		}
		delta := math.Abs(d.Sub(target).Hours() / 24)
		if delta < bestDelta {
			bestDelta = delta
			bestExpiry = exp
			bestExpiryDate = d
		}
	}
	if bestExpiry == "" {
		fmt.Fprintln(os.Stderr, "no parseable expirations")
		os.Exit(1)
	}
	dte := int(math.Round(bestExpiryDate.Sub(now).Hours() / 24))
	fmt.Printf("picked expiry=%s (DTE=%d, target=%d)\n", bestExpiry, dte, targetDTE)

	// 6. Pick strikes near ATM. Note: ReqSecDefOptParams returns the strike
	// UNION across all expirations, so a small fraction of strikes won't
	// resolve for our specific expiry and will surface as errCode=200 in the
	// snapshot pass. Bare-OPT ReqContractDetails enumeration would let us
	// pre-filter, but it errors out on this paper account, so we accept the
	// post-hoc bookkeeping miss instead.
	strikes := append([]float64(nil), smart.Strikes...)
	sort.Float64s(strikes)
	atmIdx := sort.SearchFloat64s(strikes, spot)
	lo := atmIdx - strikeWindow
	hi := atmIdx + strikeWindow + 1
	if lo < 0 {
		lo = 0
	}
	if hi > len(strikes) {
		hi = len(strikes)
	}
	pickedStrikes := strikes[lo:hi]
	fmt.Printf("picked %d strikes around ATM=%.4f: %.2f .. %.2f\n",
		len(pickedStrikes), spot, pickedStrikes[0], pickedStrikes[len(pickedStrikes)-1])

	// 7. Build contracts (call + put per strike) and parallel-snapshot.
	cells := make([]cell, 0, len(pickedStrikes)*2)
	for _, k := range pickedStrikes {
		cells = append(cells, cell{strike: k, right: "C"}, cell{strike: k, right: "P"})
	}

	mult := smart.Multiplier
	if mult == "" {
		mult = "100"
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	tStart := time.Now()
	for i := range cells {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			c := &cells[i]
			opt := ibsync.NewOption(underlying, bestExpiry, c.strike, c.right, "SMART", mult, "USD")
			opt.TradingClass = smart.TradingClass
			t0 := time.Now()
			tk, err := ib.Snapshot(opt)
			c.elapsed = time.Since(t0).Milliseconds()
			if err != nil && !errors.Is(err, ibsync.WarnDelayedMarketData) {
				c.err = err
				return
			}
			c.bid = tk.Bid()
			c.ask = tk.Ask()
			g := tk.ModelGreeks()
			if g.ImpliedVol == 0 {
				g = tk.Greeks()
			}
			c.iv = g.ImpliedVol
			c.delta = g.Delta
		}(i)
	}
	wg.Wait()
	totalMs := time.Since(tStart).Milliseconds()

	// 8. Print results.
	fmt.Printf("\nchain snapshot: %d contracts in %dms (concurrency=%d)\n\n",
		len(cells), totalMs, concurrency)

	calls := map[float64]*cell{}
	puts := map[float64]*cell{}
	for i := range cells {
		c := &cells[i]
		if c.right == "C" {
			calls[c.strike] = c
		} else {
			puts[c.strike] = c
		}
	}

	fmt.Printf("%-8s | %8s %8s %8s %8s %6s | %8s %8s %8s %8s %6s\n",
		"strike", "C_bid", "C_ask", "C_iv", "C_delta", "C_ms", "P_bid", "P_ask", "P_iv", "P_delta", "P_ms")
	fmt.Println("---------+------------------------------------------------+------------------------------------------------")
	var okCount, errCount int
	var sumMs, maxMs int64
	for _, k := range pickedStrikes {
		c := calls[k]
		p := puts[k]
		cb, ca, civ, cd, cms := fmtCell(c)
		pb, pa, piv, pd, pms := fmtCell(p)
		fmt.Printf("%-8.2f | %8s %8s %8s %8s %6s | %8s %8s %8s %8s %6s\n",
			k, cb, ca, civ, cd, cms, pb, pa, piv, pd, pms)
		for _, x := range []*cell{c, p} {
			if x == nil {
				continue
			}
			if x.err != nil {
				errCount++
			} else {
				okCount++
			}
			sumMs += x.elapsed
			if x.elapsed > maxMs {
				maxMs = x.elapsed
			}
		}
	}

	fmt.Println()
	fmt.Printf("results: ok=%d err=%d\n", okCount, errCount)
	if okCount > 0 {
		fmt.Printf("per-snapshot latency: avg=%dms max=%dms (sum=%dms across %d goroutines, wall=%dms)\n",
			sumMs/int64(okCount+errCount), maxMs, sumMs, len(cells), totalMs)
	}
	for i := range cells {
		c := &cells[i]
		if c.err != nil {
			fmt.Printf("  ERR strike=%.2f right=%s: %v\n", c.strike, c.right, c.err)
		}
	}

	if !alpacaCompare {
		return
	}
	runAlpacaCompare(configPath, envPath, underlying, bestExpiryDate, pickedStrikes, calls, puts)
}

// runAlpacaCompare fetches the same target-expiry chain from Alpaca and
// prints a per-strike side-by-side diff vs the IBKR cells.
func runAlpacaCompare(
	configPath, envPath string,
	underlying string,
	expiry time.Time,
	pickedStrikes []float64,
	ibCalls, ibPuts map[float64]*cell,
) {
	fmt.Println()
	fmt.Println("=== Alpaca comparison ===")

	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "alpaca compare: config load: %v\n", err)
		return
	}
	alpAdapter, err := alpaca.NewAdapter(cfg.Alpaca, zerolog.Nop(), alpaca.WithNoStream())
	if err != nil {
		fmt.Fprintf(os.Stderr, "alpaca compare: adapter init: %v\n", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tStart := time.Now()
	callChain, errC := alpAdapter.GetOptionChain(ctx, domain.Symbol(underlying), expiry, domain.OptionRightCall, 0, 0)
	callMs := time.Since(tStart).Milliseconds()
	tStart = time.Now()
	putChain, errP := alpAdapter.GetOptionChain(ctx, domain.Symbol(underlying), expiry, domain.OptionRightPut, 0, 0)
	putMs := time.Since(tStart).Milliseconds()

	if errC != nil {
		fmt.Fprintf(os.Stderr, "alpaca call chain: %v\n", errC)
	}
	if errP != nil {
		fmt.Fprintf(os.Stderr, "alpaca put chain: %v\n", errP)
	}

	fmt.Printf("alpaca: calls=%d (in %dms) puts=%d (in %dms) for %s ±7d window\n",
		len(callChain), callMs, len(putChain), putMs, expiry.Format("2006-01-02"))

	// Filter Alpaca chain to the exact target expiry (its window is ±7d).
	expiryDay := expiry.Format("2006-01-02")
	alpCallByStrike := map[float64]domain.OptionContractSnapshot{}
	for _, s := range callChain {
		if s.Expiry.Format("2006-01-02") == expiryDay {
			alpCallByStrike[s.Strike] = s
		}
	}
	alpPutByStrike := map[float64]domain.OptionContractSnapshot{}
	for _, s := range putChain {
		if s.Expiry.Format("2006-01-02") == expiryDay {
			alpPutByStrike[s.Strike] = s
		}
	}
	fmt.Printf("alpaca: %d call strikes, %d put strikes for exact expiry %s\n",
		len(alpCallByStrike), len(alpPutByStrike), expiryDay)

	// Side-by-side per-strike diff. We show: strike | C(ibkr_mid, alp_mid, mid_diff, iv_diff, delta_diff) | P(...)
	fmt.Println()
	fmt.Printf("%-8s | %9s %9s %8s %8s %8s | %9s %9s %8s %8s %8s\n",
		"strike", "C_ib_mid", "C_alp_mid", "C_dMid", "C_dIV", "C_dDelta",
		"P_ib_mid", "P_alp_mid", "P_dMid", "P_dIV", "P_dDelta")
	fmt.Println("---------+--------------------------------------------------+--------------------------------------------------")

	var (
		nMatch, nIBOnly, nAlpOnly int
		sumAbsMidDiff             float64
		sumAbsIVDiff              float64
	)

	for _, k := range pickedStrikes {
		ibC := ibCalls[k]
		ibP := ibPuts[k]
		alpC, hasAlpC := alpCallByStrike[k]
		alpP, hasAlpP := alpPutByStrike[k]

		ibCMid, ibCMidOK := midOK(ibC)
		ibPMid, ibPMidOK := midOK(ibP)
		alpCMid := (alpC.Bid + alpC.Ask) / 2
		alpPMid := (alpP.Bid + alpP.Ask) / 2

		cIbMidStr := fmtMaybe(ibCMid, ibCMidOK)
		cAlpMidStr := fmtMaybe(alpCMid, hasAlpC)
		cMidDiff := diffStr(ibCMid, alpCMid, ibCMidOK && hasAlpC)
		cIVDiff := diffStr(safeIV(ibC), alpC.IV, ibCMidOK && hasAlpC)
		cDDiff := diffStr(safeDelta(ibC), alpC.Delta, ibCMidOK && hasAlpC)

		pIbMidStr := fmtMaybe(ibPMid, ibPMidOK)
		pAlpMidStr := fmtMaybe(alpPMid, hasAlpP)
		pMidDiff := diffStr(ibPMid, alpPMid, ibPMidOK && hasAlpP)
		pIVDiff := diffStr(safeIV(ibP), alpP.IV, ibPMidOK && hasAlpP)
		pDDiff := diffStr(safeDelta(ibP), alpP.Delta, ibPMidOK && hasAlpP)

		fmt.Printf("%-8.2f | %9s %9s %8s %8s %8s | %9s %9s %8s %8s %8s\n",
			k, cIbMidStr, cAlpMidStr, cMidDiff, cIVDiff, cDDiff,
			pIbMidStr, pAlpMidStr, pMidDiff, pIVDiff, pDDiff)

		for _, pair := range []struct {
			ibOK, alpOK bool
			ibMid, alpM float64
			ibIV, alpIV float64
		}{
			{ibCMidOK, hasAlpC, ibCMid, alpCMid, safeIV(ibC), alpC.IV},
			{ibPMidOK, hasAlpP, ibPMid, alpPMid, safeIV(ibP), alpP.IV},
		} {
			switch {
			case pair.ibOK && pair.alpOK:
				nMatch++
				sumAbsMidDiff += math.Abs(pair.ibMid - pair.alpM)
				sumAbsIVDiff += math.Abs(pair.ibIV - pair.alpIV)
			case pair.ibOK:
				nIBOnly++
			case pair.alpOK:
				nAlpOnly++
			}
		}
	}

	fmt.Println()
	fmt.Printf("compare summary: matched=%d ib_only=%d alp_only=%d\n", nMatch, nIBOnly, nAlpOnly)
	if nMatch > 0 {
		fmt.Printf("  abs mid diff: avg=%.4f\n", sumAbsMidDiff/float64(nMatch))
		fmt.Printf("  abs iv  diff: avg=%.4f\n", sumAbsIVDiff/float64(nMatch))
	}
}

func midOK(c *cell) (float64, bool) {
	if c == nil || c.err != nil || (c.bid == 0 && c.ask == 0) {
		return 0, false
	}
	return (c.bid + c.ask) / 2, true
}

func safeIV(c *cell) float64 {
	if c == nil {
		return 0
	}
	return c.iv
}

func safeDelta(c *cell) float64 {
	if c == nil {
		return 0
	}
	return c.delta
}

func fmtMaybe(v float64, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.4f", v)
}

func diffStr(a, b float64, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%+.4f", a-b)
}

func fmtCell(c *cell) (bid, ask, iv, delta, ms string) {
	if c == nil {
		return "-", "-", "-", "-", "-"
	}
	if c.err != nil {
		return "ERR", "ERR", "ERR", "ERR", strconv.FormatInt(c.elapsed, 10)
	}
	return fmt.Sprintf("%.4f", c.bid),
		fmt.Sprintf("%.4f", c.ask),
		fmt.Sprintf("%.4f", c.iv),
		fmt.Sprintf("%+.3f", c.delta),
		strconv.FormatInt(c.elapsed, 10)
}

type cell struct {
	strike   float64
	right    string
	bid, ask float64
	iv       float64
	delta    float64
	elapsed  int64
	err      error
}
