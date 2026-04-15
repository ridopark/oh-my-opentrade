package ibkr

import (
	"fmt"
	"time"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/scmhub/ibsync"
)

// warmQuote holds a persistent ReqMktData ticker for a symbol. The ibsync
// *Ticker is updated in-place by the library's callback wrapper, so reading
// Bid()/Ask() returns the most recent streamed values.
type warmQuote struct {
	ticker   *ibsync.Ticker
	contract *ibsync.Contract
}

// WarmMktData opens persistent streaming market-data subscriptions for the
// given symbols. Callers pay the farm wake-up cost once at startup instead
// of on every slippage check, eliminating the 5s snapshot timeout seen when
// IBKR's uscrypto farm goes idle between infrequent crypto signals.
//
// Reconnects re-subscribe automatically via OnReconnect.
func (a *Adapter) WarmMktData(symbols []domain.Symbol) error {
	ib := a.conn.IB()
	if ib == nil {
		return fmt.Errorf("ibkr: WarmMktData: not connected")
	}

	a.warmMu.Lock()
	if a.warmQuotes == nil {
		a.warmQuotes = make(map[domain.Symbol]*warmQuote)
	}
	for _, sym := range symbols {
		if _, exists := a.warmQuotes[sym]; exists {
			continue
		}
		contract := newContract(sym)
		ticker := ib.ReqMktData(contract, "")
		a.warmQuotes[sym] = &warmQuote{ticker: ticker, contract: contract}
		a.log.Info().Str("symbol", string(sym)).Msg("ibkr: warm market-data subscription opened")
	}
	a.warmMu.Unlock()

	a.warmReconnectOnce.Do(func() {
		a.conn.OnReconnect(func() {
			a.rewarmMktData()
		})
	})

	return nil
}

// rewarmMktData re-issues ReqMktData for all warm symbols after a reconnect.
// The old *Ticker pointers are abandoned; the new ones are stored in place.
func (a *Adapter) rewarmMktData() {
	ib := a.conn.IB()
	if ib == nil {
		return
	}
	a.warmMu.Lock()
	defer a.warmMu.Unlock()
	for sym, wq := range a.warmQuotes {
		ticker := ib.ReqMktData(wq.contract, "")
		wq.ticker = ticker
		a.log.Info().Str("symbol", string(sym)).Msg("ibkr: warm market-data re-subscribed after reconnect")
	}
}

// hasWarmSubscription reports whether sym has an open persistent ticker,
// regardless of whether a bid/ask has arrived yet.
func (a *Adapter) hasWarmSubscription(sym domain.Symbol) bool {
	a.warmMu.RLock()
	_, ok := a.warmQuotes[sym]
	a.warmMu.RUnlock()
	return ok
}

// warmBidAsk returns (bid, ask, true) if a warm quote exists for sym and has
// populated bid/ask. Returns (0, 0, false) otherwise.
func (a *Adapter) warmBidAsk(sym domain.Symbol) (float64, float64, bool) {
	a.warmMu.RLock()
	wq, ok := a.warmQuotes[sym]
	a.warmMu.RUnlock()
	if !ok || wq.ticker == nil {
		return 0, 0, false
	}
	if !wq.ticker.HasBidAsk() {
		return 0, 0, false
	}
	return wq.ticker.Bid(), wq.ticker.Ask(), true
}

// warmBidAskWait blocks up to d for a warm ticker to populate bid/ask. Used
// at startup so the first slippage check after boot doesn't race the farm
// wake-up.
func (a *Adapter) warmBidAskWait(sym domain.Symbol, d time.Duration) (float64, float64, bool) {
	deadline := time.Now().Add(d)
	for {
		if bid, ask, ok := a.warmBidAsk(sym); ok {
			return bid, ask, true
		}
		if time.Now().After(deadline) {
			return 0, 0, false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

