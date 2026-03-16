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

func (a *Adapter) StreamBars(ctx context.Context, symbols []domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) error {
	ib := a.conn.IB()
	if ib == nil {
		return fmt.Errorf("ibkr: not connected")
	}

	a.streamMu.Lock()
	a.barCtx = ctx
	a.barTF = tf
	a.barHdl = handler
	a.streamMu.Unlock()

	for _, sym := range symbols {
		a.streamMu.Lock()
		a.streaming[sym] = struct{}{}
		a.streamMu.Unlock()
		a.startSymbolStream(ctx, sym, tf, handler)
	}

	<-ctx.Done()
	return nil
}

const barPollInterval = 65 * time.Second

func (a *Adapter) startSymbolStream(ctx context.Context, sym domain.Symbol, tf domain.Timeframe, handler ports.BarHandler) {
	go func() {
		var lastBarTime time.Time
		ticker := time.NewTicker(barPollInterval)
		defer ticker.Stop()
		a.log.Info().Str("symbol", string(sym)).Msg("ibkr: bar polling started")

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				from := time.Now().Add(-10 * time.Minute)
				to := time.Now()
				bars, err := a.GetHistoricalBars(ctx, sym, tf, from, to)
				if err != nil {
					a.log.Warn().Err(err).Str("symbol", string(sym)).Msg("ibkr: bar poll failed")
					continue
				}
				newBars := 0
				for _, bar := range bars {
					if bar.Time.After(lastBarTime) {
						lastBarTime = bar.Time
						if err := handler(ctx, bar); err != nil {
							a.log.Error().Err(err).Str("symbol", string(sym)).Msg("ibkr: bar handler error")
						}
						newBars++
					}
				}
				if newBars > 0 {
					a.log.Info().Str("symbol", string(sym)).Int("new_bars", newBars).Msg("ibkr: bars polled")
				}
			}
		}
	}()
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
		ch, cancel := ib.ReqHistoricalData(contract, endDT, duration, barSize, "MIDPOINT", false, 2)
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

func (a *Adapter) SubscribeSymbols(ctx context.Context, symbols []domain.Symbol) error {
	a.streamMu.RLock()
	hdl := a.barHdl
	tf := a.barTF
	streamCtx := a.barCtx
	a.streamMu.RUnlock()

	if hdl == nil {
		return nil
	}

	ib := a.conn.IB()
	if ib == nil {
		return fmt.Errorf("ibkr: not connected")
	}

	for _, sym := range symbols {
		a.streamMu.Lock()
		_, already := a.streaming[sym]
		if !already {
			a.streaming[sym] = struct{}{}
		}
		a.streamMu.Unlock()

		if already {
			continue
		}

		streamContext := streamCtx
		if streamContext == nil {
			streamContext = ctx
		}
		a.startSymbolStream(streamContext.(context.Context), sym, tf, hdl)
		a.log.Info().Str("symbol", string(sym)).Msg("ibkr: subscribed new symbol to real-time bars")
	}
	return nil
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
