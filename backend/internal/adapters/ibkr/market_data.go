package ibkr

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/scmhub/ibsync"
)

const maxRealTimeBarsSymbols = 50

func (a *Adapter) StreamBars(ctx context.Context, symbols []domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) error {
	if len(symbols) > maxRealTimeBarsSymbols {
		return fmt.Errorf("ibkr: StreamBars: pacing limit exceeded: max %d symbols, got %d", maxRealTimeBarsSymbols, len(symbols))
	}
	ib := a.conn.IB()
	if ib == nil {
		return fmt.Errorf("ibkr: StreamBars: not connected")
	}

	a.streamMu.Lock()
	a.barCtx = ctx
	a.barTF = tf
	a.barHdl = handler
	a.streamMu.Unlock()

	var wg sync.WaitGroup
	var cancelMu sync.Mutex
	cancelFuncs := make([]ibsync.CancelFunc, 0, len(symbols))

	for _, sym := range symbols {
		a.streamMu.Lock()
		a.streaming[sym] = struct{}{}
		a.streamMu.Unlock()

		contract := newContract(sym)
		barCh, cancel := ib.ReqRealTimeBars(contract, 5, "TRADES", false)

		cancelMu.Lock()
		cancelFuncs = append(cancelFuncs, cancel)
		cancelMu.Unlock()

		wg.Add(1)
		go func(symbol domain.Symbol, ch <-chan ibsync.RealTimeBar) {
			defer wg.Done()
			agg := newBarAggregator(symbol, tf)
			for {
				select {
				case rtb, ok := <-ch:
					if !ok {
						return
					}
					if mb := agg.add(rtb); mb != nil {
						if err := handler(ctx, *mb); err != nil {
							a.log.Error().Err(err).Str("symbol", string(symbol)).Msg("ibkr: bar handler error")
						}
					}
				case <-ctx.Done():
					return
				}
			}
		}(sym, barCh)
	}

	a.conn.OnReconnect(func() {
		a.streamMu.RLock()
		hdl := a.barHdl
		tf := a.barTF
		streamCtx := a.barCtx
		syms := make([]domain.Symbol, 0, len(a.streaming))
		for s := range a.streaming {
			syms = append(syms, s)
		}
		a.streamMu.RUnlock()
		if hdl == nil || streamCtx == nil {
			return
		}
		_ = a.StreamBars(streamCtx.(context.Context), syms, tf, hdl)
	})

	go func() {
		<-ctx.Done()
		cancelMu.Lock()
		for _, cancel := range cancelFuncs {
			cancel()
		}
		cancelMu.Unlock()
		wg.Wait()
	}()

	return nil
}

func (a *Adapter) SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error {
	a.streamMu.RLock()
	hdl := a.barHdl
	tf := a.barTF
	streamCtx := a.barCtx
	a.streamMu.RUnlock()

	if hdl != nil && streamCtx != nil {
		newSyms := make([]domain.Symbol, 0)
		a.streamMu.Lock()
		for _, s := range symbols {
			if _, exists := a.streaming[s]; !exists {
				newSyms = append(newSyms, s)
				a.streaming[s] = struct{}{}
			}
		}
		a.streamMu.Unlock()
		if len(newSyms) == 0 {
			return nil
		}
		return a.StreamBars(streamCtx.(context.Context), newSyms, tf, hdl)
	}
	noop := func(_ context.Context, _ domain.MarketBar) error { return nil }
	return a.StreamBars(ctx, symbols, "1m", noop)
}

const (
	maxConcurrentHistorical = 1
	historicalTimeout       = 10 * time.Second
	historicalMinInterval   = 200 * time.Millisecond
)

var (
	historicalSem      = make(chan struct{}, maxConcurrentHistorical)
	historicalLastCall = struct {
		mu   sync.Mutex
		time time.Time
	}{}
)

func (a *Adapter) GetHistoricalBars(ctx context.Context, symbol domain.Symbol, tf domain.Timeframe, from, to time.Time) ([]domain.MarketBar, error) {
	select {
	case historicalSem <- struct{}{}:
		defer func() { <-historicalSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	a.conn.symHook.set(string(symbol))
	defer a.conn.symHook.clear()

	historicalLastCall.mu.Lock()
	since := time.Since(historicalLastCall.time)
	if since < historicalMinInterval {
		time.Sleep(historicalMinInterval - since)
	}
	historicalLastCall.time = time.Now()
	historicalLastCall.mu.Unlock()

	ib := a.conn.IB()
	if ib == nil || !ib.IsConnected() {
		return nil, nil
	}

	contract := newContract(symbol)
	endDT := ibsync.FormatIBTimeUSEastern(to)
	duration := durationStr(from, to)
	barSize := barSizeStr(tf)

	tCtx, tCancel := context.WithTimeout(ctx, historicalTimeout)
	defer tCancel()

	type reqResult struct {
		ch     chan ibsync.Bar
		cancel ibsync.CancelFunc
	}
	reqCh := make(chan reqResult, 1)
	go func() {
		ch, cancel := ib.ReqHistoricalData(contract, endDT, duration, barSize, "TRADES", false, 2)
		reqCh <- reqResult{ch, cancel}
	}()

	var barsCh chan ibsync.Bar
	var cancelReq ibsync.CancelFunc
	select {
	case r := <-reqCh:
		barsCh = r.ch
		cancelReq = r.cancel
		defer cancelReq()
	case <-tCtx.Done():
		a.log.Warn().Str("symbol", string(symbol)).Str("tf", string(tf)).Msg("ibkr: historical data request timed out (connecting)")
		return nil, nil
	}

	var bars []domain.MarketBar
	for {
		select {
		case b, ok := <-barsCh:
			if !ok {
				return bars, nil
			}
			ts, err := strconv.ParseInt(b.Date, 10, 64)
			var t time.Time
			if err == nil {
				t = time.Unix(ts, 0).UTC()
			} else {
				t, _ = ibsync.ParseIBTime(b.Date)
			}
			bars = append(bars, domain.MarketBar{
				Symbol:    symbol,
				Timeframe: tf,
				Time:      t,
				Open:      b.Open,
				High:      b.High,
				Low:       b.Low,
				Close:     b.Close,
				Volume:    b.Volume.Float(),
			})
		case <-tCtx.Done():
			a.log.Warn().Str("symbol", string(symbol)).Str("tf", string(tf)).Msg("ibkr: historical data request timed out")
			return bars, nil
		}
	}
}

func (a *Adapter) Close() error {
	return a.conn.disconnect()
}

func durationStr(from, to time.Time) string {
	days := int(to.Sub(from).Hours()/24) + 1
	switch {
	case days <= 1:
		return "1 D"
	case days <= 7:
		return fmt.Sprintf("%d D", days)
	case days <= 30:
		weeks := (days + 6) / 7
		return fmt.Sprintf("%d W", weeks)
	case days <= 365:
		months := (days + 29) / 30
		return fmt.Sprintf("%d M", months)
	default:
		years := (days + 364) / 365
		return fmt.Sprintf("%d Y", years)
	}
}

func barSizeStr(tf domain.Timeframe) string {
	switch string(tf) {
	case "1m":
		return "1 min"
	case "5m":
		return "5 mins"
	case "15m":
		return "15 mins"
	case "1h":
		return "1 hour"
	case "1d":
		return "1 day"
	default:
		return "1 min"
	}
}
