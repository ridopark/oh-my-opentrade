package ibkr

import (
	"context"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
)

var (
	_ ports.BrokerPort            = (*Adapter)(nil)
	_ ports.OrderStreamPort       = (*Adapter)(nil)
	_ ports.MarketDataPort        = (*Adapter)(nil)
	_ ports.AccountPort           = (*Adapter)(nil)
	_ ports.OptionsMarketDataPort = (*Adapter)(nil)
	_ ports.UniverseProviderPort  = (*Adapter)(nil)
	_ ports.SnapshotPort          = (*Adapter)(nil)
)

const accountSummaryCacheTTL = 30 * time.Second

type accountSummaryCache struct {
	mu        sync.Mutex
	summary   ibsync.AccountSummary
	fetchedAt time.Time
}

type Adapter struct {
	conn *connection
	cfg  config.IBKRConfig
	log  zerolog.Logger

	streamMu  sync.RWMutex
	barCtx    interface{ Done() <-chan struct{} }
	barTF     domain.Timeframe
	barHdl    ports.BarHandler
	streaming map[domain.Symbol]struct{}

	acctCache accountSummaryCache
}

func NewAdapter(cfg config.IBKRConfig, log zerolog.Logger) (*Adapter, error) {
	conn, err := newConnection(cfg, log.With().Str("component", "ibkr_connection").Logger())
	if err != nil {
		return nil, err
	}
	return &Adapter{
		conn:      conn,
		cfg:       cfg,
		log:       log,
		streaming: make(map[domain.Symbol]struct{}),
	}, nil
}

func (a *Adapter) IsConnected() bool {
	return a.conn.isConnected()
}

// NewAdapterWithClient creates an Adapter using an already-connected ibClient.
// Used in tests to inject a mock ibClient without a real IB Gateway connection.
func NewAdapterWithClient(client ibClient, log zerolog.Logger) *Adapter {
	_, cancel := context.WithCancel(context.Background())
	conn := &connection{ib: client, log: log, cancel: cancel}
	return &Adapter{
		conn:      conn,
		log:       log,
		streaming: make(map[domain.Symbol]struct{}),
	}
}

// NewAdapterWithClientAndCfg creates an Adapter with an injected ibClient and config.
// Used in tests that need to verify config-driven behavior (e.g. AccountID filtering).
func NewAdapterWithClientAndCfg(client ibClient, cfg config.IBKRConfig, log zerolog.Logger) *Adapter {
	_, cancel := context.WithCancel(context.Background())
	conn := &connection{ib: client, log: log, cancel: cancel}
	return &Adapter{
		conn:      conn,
		cfg:       cfg,
		log:       log,
		streaming: make(map[domain.Symbol]struct{}),
	}
}
