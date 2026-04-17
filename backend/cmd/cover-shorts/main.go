// cover-shorts is a one-shot operator tool that connects to IBKR Gateway
// on a unique client_id (separate from the running omo-core) and submits
// BUY orders to flatten unintended short positions. On a successful cover,
// it also writes a reconciliation trade row into the TimescaleDB trades
// ledger so the global reconciler sees DB net = broker net. Without this,
// the DB-side net stays negative forever and the reconciler alerts until
// an operator cleans up the trades table manually.
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
//   - DB writes are best-effort: if the DB is unreachable the broker cover
//     still executes; operator must then clean up manually (see SQL pattern
//     in the incident runbook / 2026-04-17 reconciliation commits).
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
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oh-my-opentrade/backend/internal/adapters/ibkr"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
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

// openDB connects to TimescaleDB using the standard env vars the rest of
// omo-core reads. Returns nil (and logs a warning) if the connection fails —
// the cover action can still proceed; the operator just needs to write the
// reconciliation rows manually afterwards.
func openDB(log zerolog.Logger) *sql.DB {
	host := os.Getenv("TIMESCALEDB_HOST")
	port := os.Getenv("TIMESCALEDB_PORT")
	user := os.Getenv("TIMESCALEDB_USER")
	password := os.Getenv("TIMESCALEDB_PASSWORD")
	dbname := os.Getenv("TIMESCALEDB_NAME")
	if host == "" || port == "" || user == "" || dbname == "" {
		log.Warn().Msg("TIMESCALEDB_* env vars not fully set — skipping DB reconciliation writes")
		return nil
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		log.Warn().Err(err).Msg("pgx parse failed — skipping DB reconciliation writes")
		return nil
	}
	db := stdlib.OpenDB(*cfg)
	if pErr := db.PingContext(context.Background()); pErr != nil {
		log.Warn().Err(pErr).Msg("DB ping failed — skipping DB reconciliation writes")
		_ = db.Close()
		return nil
	}
	return db
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

	db := openDB(log)
	var repo *timescaledb.Repository
	if db != nil {
		defer db.Close()
		repo = timescaledb.NewRepositoryWithLogger(timescaledb.NewSqlDB(db), log.With().Str("component", "reconcile_repo").Logger())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	positions, err := adapter.GetPositions(ctx, "default", domain.EnvModePaper)
	if err != nil {
		log.Fatal().Err(err).Msg("GetPositions failed")
	}
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

	// For each target, verify position changed and write a reconciliation
	// trade row into the DB. The covered_qty is derived from the broker
	// delta so we only record trades that actually filled. If the DB is
	// unavailable we log the exact SQL to run manually.
	for _, t := range targets {
		before := signedBySymbol[t.occSymbol]
		nowQty := after[t.occSymbol]
		log.Info().
			Str("symbol", string(t.occSymbol)).
			Float64("before", before).
			Float64("after", nowQty).
			Msg("position check")

		coveredQty := nowQty - before
		if coveredQty <= 0 {
			continue
		}

		if repo == nil {
			log.Warn().
				Str("symbol", string(t.occSymbol)).
				Float64("covered_qty", coveredQty).
				Float64("limit", t.coverLimit).
				Msg("DB unavailable — NOT writing reconciliation row; clean up manually")
			continue
		}

		trade := buildReconciliationTrade(t.occSymbol, coveredQty, t.coverLimit)
		if saveErr := repo.SaveTrade(ctx, trade); saveErr != nil {
			log.Error().Err(saveErr).
				Str("symbol", string(t.occSymbol)).
				Msg("failed to write reconciliation trade row — MUST clean up manually")
			continue
		}
		log.Info().
			Str("symbol", string(t.occSymbol)).
			Float64("qty", coveredQty).
			Float64("price", t.coverLimit).
			Str("strategy", "reconciliation").
			Msg("reconciliation BUY row written to trades DB")
	}

	fmt.Println("done")
}

// buildReconciliationTrade constructs the compensating BUY row. Uses the
// limit price as the fill-price approximation (actual broker fill is
// typically tighter, but the limit is the conservative upper bound for
// accounting and matches what the operator submitted). OCC symbol is
// decoded for the option fields so downstream tools can filter on
// instrument_type=OPTION, strategy=reconciliation.
func buildReconciliationTrade(sym domain.Symbol, qty, price float64) domain.Trade {
	t := domain.Trade{
		Time:       time.Now().UTC(),
		TenantID:   "default",
		EnvMode:    domain.EnvModePaper,
		TradeID:    uuid.New(),
		Symbol:     sym,
		Side:       "BUY",
		Quantity:   qty,
		Price:      price,
		Commission: 0,
		Status:     "FILLED",
		Strategy:   "reconciliation",
		Rationale:  "cover-shorts: broker-side cover via client_id=97 at $" + strconv.FormatFloat(price, 'f', 2, 64) + " limit; zeros DB net to match broker reality",
	}
	if domain.IsOCCSymbol(sym) {
		t.InstrumentType = domain.InstrumentTypeOption
		t.OptionSymbol = string(sym)
		if underlying, expiry, strike, right, ok := decodeOCC(sym); ok {
			t.Underlying = underlying
			t.Expiry = expiry
			t.Strike = strike
			t.OptionRight = right
		}
	}
	return t
}

// decodeOCC parses an OCC option symbol like "SOFI260501P00021000" into
// its parts. Returns ok=false if the string is too short to be valid OCC.
func decodeOCC(sym domain.Symbol) (underlying string, expiry time.Time, strike float64, right string, ok bool) {
	s := string(sym)
	if len(s) < 15 {
		return "", time.Time{}, 0, "", false
	}
	suffix := s[len(s)-15:]
	underlying = s[:len(s)-15]
	exp, err := time.Parse("060102", suffix[:6])
	if err != nil {
		return "", time.Time{}, 0, "", false
	}
	right = string(suffix[6])
	strikeInt, err := strconv.ParseFloat(suffix[7:], 64)
	if err != nil {
		return "", time.Time{}, 0, "", false
	}
	return underlying, exp, strikeInt / 1000.0, right, true
}
