package ibkr

// Sprint 5 first-ship: multi-leg BAG combo orders for 2-leg verticals.
//
// The flow is: domain.OrderIntent with Legs[] -> BuildBAGContract resolves
// each leg's conID via IBKR reqContractDetails, produces a SMART BAG contract
// with ComboLegs populated, and SubmitComboOrder submits it as a single
// atomic order. Paper and live go through the same path; simbroker does not
// simulate BAG atomicity (see broker stub elsewhere in this package).

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/oh-my-opentrade/backend/internal/domain"
	"github.com/scmhub/ibsync"
)

// conIDCache memoizes option contract conID lookups. IBKR rate-limits
// reqContractDetails so we avoid re-resolving the same OCC symbol every time
// a combo is submitted.
type conIDCache struct {
	mu    sync.RWMutex
	byKey map[string]int64
}

func newConIDCache() *conIDCache {
	return &conIDCache{byKey: make(map[string]int64)}
}

func (c *conIDCache) get(key string) (int64, bool) {
	c.mu.RLock()
	v, ok := c.byKey[key]
	c.mu.RUnlock()
	return v, ok
}

func (c *conIDCache) put(key string, id int64) {
	c.mu.Lock()
	c.byKey[key] = id
	c.mu.Unlock()
}

// adapterComboCache lazily attaches a conID cache to the Adapter. The cache
// lives as a package-level map keyed by adapter pointer so we don't have to
// modify the Adapter struct for Sprint 5 first-ship.
var (
	comboCacheMu sync.Mutex
	comboCaches  = make(map[*Adapter]*conIDCache)
)

func (a *Adapter) comboCache() *conIDCache {
	comboCacheMu.Lock()
	defer comboCacheMu.Unlock()
	c, ok := comboCaches[a]
	if !ok {
		c = newConIDCache()
		comboCaches[a] = c
	}
	return c
}

// legCacheKey builds a stable cache key from the OCC symbol and expiry.
func legCacheKey(leg domain.ComboLeg) string {
	return string(leg.OCCSymbol())
}

// resolveLegConID looks up the IBKR conID for a single option leg via
// reqContractDetails, caching the result per adapter. Returns an error if the
// IB connection is down or the contract isn't found.
func (a *Adapter) resolveLegConID(ctx context.Context, leg domain.ComboLeg) (int64, error) {
	_ = ctx // reqContractDetails on ibsync is synchronous; ctx is accepted for future use
	key := legCacheKey(leg)
	if id, ok := a.comboCache().get(key); ok {
		return id, nil
	}

	ib := a.conn.IB()
	if ib == nil {
		return 0, fmt.Errorf("ibkr: combo: not connected")
	}

	// Build an option contract lookup (not yet a BAG leg). Use YYYYMMDD
	// for the expiry to match the single-leg path.
	right := "C"
	if leg.Right == domain.OptionRightPut {
		right = "P"
	}
	lookup := ibsync.NewOption(
		string(leg.Symbol),
		leg.Expiry.Format("20060102"),
		leg.Strike,
		right,
		"SMART",
		"100",
		"USD",
	)

	cds, err := ib.ReqContractDetails(lookup)
	if err != nil {
		return 0, fmt.Errorf("ibkr: combo: reqContractDetails for %s: %w", key, err)
	}
	if len(cds) == 0 {
		return 0, fmt.Errorf("ibkr: combo: no contract details for %s", key)
	}
	id := cds[0].Contract.ConID
	if id == 0 {
		return 0, fmt.Errorf("ibkr: combo: contract details returned zero conID for %s", key)
	}
	a.comboCache().put(key, id)
	return id, nil
}

// BuildBAGContract constructs an IBKR BAG contract from the leg definitions.
// Each leg's conID is resolved via reqContractDetails (cached). The returned
// contract has SecType=BAG, Exchange=SMART, and ComboLegs populated with the
// correct BUY/SELL action derived from the sign of each leg's Ratio.
//
// First-ship scope enforces 2 legs; additional legs return an error so we
// fail loud rather than silently submitting a malformed iron condor.
func (a *Adapter) BuildBAGContract(ctx context.Context, legs []domain.ComboLeg) (*ibsync.Contract, error) {
	if len(legs) != 2 {
		return nil, fmt.Errorf("ibkr: combo: first-ship supports exactly 2 legs, got %d", len(legs))
	}
	underlying := strings.ToUpper(string(legs[0].Symbol))
	if underlying == "" {
		return nil, fmt.Errorf("ibkr: combo: empty underlying on leg 0")
	}

	bag := ibsync.NewBag()
	bag.Symbol = underlying
	bag.Exchange = "SMART"
	bag.Currency = "USD"

	bag.ComboLegs = make([]ibsync.ComboLeg, 0, len(legs))
	for i, leg := range legs {
		if leg.Ratio == 0 {
			return nil, fmt.Errorf("ibkr: combo: leg %d has zero ratio", i)
		}
		conID, err := a.resolveLegConID(ctx, leg)
		if err != nil {
			return nil, err
		}
		action := "BUY"
		ratio := leg.Ratio
		if ratio < 0 {
			action = "SELL"
			ratio = -ratio
		}
		cl := ibsync.NewComboLeg()
		cl.ConID = conID
		cl.Ratio = int64(ratio)
		cl.Action = action
		cl.Exchange = "SMART"
		bag.ComboLegs = append(bag.ComboLegs, cl)
	}
	return bag, nil
}

// SubmitComboOrder is the entry point for combo BAG orders. It is intentionally
// a thin wrapper so the existing single-leg SubmitOrder path is completely
// untouched.
//
// TODO(sprint5): wire this into SubmitOrder with an `if intent.IsCombo()`
// branch once end-to-end integration testing against an IBKR paper account is
// in place. Today no strategy emits combo intents so leaving this decoupled
// keeps the live/paper equity path byte-identical to pre-Sprint-5 behavior.
func (a *Adapter) SubmitComboOrder(ctx context.Context, intent domain.OrderIntent) (string, error) {
	if !intent.IsCombo() {
		return "", fmt.Errorf("ibkr: combo: intent is not a combo (no legs)")
	}
	ib := a.conn.IB()
	if ib == nil {
		return "", fmt.Errorf("ibkr: combo: not connected")
	}
	if intent.Quantity <= 0 {
		return "", fmt.Errorf("ibkr: combo: quantity must be > 0")
	}
	if intent.LimitPrice <= 0 {
		return "", fmt.Errorf("ibkr: combo: net limit price must be > 0")
	}

	_, err := a.BuildBAGContract(ctx, intent.Legs)
	if err != nil {
		return "", fmt.Errorf("ibkr: combo: build BAG: %w", err)
	}

	// TODO(sprint5): issue PlaceOrder on the BAG contract with a net-debit
	// limit order and return the broker order ID. Returning "not implemented"
	// for now keeps production safety: the execution service never calls this
	// yet (no combo intents in flight) so this branch cannot fire in the
	// running system.
	return "", fmt.Errorf("ibkr: combo: SubmitComboOrder not yet wired to live broker (sprint5 TODO)")
}
