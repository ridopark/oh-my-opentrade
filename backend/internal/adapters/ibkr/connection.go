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

	// reconnectEscalateAfter is the attempt count at which keepAlive emits a
	// Discord alert. With the current exponential backoff (5s, 10s, 20s, 40s,
	// 60s, 60s…) this lands at roughly 3 minutes of continuous downtime — long
	// enough to ignore the single-flap case, short enough that an operator can
	// still intervene before positions drift unmanaged.
	reconnectEscalateAfter int64 = 6

	// reconnectFatalAfter is the attempt count at which keepAlive fires the
	// installed fatal-halt callback. At the capped 60s delay this is about
	// 1 hour of continuous failure — well past any reasonable transient outage,
	// and the point at which we would rather pull the plug on trading than
	// continue to hope.
	reconnectFatalAfter int64 = 60
)

// FatalHaltFunc is invoked by the keepAlive loop once reconnect attempts
// exceed reconnectFatalAfter. Implementations should trip the global
// trading-halt circuit (e.g. DailyLossBreaker.SetGlobalHalt) and stop the
// orchestrator. Wired from main.go to avoid an import cycle between this
// adapter and the app/execution + app/risk packages.
type FatalHaltFunc func(reason string)

// ReconnectNotifier is the minimal surface the connection needs from a
// Discord/notify service. *notify.Service.NotifySync satisfies this without
// dragging the notify package into the ibkr adapter.
type ReconnectNotifier interface {
	NotifySync(message string)
}

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

	// Escalation wiring — installed post-construction via Adapter setters.
	notifier  ReconnectNotifier
	fatalHalt FatalHaltFunc

	// reconnectAttempts counts consecutive failed reconnects since the last
	// successful connect. Reset to 0 on success. Read atomically.
	reconnectAttempts atomic.Int64
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
		return fmt.Errorf("ibkr: PaperMode=true but port=%d is live; set Port=4002 or unset", port)
	}
	if !c.cfg.PaperMode && port == portPaper {
		return fmt.Errorf("ibkr: PaperMode=false but port=%d is paper; set Port=4001 or unset", port)
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
	ib.ReqPositions()

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
			if c.isConnected() {
				continue
			}

			attempts := c.reconnectAttempts.Add(1)
			c.log.Warn().
				Dur("retry_in", delay).
				Int64("attempt", attempts).
				Msg("ibkr: connection lost, reconnecting")

			if err := c.connect(); err != nil {
				c.log.Error().Err(err).Int64("attempt", attempts).Msg("ibkr: reconnect failed")
				c.onReconnectFailure(attempts, err)
				delay *= 2
				if delay > reconnectMaxDelay {
					delay = reconnectMaxDelay
				}
			} else {
				c.onReconnectSuccess()
				delay = reconnectInitialDelay
				c.fireReconnectCallbacks()
			}
			ticker.Reset(delay)
		}
	}
}

// onReconnectFailure handles a single failed reconnect attempt: emits
// the one-shot escalation alert at reconnectEscalateAfter and the fatal
// halt at reconnectFatalAfter. Using == (not >=) guarantees exactly one
// alert per outage regardless of how many subsequent ticks pass through
// this branch — critical to avoid a Discord spam loop.
func (c *connection) onReconnectFailure(attempts int64, err error) {
	c.mu.RLock()
	notifier := c.notifier
	fatalHalt := c.fatalHalt
	c.mu.RUnlock()

	if attempts == reconnectEscalateAfter && notifier != nil {
		notifier.NotifySync(fmt.Sprintf(
			"IBKR disconnected: %d consecutive reconnect failures — latest error: %v",
			attempts, err,
		))
	}

	if attempts == reconnectFatalAfter {
		if notifier != nil {
			notifier.NotifySync(fmt.Sprintf(
				"IBKR down for %d attempts — activating kill switch", attempts,
			))
		}
		if fatalHalt != nil {
			fatalHalt(fmt.Sprintf("ibkr reconnect exhausted after %d attempts", attempts))
		}
	}
}

// onReconnectSuccess resets the attempt counter and emits a recovery
// notification when the outage was long enough to have raised an alert.
func (c *connection) onReconnectSuccess() {
	total := c.reconnectAttempts.Swap(0)
	if total > 0 {
		c.log.Info().Int64("attempts", total).Msg("ibkr: reconnect succeeded — resetting attempt counter")
		c.mu.RLock()
		notifier := c.notifier
		c.mu.RUnlock()
		if notifier != nil {
			notifier.NotifySync(fmt.Sprintf(
				"IBKR reconnected after %d attempts", total,
			))
		}
	}
}

// SetReconnectNotifier installs a Discord/alert sink that the keepAlive loop
// uses to escalate extended IBKR outages. Safe to call on an already-running
// connection.
func (c *connection) SetReconnectNotifier(n ReconnectNotifier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifier = n
}

// SetFatalHalt installs the callback invoked once reconnectFatalAfter is
// reached. Wiring lives in main.go to keep the adapter free of app-layer
// imports.
func (c *connection) SetFatalHalt(fn FatalHaltFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fatalHalt = fn
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
