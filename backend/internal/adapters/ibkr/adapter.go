package ibkr

import (
	"sync"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
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

type Adapter struct {
	conn *connection
	cfg  config.IBKRConfig
	log  zerolog.Logger

	streamMu  sync.RWMutex
	barCtx    interface{ Done() <-chan struct{} }
	barTF     domain.Timeframe
	barHdl    ports.BarHandler
	streaming map[domain.Symbol]struct{}
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
