package ibkr

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/rs/zerolog"
	"github.com/scmhub/ibsync"
)

type symbolHook struct {
	sym atomic.Pointer[string]
}

func (h *symbolHook) Run(e *zerolog.Event, _ zerolog.Level, _ string) {
	if s := h.sym.Load(); s != nil {
		e.Str("symbol", *s)
	}
}

func (h *symbolHook) set(sym string) { h.sym.Store(&sym) }
func (h *symbolHook) clear()         { h.sym.Store(nil) }

const (
	reconnectInitialDelay = 5 * time.Second
	reconnectMaxDelay     = 60 * time.Second
)

type connection struct {
	ib      ibClient
	cfg     config.IBKRConfig
	log     zerolog.Logger
	symHook *symbolHook
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex

	reconnectSubs []func()
	subsMu        sync.Mutex
}

func newConnection(cfg config.IBKRConfig, log zerolog.Logger) (*connection, error) {
	hook := &symbolHook{}
	ctx, cancel := context.WithCancel(context.Background())
	c := &connection{
		cfg:     cfg,
		log:     log.Hook(hook),
		symHook: hook,
		ctx:     ctx,
		cancel:  cancel,
	}
	if err := c.connect(); err != nil {
		cancel()
		return nil, err
	}
	go c.keepAlive()
	return c, nil
}

const (
	portLive  = 4001
	portPaper = 4002
)

func (c *connection) effectivePort() int {
	if c.cfg.Port != 0 {
		return c.cfg.Port
	}
	if c.cfg.PaperMode {
		return portPaper
	}
	return portLive
}

func (c *connection) connect() error {
	port := c.effectivePort()

	if c.cfg.PaperMode && port == portLive {
		c.log.Warn().Int("port", port).Msg("ibkr: PaperMode=true but connecting to live port 4001 — intended?")
	}
	if !c.cfg.PaperMode && port == portPaper {
		c.log.Warn().Int("port", port).Msg("ibkr: PaperMode=false but connecting to paper port 4002 — intended?")
	}

	ib := ibsync.NewIB()
	ib.SetLogger(c.log)

	ibCfg := ibsync.NewConfig(
		ibsync.WithHost(c.cfg.Host),
		ibsync.WithPort(port),
		ibsync.WithClientID(int64(c.cfg.ClientID)),
	)
	if err := ib.Connect(ibCfg); err != nil {
		return fmt.Errorf("ibkr connect %s:%d clientID=%d: %w", c.cfg.Host, port, c.cfg.ClientID, err)
	}

	mdType := int64(1)
	if c.cfg.MarketDataType != 0 {
		mdType = int64(c.cfg.MarketDataType)
	} else if c.cfg.PaperMode {
		mdType = 3
	}
	ib.ReqMarketDataType(mdType)

	c.mu.Lock()
	c.ib = ib
	c.mu.Unlock()

	c.log.Info().
		Str("host", c.cfg.Host).
		Int("port", port).
		Int("client_id", c.cfg.ClientID).
		Bool("paper", c.cfg.PaperMode).
		Int64("market_data_type", mdType).
		Msg("ibkr: connected")
	return nil
}

func (c *connection) keepAlive() {
	delay := reconnectInitialDelay
	ticker := time.NewTicker(delay)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if !c.isConnected() {
				c.log.Warn().Dur("retry_in", delay).Msg("ibkr: connection lost, reconnecting")
				if err := c.connect(); err != nil {
					c.log.Error().Err(err).Msg("ibkr: reconnect failed")
					delay *= 2
					if delay > reconnectMaxDelay {
						delay = reconnectMaxDelay
					}
				} else {
					delay = reconnectInitialDelay
					c.fireReconnectCallbacks()
				}
				ticker.Reset(delay)
			}
		}
	}
}

func (c *connection) OnReconnect(fn func()) {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	c.reconnectSubs = append(c.reconnectSubs, fn)
}

func (c *connection) fireReconnectCallbacks() {
	c.subsMu.Lock()
	fns := make([]func(), len(c.reconnectSubs))
	copy(fns, c.reconnectSubs)
	c.subsMu.Unlock()
	for _, fn := range fns {
		go fn()
	}
}

func (c *connection) isConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ib != nil && c.ib.IsConnected()
}

func (c *connection) disconnect() error {
	c.cancel()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ib == nil {
		return nil
	}
	return c.ib.Disconnect()
}

func (c *connection) IB() ibClient {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ib
}
