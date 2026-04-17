// cover-shorts is a one-shot operator tool that connects to IBKR Gateway
// on a unique client_id (separate from the running omo-core) and submits
// BUY orders to flatten unintended short positions.
//
// Safety:
//   - Uses client_id=97 so it does NOT collide with omo-core (client_id=2)
//     or other operator tools (submit-limit-order, cancel-test-orders use 2).
//   - Queries GetPositions first and caps each BUY at abs(actual short qty)
//     so we cannot accidentally go long if the position is already flat.
//   - Refuses to run if any target symbol already shows non-negative qty
//     (already covered — nothing to do).
//   - Uses marketable limits, not MKT, to bound slippage.
//   - Dumps ALL broker positions at the start for audit so hidden phantom
//     shorts outside the target list are visible.
//
// Usage:
//
//	cover-shorts SYMBOL1 LIMIT1 [SYMBOL2 LIMIT2 ...]
//
// Example:
//
//	cover-shorts SOFI260501P00021000 2.50 CRM260424P00185000 6.50
//
// Re-run is safe: the position check bails if already flat.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

type coverTarget struct {
	occSymbol  domain.Symbol
	coverLimit float64
}

func parseTargets(args []string) ([]coverTarget, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("no targets provided")
	}
	if len(args)%2 != 0 {
		return nil, fmt.Errorf("args must be pairs of SYMBOL LIMIT (got %d args)", len(args))
	}
	out := make([]coverTarget, 0, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		limit, err := strconv.ParseFloat(args[i+1], 64)
		if err != nil {
			return nil, fmt.Errorf("limit %q for %s is not a number: %w", args[i+1], args[i], err)
		}
		if limit <= 0 {
			return nil, fmt.Errorf("limit for %s must be positive, got %v", args[i], limit)
		}
		out = append(out, coverTarget{occSymbol: domain.Symbol(args[i]), coverLimit: limit})
	}
	return out, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: cover-shorts SYMBOL1 LIMIT1 [SYMBOL2 LIMIT2 ...]")
	fmt.Fprintln(os.Stderr, "example: cover-shorts SOFI260501P00021000 2.50 CRM260424P00185000 6.50")
}

func main() {
	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

	targets, err := parseTargets(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage()
		os.Exit(2)
	}

	cfg := config.IBKRConfig{
		Host:      "localhost",
		Port:      4002,
		ClientID:  97,
		PaperMode: true,
	}

	log.Info().Int("client_id", cfg.ClientID).Int("targets", len(targets)).Msg("connecting to IBKR (one-shot cover)")
	adapter, err := ibkr.NewAdapter(cfg, log)
	if err != nil {
		log.Fatal().Err(err).Msg("connect failed")
	}
	defer adapter.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	positions, err := adapter.GetPositions(ctx, "default", domain.EnvModePaper)
	if err != nil {
		log.Fatal().Err(err).Msg("GetPositions failed")
	}
	// Audit dump: every broker position with its signed quantity so hidden
	// phantom shorts outside the target list are visible in the run.
	for _, p := range positions {
		log.Info().
			Str("symbol", string(p.Symbol)).
			Float64("signed_qty", p.SignedQuantity()).
			Float64("avg_price", p.Price).
			Msg("broker position audit")
	}

	signedBySymbol := make(map[domain.Symbol]float64, len(positions))
	for _, p := range positions {
		signedBySymbol[p.Symbol] = p.SignedQuantity()
	}

	for _, t := range targets {
		qty, ok := signedBySymbol[t.occSymbol]
		if !ok {
			log.Warn().Str("symbol", string(t.occSymbol)).Msg("skipped — no position found on broker")
			continue
		}
		if qty >= 0 {
			log.Info().Str("symbol", string(t.occSymbol)).Float64("qty", qty).Msg("skipped — already flat or long")
			continue
		}
		coverQty := -qty

		intent := domain.OrderIntent{
			Symbol:      t.occSymbol,
			Direction:   domain.DirectionLong,
			Quantity:    coverQty,
			LimitPrice:  t.coverLimit,
			OrderType:   "limit",
			TimeInForce: "day",
		}

		log.Info().
			Str("symbol", string(t.occSymbol)).
			Float64("cover_qty", coverQty).
			Float64("limit", t.coverLimit).
			Msg("submitting cover BUY")

		orderID, serr := adapter.SubmitOrder(ctx, intent)
		if serr != nil {
			log.Error().Err(serr).Str("symbol", string(t.occSymbol)).Msg("submit failed")
			continue
		}
		log.Info().Str("symbol", string(t.occSymbol)).Str("broker_order_id", orderID).Msg("cover submitted")
	}

	log.Info().Msg("sleeping 5s to let fills settle")
	time.Sleep(5 * time.Second)

	positions2, err := adapter.GetPositions(ctx, "default", domain.EnvModePaper)
	if err != nil {
		log.Error().Err(err).Msg("post-cover GetPositions failed")
		return
	}
	after := make(map[domain.Symbol]float64, len(positions2))
	for _, p := range positions2 {
		after[p.Symbol] = p.SignedQuantity()
	}

	for _, t := range targets {
		before := signedBySymbol[t.occSymbol]
		nowQty := after[t.occSymbol]
		log.Info().
			Str("symbol", string(t.occSymbol)).
			Float64("before", before).
			Float64("after", nowQty).
			Msg("position check")
	}

	fmt.Println("done")
}
