// option-quote-shadow fetches a single-contract bid/ask snapshot from both
// IBKR and Alpaca for a given list of OCC option symbols, then prints them
// side-by-side. Used to validate that IBKR's reqMktData on option contracts
// returns sane data before wiring it into the production submit-time
// quote snapshotter.
//
// Usage:
//
//	go run ./backend/cmd/option-quote-shadow \
//	    --symbols NVDA260522C00205000,MSFT260515C00420000,AMD260522P00347500
//
// IBKR ClientID defaults to 99 to avoid colliding with omo-core (ClientID=2).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/logger"
	"github.com/rs/zerolog"
)

const defaultSymbols = "NVDA260522C00205000,MSFT260515C00420000,AMD260522P00347500"

func main() {
	var (
		symbolsFlag string
		configPath  string
		envPath     string
		clientID    int
		timeoutSec  int
		repeat      int
		repeatDelay int
	)
	flag.StringVar(&symbolsFlag, "symbols", defaultSymbols, "Comma-separated OCC option symbols")
	flag.StringVar(&configPath, "config", "configs/config.yaml", "Path to YAML config")
	flag.StringVar(&envPath, "env-file", ".env", "Path to .env file")
	flag.IntVar(&clientID, "ibkr-client-id", 99, "IBKR client ID (must not collide with running services)")
	flag.IntVar(&timeoutSec, "timeout-sec", 10, "Per-symbol per-vendor fetch timeout")
	flag.IntVar(&repeat, "repeat", 1, "Number of full sweeps to perform within one connection")
	flag.IntVar(&repeatDelay, "repeat-delay-sec", 2, "Seconds to sleep between sweeps")
	flag.Parse()

	log := logger.New(logger.Config{Level: zerolog.InfoLevel, Pretty: true}).
		With().Str("tool", "option-quote-shadow").Logger()

	cfg, err := config.Load(envPath, configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("config load failed")
	}

	var symbols []domain.Symbol
	for _, s := range strings.Split(symbolsFlag, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		symbols = append(symbols, domain.Symbol(s))
	}
	if len(symbols) == 0 {
		fmt.Fprintln(os.Stderr, "--symbols produced no entries")
		os.Exit(2)
	}

	ibkrCfg := cfg.IBKR
	ibkrCfg.ClientID = clientID
	ibAdapter, err := ibkr.NewAdapter(ibkrCfg, log.With().Str("component", "ibkr").Logger())
	if err != nil {
		log.Fatal().Err(err).Msg("ibkr connect failed")
	}
	defer ibAdapter.Close()

	alpAdapter, err := alpaca.NewAdapter(
		cfg.Alpaca,
		log.With().Str("component", "alpaca").Logger(),
		alpaca.WithNoStream(),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("alpaca init failed")
	}

	timeout := time.Duration(timeoutSec) * time.Second

	fmt.Printf("\n%-4s %-22s | %10s %10s %8s | %10s %10s %8s | %10s %12s\n",
		"swp", "symbol", "ibkr_bid", "ibkr_ask", "ib_ms", "alp_bid", "alp_ask", "alp_ms", "mid_diff", "alp_age_ms")
	fmt.Println(strings.Repeat("-", 125))

	for sweep := 1; sweep <= repeat; sweep++ {
	for _, sym := range symbols {
		var (
			ibBid, ibAsk    float64
			ibErr           error
			ibElapsedMs     int64
			alpQuote        domain.OptionQuote
			alpErr          error
			alpElapsedMs    int64
			wg              sync.WaitGroup
		)

		wg.Add(2)

		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			t0 := time.Now()
			ibBid, ibAsk, ibErr = ibAdapter.GetQuote(ctx, sym)
			ibElapsedMs = time.Since(t0).Milliseconds()
		}()

		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			t0 := time.Now()
			res, err := alpAdapter.GetOptionPrices(ctx, []domain.Symbol{sym})
			alpElapsedMs = time.Since(t0).Milliseconds()
			if err != nil {
				alpErr = err
				return
			}
			q, ok := res[sym]
			if !ok {
				alpErr = fmt.Errorf("alpaca: no quote returned for %s", sym)
				return
			}
			alpQuote = q
		}()

		wg.Wait()

		ibBidStr := fmt.Sprintf("%.4f", ibBid)
		ibAskStr := fmt.Sprintf("%.4f", ibAsk)
		if ibErr != nil {
			ibBidStr, ibAskStr = "ERR", "ERR"
		}
		alpBidStr := fmt.Sprintf("%.4f", alpQuote.Bid)
		alpAskStr := fmt.Sprintf("%.4f", alpQuote.Ask)
		if alpErr != nil {
			alpBidStr, alpAskStr = "ERR", "ERR"
		}

		midDiffStr := "-"
		ageStr := "-"
		if ibErr == nil && alpErr == nil {
			ibMid := (ibBid + ibAsk) / 2
			alpMid := (alpQuote.Bid + alpQuote.Ask) / 2
			midDiffStr = fmt.Sprintf("%+.4f", ibMid-alpMid)
			if !alpQuote.Timestamp.IsZero() {
				ageStr = fmt.Sprintf("%d", time.Since(alpQuote.Timestamp).Milliseconds())
			}
		}

		fmt.Printf("%-4d %-22s | %10s %10s %8d | %10s %10s %8d | %10s %12s\n",
			sweep, sym, ibBidStr, ibAskStr, ibElapsedMs, alpBidStr, alpAskStr, alpElapsedMs, midDiffStr, ageStr)

		if ibErr != nil {
			fmt.Printf("  IBKR error:   %v\n", ibErr)
		}
		if alpErr != nil {
			fmt.Printf("  Alpaca error: %v\n", alpErr)
		}
	}
	if sweep < repeat {
		time.Sleep(time.Duration(repeatDelay) * time.Second)
	}
	}
	fmt.Println()
}
