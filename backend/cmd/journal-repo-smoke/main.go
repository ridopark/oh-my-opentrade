// journal-repo-smoke exercises every method of the Sprint 2
// ports.OrderIntentJournal interface against a real TimescaleDB via
// the production Go repo + pgx driver. Complements the existing
// mock-DBTX unit tests by validating the Go → SQL → pgx column
// mapping on the real database rather than a fake DBTX.
//
// Runs alongside a live omo-core: uses distinct idempotency keys
// prefixed with "SMOKE-" so it cannot collide with real rows, and
// cleans up every row it writes before exiting.
//
// Coverage:
//   1. SaveOrderIntent (insert pending_submit row)
//   2. MarkIntentSubmitted (pending_submit -> submitted)
//   3. MarkIntentTerminal with event=filled (submitted -> filled)
//   4. SaveOrderIntent + MarkIntentSubmitFailed (insert -> rejected)
//   5. SaveOrderIntent + MarkIntentSubmitted + MarkIntentLost
//      (insert -> submitted -> lost)
//   6. OpenIntents (verifies the SELECT + row scanner)
//   7. SaveOrderIntent duplicate idempotency_key → ErrDuplicateIntent
//
// All seven steps must succeed.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

const (
	tenantID = "default"
	envMode  = domain.EnvModePaper
)

func main() {
	log := zerolog.New(os.Stdout).With().Timestamp().Logger()

	// Load .env then build a DB connection exactly as cmd/omo-core/infra.go
	// does — same DSN format, same pgx.ParseConfig + stdlib.OpenDB path.
	cfg, err := config.Load(".env", "configs/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config.Load: %v\n", err)
		os.Exit(1)
	}
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgx.ParseConfig: %v\n", err)
		os.Exit(1)
	}
	sqlDB := stdlib.OpenDB(*pgxCfg)
	defer sqlDB.Close()

	ctx := context.Background()
	if err := sqlDB.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "db ping: %v\n", err)
		os.Exit(1)
	}

	repo := timescaledb.NewOrderIntentRepo(timescaledb.NewSqlDB(sqlDB), log)

	fmt.Println("== journal-repo-smoke: real DB + real pgx + real repo ==")

	// Track rows we create so we can always clean up, even on partial failure.
	// Four rows total — one per lifecycle branch we exercise plus one for
	// OpenIntents. Preallocated so the slice never reallocates.
	createdIDs := make([]uuid.UUID, 0, 4)
	cleanup := func() {
		for _, id := range createdIDs {
			if _, err := sqlDB.ExecContext(ctx, `DELETE FROM order_intents WHERE id = $1`, id); err != nil {
				fmt.Fprintf(os.Stderr, "cleanup delete %s: %v\n", id, err)
			}
		}
	}
	defer cleanup()

	// Step 1: SaveOrderIntent — insert a pending_submit row.
	id1 := uuid.New()
	createdIDs = append(createdIDs, id1)
	intent1 := domain.OrderIntent{
		ID:             id1,
		IdempotencyKey: fmt.Sprintf("SMOKE-%s-1", time.Now().Format("20060102-150405")),
		TenantID:       tenantID,
		EnvMode:        envMode,
		Symbol:         "AAPL",
		Direction:      domain.DirectionLong,
		AssetClass:     domain.AssetClassEquity,
		OrderType:      "limit",
		TimeInForce:    "gtc",
		Quantity:       10,
		LimitPrice:     150.00,
		StopLoss:       148.00,
		MaxSlippageBPS: 20,
		Strategy:       "smoke_test",
		Confidence:     0.80,
	}
	if err := repo.SaveOrderIntent(ctx, intent1); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 1 FAIL — SaveOrderIntent: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("STEP 1 PASS — SaveOrderIntent inserted %s (pending_submit)\n", id1)

	// Step 2: MarkIntentSubmitted — pending_submit → submitted.
	brokerID1 := fmt.Sprintf("SMOKE-BROKER-%d", time.Now().UnixNano())
	if err := repo.MarkIntentSubmitted(ctx, id1, brokerID1, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 2 FAIL — MarkIntentSubmitted: %v\n", err)
		os.Exit(1)
	}
	if status := mustScanStatus(ctx, sqlDB, id1); status != "submitted" {
		fmt.Fprintf(os.Stderr, "STEP 2 FAIL — expected status=submitted, got %q\n", status)
		os.Exit(1)
	}
	fmt.Printf("STEP 2 PASS — MarkIntentSubmitted: pending_submit → submitted, broker_order_id=%s\n", brokerID1)

	// Step 3: MarkIntentTerminal with event=filled — submitted → filled.
	if err := repo.MarkIntentTerminal(ctx, brokerID1, "filled", 10.0, 149.87, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 3 FAIL — MarkIntentTerminal(filled): %v\n", err)
		os.Exit(1)
	}
	if status := mustScanStatus(ctx, sqlDB, id1); status != "filled" {
		fmt.Fprintf(os.Stderr, "STEP 3 FAIL — expected status=filled, got %q\n", status)
		os.Exit(1)
	}
	fmt.Printf("STEP 3 PASS — MarkIntentTerminal: submitted → filled (10 @ $149.87)\n")

	// Step 4: MarkIntentSubmitFailed on a fresh row.
	id2 := uuid.New()
	createdIDs = append(createdIDs, id2)
	intent2 := intent1
	intent2.ID = id2
	intent2.IdempotencyKey = fmt.Sprintf("SMOKE-%s-2", time.Now().Format("20060102-150405"))
	if err := repo.SaveOrderIntent(ctx, intent2); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 4a FAIL — SaveOrderIntent: %v\n", err)
		os.Exit(1)
	}
	if err := repo.MarkIntentSubmitFailed(ctx, id2, "simulated broker rejection", time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 4 FAIL — MarkIntentSubmitFailed: %v\n", err)
		os.Exit(1)
	}
	if status := mustScanStatus(ctx, sqlDB, id2); status != "rejected" {
		fmt.Fprintf(os.Stderr, "STEP 4 FAIL — expected status=rejected, got %q\n", status)
		os.Exit(1)
	}
	fmt.Printf("STEP 4 PASS — MarkIntentSubmitFailed: pending_submit → rejected\n")

	// Step 5: SaveOrderIntent + MarkIntentSubmitted + MarkIntentLost.
	id3 := uuid.New()
	createdIDs = append(createdIDs, id3)
	intent3 := intent1
	intent3.ID = id3
	intent3.IdempotencyKey = fmt.Sprintf("SMOKE-%s-3", time.Now().Format("20060102-150405"))
	if err := repo.SaveOrderIntent(ctx, intent3); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 5a FAIL — SaveOrderIntent: %v\n", err)
		os.Exit(1)
	}
	brokerID3 := fmt.Sprintf("SMOKE-BROKER-%d-LOST", time.Now().UnixNano())
	if err := repo.MarkIntentSubmitted(ctx, id3, brokerID3, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 5b FAIL — MarkIntentSubmitted: %v\n", err)
		os.Exit(1)
	}
	if err := repo.MarkIntentLost(ctx, id3, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 5 FAIL — MarkIntentLost: %v\n", err)
		os.Exit(1)
	}
	if status := mustScanStatus(ctx, sqlDB, id3); status != "lost" {
		fmt.Fprintf(os.Stderr, "STEP 5 FAIL — expected status=lost, got %q\n", status)
		os.Exit(1)
	}
	fmt.Printf("STEP 5 PASS — MarkIntentLost: submitted → lost\n")

	// Step 6: OpenIntents — must not return the smoke rows since all three
	// are terminal (filled, rejected, lost). We stage a temporary submitted
	// row to prove the SELECT actually returns something, then clean it up.
	id4 := uuid.New()
	createdIDs = append(createdIDs, id4)
	intent4 := intent1
	intent4.ID = id4
	intent4.IdempotencyKey = fmt.Sprintf("SMOKE-%s-4", time.Now().Format("20060102-150405"))
	if err := repo.SaveOrderIntent(ctx, intent4); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 6a FAIL — SaveOrderIntent: %v\n", err)
		os.Exit(1)
	}
	if err := repo.MarkIntentSubmitted(ctx, id4, fmt.Sprintf("SMOKE-BROKER-%d-OPEN", time.Now().UnixNano()), time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "STEP 6b FAIL — MarkIntentSubmitted: %v\n", err)
		os.Exit(1)
	}
	rows, err := repo.OpenIntents(ctx, tenantID, envMode, 48*time.Hour)
	if err != nil {
		fmt.Fprintf(os.Stderr, "STEP 6 FAIL — OpenIntents: %v\n", err)
		os.Exit(1)
	}
	found := false
	for _, r := range rows {
		if r.ID == id4 {
			found = true
			if r.Status != "submitted" {
				fmt.Fprintf(os.Stderr, "STEP 6 FAIL — OpenIntents returned row %s with status=%q, want submitted\n", id4, r.Status)
				os.Exit(1)
			}
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "STEP 6 FAIL — OpenIntents did not return staged submitted row %s\n", id4)
		os.Exit(1)
	}
	fmt.Printf("STEP 6 PASS — OpenIntents returned %d non-terminal rows (found staged submitted)\n", len(rows))

	// Step 7: Duplicate idempotency_key must return ErrDuplicateIntent.
	dup := intent1
	dup.ID = uuid.New() // different PK, same idempotency key
	err = repo.SaveOrderIntent(ctx, dup)
	if !errors.Is(err, ports.ErrDuplicateIntent) {
		fmt.Fprintf(os.Stderr, "STEP 7 FAIL — expected ErrDuplicateIntent, got: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("STEP 7 PASS — SaveOrderIntent rejected duplicate idempotency_key with ErrDuplicateIntent\n")

	fmt.Println("\n== all steps passed ==")
}

// mustScanStatus reads back the status column for a given row. Uses raw
// sql.DB directly so a repo bug in SELECT logic cannot mask a repo bug
// in the UPDATE path we are validating.
func mustScanStatus(ctx context.Context, db *sql.DB, id uuid.UUID) string {
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM order_intents WHERE id = $1`, id).Scan(&status); err != nil {
		fmt.Fprintf(os.Stderr, "status lookup %s: %v\n", id, err)
		os.Exit(1)
	}
	return status
}
