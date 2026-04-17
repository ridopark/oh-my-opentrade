// cover-shorts is a one-shot operator tool that connects to IBKR Gateway
// on a unique client_id (separate from the running omo-core) and submits
// BUY orders to flatten unintended short positions that were created by
// a race in the exit-order state machine on 2026-04-17.
//
// Safety:
//   - Uses client_id=97 so it does NOT collide with omo-core (client_id=2)
//     or other operator tools (submit-limit-order, cancel-test-orders use 2).
//   - Queries GetPositions first and caps each BUY at abs(actual short qty)
//     so we cannot accidentally go long if the position is already flat.
//   - Refuses to run if any target symbol already shows non-negative qty
//     (already covered — nothing to do).
//   - Uses marketable limits, not MKT, to bound slippage.
//
// Intended targets (hardcoded for today's incident):
//
//	SOFI260501P00021000 — broker showed -19
//	CRM260424P00185000  — broker showed -3
//
// Re-run is safe: the position check will bail if already covered.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/rs/zerolog"
)

type coverTarget struct {
	occSymbol  domain.Symbol
	coverLimit float64 // aggressive BUY limit, well above current ask
}

func main() {
	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

	cfg := config.IBKRConfig{
		Host:      "localhost",
		Port:      4002,
		ClientID:  97,
		PaperMode: true,
	}

	log.Info().Int("client_id", cfg.ClientID).Msg("connecting to IBKR (one-shot cover)")
	adapter, err := ibkr.NewAdapter(cfg, log)
	if err != nil {
		log.Fatal().Err(err).Msg("connect failed")
	}
	defer adapter.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	targets := []coverTarget{
		{occSymbol: "SOFI260501P00021000", coverLimit: 2.50},
		{occSymbol: "CRM260424P00185000", coverLimit: 6.50},
	}

	positions, err := adapter.GetPositions(ctx, "default", domain.EnvModePaper)
	if err != nil {
		log.Fatal().Err(err).Msg("GetPositions failed")
	}
	// The adapter returns positions with Side+Quantity (magnitude), not signed qty.
	// Reconstruct the signed position by looking at Side: "SELL" means short.
	signedBySymbol := make(map[domain.Symbol]float64, len(positions))
	for _, p := range positions {
		q := p.Quantity
		if p.Side == "SELL" {
			q = -q
		}
		signedBySymbol[p.Symbol] = q
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
		coverQty := -qty // absolute short size

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
		q := p.Quantity
		if p.Side == "SELL" {
			q = -q
		}
		after[p.Symbol] = q
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
