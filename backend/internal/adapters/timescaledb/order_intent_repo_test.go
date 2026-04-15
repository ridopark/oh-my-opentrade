package timescaledb_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oh-my-opentrade/backend/internal/adapters/timescaledb"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestIntent(t *testing.T) domain.OrderIntent {
	t.Helper()
	return domain.OrderIntent{
		ID:             uuid.New(),
		TenantID:       "default",
		EnvMode:        domain.EnvModePaper,
		Symbol:         "AAPL",
		Direction:      domain.DirectionLong,
		LimitPrice:     150.0,
		StopLoss:       148.0,
		MaxSlippageBPS: 10,
		Quantity:       10,
		Strategy:       "orb_break_retest",
		Rationale:      "break and retest",
		Confidence:     0.75,
		IdempotencyKey: "idem-" + uuid.NewString(),
		OrderType:      "limit",
		TimeInForce:    "gtc",
		AssetClass:     domain.AssetClassEquity,
	}
}

func TestOrderIntentRepo_ImplementsPort(t *testing.T) {
	var _ ports.OrderIntentJournal = (*timescaledb.OrderIntentRepo)(nil)
}

func TestOrderIntentRepo_SaveOrderIntent_Insert(t *testing.T) {
	intent := newTestIntent(t)
	called := false
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			called = true
			assert.True(t, strings.Contains(query, "INSERT INTO order_intents"))
			assert.Equal(t, intent.ID, args[0])
			assert.Equal(t, intent.IdempotencyKey, args[1])
			assert.Equal(t, intent.TenantID, args[2])
			assert.Equal(t, string(intent.EnvMode), args[3])
			assert.Equal(t, string(intent.Symbol), args[4])
			assert.Equal(t, "pending_submit", args[18])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	require.NoError(t, repo.SaveOrderIntent(context.Background(), intent))
	require.True(t, called)
}

func TestOrderIntentRepo_SaveOrderIntent_VenueNullWhenUnspecified(t *testing.T) {
	// Equity intents built before Gap 10 leave Venue empty; the journal
	// must persist that as NULL so the implicit DefaultVenue path stays
	// authoritative. Otherwise we'd freeze today's default into old rows.
	intent := newTestIntent(t)
	var gotVenue any = "sentinel"
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			require.Len(t, args, 22)
			gotVenue = args[21]
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	require.NoError(t, repo.SaveOrderIntent(context.Background(), intent))
	assert.Nil(t, gotVenue, "unspecified venue must map to NULL, not an empty string")
}

func TestOrderIntentRepo_SaveOrderIntent_VenuePersisted(t *testing.T) {
	intent := newTestIntent(t)
	intent.AssetClass = domain.AssetClassCrypto
	intent.Venue = domain.VenueHyperliquid
	var gotVenue any
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			require.Len(t, args, 22)
			gotVenue = args[21]
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	require.NoError(t, repo.SaveOrderIntent(context.Background(), intent))
	assert.Equal(t, "hyperliquid", gotVenue)
}

func TestOrderIntentRepo_SaveOrderIntent_DuplicateKey(t *testing.T) {
	intent := newTestIntent(t)
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			return nil, errors.New("pq: duplicate key value violates unique constraint \"uq_order_intents_idempotency\" (SQLSTATE 23505)")
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	err := repo.SaveOrderIntent(context.Background(), intent)
	require.ErrorIs(t, err, ports.ErrDuplicateIntent)
}

func TestOrderIntentRepo_SaveOrderIntent_DBError(t *testing.T) {
	intent := newTestIntent(t)
	dbErr := errors.New("connection refused")
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			return nil, dbErr
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	err := repo.SaveOrderIntent(context.Background(), intent)
	require.Error(t, err)
	require.NotErrorIs(t, err, ports.ErrDuplicateIntent)
}

func TestOrderIntentRepo_MarkIntentSubmitted(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	called := false
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			called = true
			assert.True(t, strings.Contains(query, "status = 'submitted'"))
			assert.Equal(t, id, args[0])
			assert.Equal(t, "BRK-1", args[1])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	require.NoError(t, repo.MarkIntentSubmitted(context.Background(), id, "BRK-1", now))
	require.True(t, called)
}

func TestOrderIntentRepo_MarkIntentSubmitFailed(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "status = 'rejected'"))
			assert.Equal(t, id, args[0])
			assert.Equal(t, "broker rejected: insufficient buying power", args[1])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	require.NoError(t, repo.MarkIntentSubmitFailed(context.Background(), id, "broker rejected: insufficient buying power", now))
}

func TestOrderIntentRepo_MarkIntentTerminal(t *testing.T) {
	now := time.Now().UTC()
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "filled_qty = $3"))
			assert.Equal(t, "BRK-1", args[0])
			assert.Equal(t, "filled", args[1])
			assert.Equal(t, 10.0, args[2])
			assert.Equal(t, 150.5, args[3])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	require.NoError(t, repo.MarkIntentTerminal(context.Background(), "BRK-1", "filled", 10.0, 150.5, now))
}

func TestOrderIntentRepo_MarkIntentTerminal_EmptyBrokerOrderIDNoop(t *testing.T) {
	called := false
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			called = true
			return mockResult{}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	require.NoError(t, repo.MarkIntentTerminal(context.Background(), "", "filled", 1, 1, time.Now()))
	require.False(t, called, "empty broker order id should short-circuit without touching DB")
}

func TestOrderIntentRepo_MarkIntentLost(t *testing.T) {
	id := uuid.New()
	db := &mockDB{
		execFunc: func(ctx context.Context, query string, args ...any) (sql.Result, error) {
			assert.True(t, strings.Contains(query, "status = 'lost'"))
			assert.Equal(t, id, args[0])
			return mockResult{affected: 1}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	require.NoError(t, repo.MarkIntentLost(context.Background(), id, time.Now()))
}

func TestOrderIntentRepo_OpenIntents_EmptyReturnsSlice(t *testing.T) {
	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			assert.True(t, strings.Contains(query, "FROM order_intents"))
			assert.Equal(t, "default", args[0])
			assert.Equal(t, string(domain.EnvModePaper), args[1])
			return &mockRows{data: [][]any{}}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	rows, err := repo.OpenIntents(context.Background(), "default", domain.EnvModePaper, 48*time.Hour)
	require.NoError(t, err)
	require.NotNil(t, rows)
	require.Len(t, rows, 0)
}

func TestOrderIntentRepo_OpenIntents_ReturnsRow(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	row := []any{
		id,                      // id (uuid.UUID)
		"idem-1",                // idempotency_key (string)
		"default",               // tenant_id (string)
		"paper",                 // env_mode (string)
		"AAPL",                  // symbol (string)
		"LONG",                  // direction (string)
		"equity",                // asset_class (string)
		"limit",                 // order_type (string)
		"gtc",                   // time_in_force (string)
		10.0,                    // quantity (float64)
		150.0,                   // limit_price (float64)
		148.0,                   // stop_loss (float64)
		10,                      // max_slippage_bps (int)
		"orb_break_retest",      // strategy (string)
		0.75,                    // confidence (float64)
		0.0,                     // max_loss_usd (float64)
		"",                      // instrument_kind (string)
		sql.NullString{},        // instrument_json (nullable) -> pass via *sql.NullString
		"submitted",             // status (string)
		"BRK-42",                // broker_order_id (string)
		"",                      // submit_error (string)
		0.0,                     // filled_qty (float64)
		0.0,                     // filled_avg_price (float64)
		now,                     // created_at (time.Time)
		sql.NullTime{Time: now, Valid: true}, // submitted_at
		sql.NullTime{},          // terminal_at
		sql.NullString{},        // meta
	}
	// The mockRows Scan expects raw values (not NullString wrappers). Translate
	// to the raw interface so the switch on *sql.NullString dispatches on nil.
	raw := make([]any, len(row))
	for i, v := range row {
		switch vv := v.(type) {
		case sql.NullString:
			if vv.Valid {
				raw[i] = vv.String
			} else {
				raw[i] = nil
			}
		case sql.NullTime:
			if vv.Valid {
				raw[i] = vv.Time
			} else {
				raw[i] = nil
			}
		default:
			raw[i] = v
		}
	}
	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			return &mockRows{data: [][]any{raw}}, nil
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	rows, err := repo.OpenIntents(context.Background(), "default", domain.EnvModePaper, 48*time.Hour)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, id, rows[0].ID)
	assert.Equal(t, "submitted", rows[0].Status)
	assert.Equal(t, "BRK-42", rows[0].BrokerOrderID)
	assert.NotNil(t, rows[0].SubmittedAt)
	assert.Nil(t, rows[0].TerminalAt)
}

func TestOrderIntentRepo_OpenIntents_QueryError(t *testing.T) {
	dbErr := errors.New("db down")
	db := &mockDB{
		queryFunc: func(ctx context.Context, query string, args ...any) (timescaledb.Rows, error) {
			return nil, dbErr
		},
	}
	repo := timescaledb.NewOrderIntentRepo(db, zerolog.Nop())
	_, err := repo.OpenIntents(context.Background(), "default", domain.EnvModePaper, 48*time.Hour)
	require.ErrorIs(t, err, dbErr)
}
