package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

type symbolScore struct {
	Symbol   string
	Price    float64
	ATR      float64
	ATRPct   float64
	NR7      bool
	Bias     string
	EMA200   float64
	RealVol  float64
	Score    float64 // composite score
}

func main() {
	// Default symbols from ORB config + some extras
	symbols := []string{
		"AAPL", "MSFT", "GOOGL", "AMZN", "TSLA", "SOXL", "U", "PLTR", "SPY", "META",
		"HIMS", "SOFI", "NFLX", "QQQ", "BAC", "AMD", "NVDA",
	}

	if len(os.Args) > 1 {
		symbols = strings.Split(os.Args[1], ",")
	}

	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	cfg, err := config.Load(".env", "configs/config.yaml")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Create Alpaca client for market data
	client := alpaca.NewRESTClient(cfg.Alpaca.DataURL, cfg.Alpaca.APIKeyID, cfg.Alpaca.APISecretKey, nil, log)
	ctx := context.Background()
	now := time.Now()

	fmt.Println("=" + strings.Repeat("=", 99))
	fmt.Println("ORB SCREENER — Symbol Analysis")
	fmt.Printf("Date: %s\n", now.Format("2006-01-02 15:04 MST"))
	fmt.Println(strings.Repeat("=", 100))

	var scores []symbolScore

	for _, sym := range symbols {
		s, _ := domain.NewSymbol(sym)
		from := now.Add(-400 * 24 * time.Hour) // enough for EMA200

		bars, fetchErr := client.GetHistoricalBars(ctx, cfg.Alpaca.DataURL, s, "1d", from, now)
		if fetchErr != nil || len(bars) < 21 {
			log.Warn().Str("symbol", sym).Err(fetchErr).Int("bars", len(bars)).Msg("insufficient data")
			continue
		}

		lastClose := bars[len(bars)-1].Close

		// Daily ATR
		atr := monitor.ComputeDailyATR(bars, 14)
		atrPct := 0.0
		if lastClose > 0 {
			atrPct = atr / lastClose * 100
		}

		// NR7
		nr7 := monitor.ComputeNR7(bars)

		// EMA200 + Bias
		closes := make([]float64, len(bars))
		for i, b := range bars {
			closes[i] = b.Close
		}
		ema200 := monitor.ComputeStaticEMA(closes, 200)
		bias := "NEUTRAL"
		if ema200 > 0 {
			if lastClose > ema200*1.005 {
				bias = "BULLISH"
			} else if lastClose < ema200*0.995 {
				bias = "BEARISH"
			}
		}

		// Realized vol (20-day)
		realVol := monitor.ComputeRealizedVol(bars, 20)

		// Composite score: higher ATR% + NR7 bonus + trend alignment
		score := atrPct * 10 // base: ATR% weighted
		if nr7 {
			score += 20 // compression bonus
		}
		if bias == "BULLISH" || bias == "BEARISH" {
			score += 5 // trending bonus (either direction)
		}

		scores = append(scores, symbolScore{
			Symbol:  sym,
			Price:   lastClose,
			ATR:     atr,
			ATRPct:  atrPct,
			NR7:     nr7,
			Bias:    bias,
			EMA200:  ema200,
			RealVol: realVol,
			Score:   score,
		})
	}

	// Sort by score descending
	sort.Slice(scores, func(i, j int) bool { return scores[i].Score > scores[j].Score })

	// Print table
	fmt.Printf("\n%-6s %10s %8s %7s %5s %-8s %8s %7s %7s\n",
		"Symbol", "Price", "ATR", "ATR%", "NR7", "Bias", "EMA200", "RVol", "Score")
	fmt.Println(strings.Repeat("-", 75))

	for _, s := range scores {
		nr7Str := " -"
		if s.NR7 {
			nr7Str = " Y"
		}
		passATR := "✗"
		if s.ATRPct >= 0.8 {
			passATR = "✓"
		}

		fmt.Printf("%-6s %10.2f %8.2f %6.1f%% %s%s %-8s %8.2f %6.1f%% %7.1f\n",
			s.Symbol, s.Price, s.ATR, s.ATRPct, nr7Str, passATR, s.Bias, s.EMA200, s.RealVol, s.Score)
	}

	fmt.Println(strings.Repeat("-", 75))
	fmt.Printf("\nATR%% filter (min 0.8%%): %d/%d symbols pass\n",
		countPassing(scores, 0.8), len(scores))
	fmt.Printf("NR7 compression days: %d/%d\n", countNR7(scores), len(scores))

	// Print recommendations
	fmt.Println("\n--- TOP PICKS FOR ORB TODAY ---")
	rank := 0
	for _, s := range scores {
		if s.ATRPct < 0.8 {
			continue
		}
		rank++
		tags := []string{}
		if s.NR7 {
			tags = append(tags, "NR7")
		}
		switch s.Bias {
		case "BULLISH":
			tags = append(tags, "BULL")
		case "BEARISH":
			tags = append(tags, "BEAR")
		}
		tagStr := ""
		if len(tags) > 0 {
			tagStr = " [" + strings.Join(tags, ", ") + "]"
		}
		fmt.Printf("  %d. %s — ATR %.1f%%, RVol %.1f%%%s\n", rank, s.Symbol, s.ATRPct, s.RealVol, tagStr)
		if rank >= 10 {
			break
		}
	}
}

func countPassing(scores []symbolScore, minATRPct float64) int {
	n := 0
	for _, s := range scores {
		if s.ATRPct >= minATRPct {
			n++
		}
	}
	return n
}

func countNR7(scores []symbolScore) int {
	n := 0
	for _, s := range scores {
		if s.NR7 {
			n++
		}
	}
	return n
}
