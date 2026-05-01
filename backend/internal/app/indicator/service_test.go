package indicator_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/domain"
)

const (
	testSymbol    domain.Symbol    = "TEST"
	testTimeframe domain.Timeframe = "1m"
	testBarCount                   = 250
)

func testAnchor(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	return time.Date(2025, 1, 6, 9, 30, 0, 0, loc)
}

func TestService_UpdateParity(t *testing.T) {
	raw := monitor.NewIndicatorCalculator()
	raw.Label = "raw"
	svc := indicator.NewService("wrapped")

	bars := indicatortest.MakeBars(testSymbol, 200.0, testAnchor(t), testBarCount)
	for i, b := range bars {
		rawSnap := raw.Update(b)
		wrappedSnap := svc.Update(b)
		indicatortest.AssertSnapshotsBitEqual(t, "Update", wrappedSnap, rawSnap, fmt.Sprintf("bar=%d", i))
	}
}

func TestService_LastSnapshotMatchesUpdate(t *testing.T) {
	svc := indicator.NewService("last")
	bars := indicatortest.MakeBars(testSymbol, 200.0, testAnchor(t), testBarCount)
	for i, b := range bars {
		updated := svc.Update(b)
		got, ok := svc.LastSnapshot(b.Symbol, b.Timeframe)
		if !ok {
			t.Fatalf("bar %d: LastSnapshot missing for %s/%s", i, b.Symbol, b.Timeframe)
		}
		indicatortest.AssertSnapshotsBitEqual(t, "LastSnapshot", got, updated, fmt.Sprintf("bar=%d", i))
	}
}

func TestService_LastSnapshotMissingKey(t *testing.T) {
	svc := indicator.NewService("missing")
	if _, ok := svc.LastSnapshot("UNKNOWN", "1m"); ok {
		t.Fatalf("LastSnapshot for unseen key should report ok=false")
	}
}

func TestService_WarmUpEqualsSerialUpdate(t *testing.T) {
	bars := indicatortest.MakeBars(testSymbol, 200.0, testAnchor(t), testBarCount)

	serial := indicator.NewService("serial")
	var lastSerial domain.IndicatorSnapshot
	for _, b := range bars {
		lastSerial = serial.Update(b)
	}

	batch := indicator.NewService("batch")
	batch.WarmUp(bars)
	lastBatch, ok := batch.LastSnapshot(testSymbol, testTimeframe)
	if !ok {
		t.Fatalf("WarmUp: LastSnapshot missing after batch feed")
	}
	indicatortest.AssertSnapshotsBitEqual(t, "WarmUp", lastBatch, lastSerial, fmt.Sprintf("bar=%d", len(bars)-1))
}
