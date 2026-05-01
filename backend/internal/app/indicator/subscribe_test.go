package indicator_test

import (
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

const (
	subSymbol domain.Symbol    = "AAPL"
	htfTF     domain.Timeframe = "5m"
	oneMin    domain.Timeframe = "1m"
)

func subAnchor(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	return time.Date(2025, 1, 6, 9, 30, 0, 0, loc)
}

func TestService_Subscribe_FiresOnClosedHTFBar(t *testing.T) {
	svc := indicator.NewService("sub-fires")
	anchor := subAnchor(t)
	svc.SetSessionOpen(anchor)

	bars := indicatortest.MakeBars(subSymbol, 200.0, anchor, 5)

	type hit struct {
		closed domain.MarketBar
		snap   domain.IndicatorSnapshot
	}
	var hits []hit
	svc.Subscribe(subSymbol, htfTF, func(closed domain.MarketBar, snap domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		hits = append(hits, hit{closed, snap})
	})

	for _, b := range bars {
		svc.Update(b)
	}

	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 closed 5m bar, got %d", len(hits))
	}
	got := hits[0].closed
	if got.Symbol != subSymbol {
		t.Fatalf("closed bar symbol: got %q want %q", got.Symbol, subSymbol)
	}
	if got.Timeframe != htfTF {
		t.Fatalf("closed bar timeframe: got %q want %q", got.Timeframe, htfTF)
	}
	if !got.Time.Equal(anchor) {
		t.Fatalf("closed bar time: got %v want %v", got.Time, anchor)
	}
}

func TestService_Subscribe_OrderingInvariant(t *testing.T) {
	svc := indicator.NewService("sub-order")
	anchor := subAnchor(t)
	svc.SetSessionOpen(anchor)

	bars := indicatortest.MakeBars(subSymbol, 200.0, anchor, 5)

	var fired bool
	svc.Subscribe(subSymbol, htfTF, func(closed domain.MarketBar, snap domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		fired = true
		got, ok := svc.LastSnapshot(subSymbol, htfTF)
		if !ok {
			t.Errorf("LastSnapshot missing during callback — state not committed")
			return
		}
		if got.RSI != snap.RSI || got.EMA9 != snap.EMA9 {
			t.Errorf("LastSnapshot diverges from callback snap: lastSnap=%+v cbSnap=%+v", got, snap)
		}
	})

	for i, b := range bars {
		preCount := fired
		svc.Update(b)
		if i < len(bars)-1 && fired != preCount {
			t.Fatalf("callback fired prematurely on bar %d", i)
		}
	}
	if !fired {
		t.Fatalf("callback never fired across %d bars", len(bars))
	}
}

func TestService_Subscribe_RegistrationOrder(t *testing.T) {
	svc := indicator.NewService("sub-reg-order")
	anchor := subAnchor(t)
	svc.SetSessionOpen(anchor)

	bars := indicatortest.MakeBars(subSymbol, 200.0, anchor, 5)

	var order []int
	svc.Subscribe(subSymbol, htfTF, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		order = append(order, 1)
	})
	svc.Subscribe(subSymbol, htfTF, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		order = append(order, 2)
	})
	svc.Subscribe(subSymbol, htfTF, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		order = append(order, 3)
	})

	for _, b := range bars {
		svc.Update(b)
	}

	if len(order) != 3 {
		t.Fatalf("expected 3 callback fires, got %d (%v)", len(order), order)
	}
	for i, v := range order {
		if v != i+1 {
			t.Fatalf("registration order broken: got %v want [1 2 3]", order)
		}
	}
}

func TestService_Subscribe_Unsubscribe(t *testing.T) {
	svc := indicator.NewService("sub-unsub")
	anchor := subAnchor(t)
	svc.SetSessionOpen(anchor)

	bars := indicatortest.MakeBars(subSymbol, 200.0, anchor, 5)

	var fired int
	unsub := svc.Subscribe(subSymbol, htfTF, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		fired++
	})
	unsub()
	unsub() // idempotent

	for _, b := range bars {
		svc.Update(b)
	}
	if fired != 0 {
		t.Fatalf("callback fired %d times after unsubscribe; want 0", fired)
	}
}

func TestService_PrimeAggregator_DoesNotFireSubscribers(t *testing.T) {
	svc := indicator.NewService("prime-no-fire")
	anchor := subAnchor(t)
	svc.SetSessionOpen(anchor)

	bars := indicatortest.MakeBars(subSymbol, 200.0, anchor, 5)

	var fired int
	svc.Subscribe(subSymbol, htfTF, func(_ domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		fired++
	})

	svc.PrimeAggregator(subSymbol, htfTF, bars)

	if fired != 0 {
		t.Fatalf("PrimeAggregator fired subscribers %d times; want 0", fired)
	}
}

func TestService_PrimeAggregator_DoesNotDriveCalc(t *testing.T) {
	svc := indicator.NewService("prime-no-calc")
	anchor := subAnchor(t)
	svc.SetSessionOpen(anchor)

	bars := indicatortest.MakeBars(subSymbol, 200.0, anchor, 5)

	svc.PrimeAggregator(subSymbol, htfTF, bars)

	if _, ok := svc.LastSnapshot(subSymbol, oneMin); ok {
		t.Fatalf("PrimeAggregator should not drive calc on 1m bars (LastSnapshot returned ok=true)")
	}
	if _, ok := svc.LastSnapshot(subSymbol, htfTF); ok {
		t.Fatalf("PrimeAggregator should not drive calc on closed HTF bars (LastSnapshot returned ok=true)")
	}
}

func TestService_SetSessionOpen_AlignsAggregators(t *testing.T) {
	svc := indicator.NewService("session-align")
	anchor := subAnchor(t)
	svc.SetSessionOpen(anchor)

	var closedTimes []time.Time
	svc.Subscribe(subSymbol, htfTF, func(closed domain.MarketBar, _ domain.IndicatorSnapshot, _ domain.MarketBarEnvelope) {
		closedTimes = append(closedTimes, closed.Time)
	})

	bars := indicatortest.MakeBars(subSymbol, 200.0, anchor, 15)
	for _, b := range bars {
		svc.Update(b)
	}

	if len(closedTimes) < 2 {
		t.Fatalf("expected >= 2 closed 5m bars from 15 1m bars, got %d", len(closedTimes))
	}
	if !closedTimes[0].Equal(anchor) {
		t.Fatalf("first closed 5m bar start: got %v want %v", closedTimes[0], anchor)
	}
	if !closedTimes[1].Equal(anchor.Add(5 * time.Minute)) {
		t.Fatalf("second closed 5m bar start: got %v want %v", closedTimes[1], anchor.Add(5*time.Minute))
	}
}
