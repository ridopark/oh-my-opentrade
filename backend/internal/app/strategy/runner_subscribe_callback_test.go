package strategy_test

import (
	"context"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/indicator"
	"github.com/oh-my-opentrade/backend/internal/app/indicator/indicatortest"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	"github.com/oh-my-opentrade/backend/internal/domain"
	start "github.com/oh-my-opentrade/backend/internal/domain/strategy"
)

// TestRunner_HTFSubscribeCallback_DispatchesAtClose feeds 10 1m bars
// against a 5m-only strategy and asserts OnBar fires once per closed 5m
// candle with the closed bar's start time, and that
// indicator.Service.LastSnapshot returns the same timestamp.
func TestRunner_HTFSubscribeCallback_DispatchesAtClose(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 24, 9, 30, 0, 0, loc)
	const sym = domain.Symbol("AAPL")
	const tfHTF = domain.Timeframe("5m")

	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	idx := indicator.NewService("subscribe_callback")
	r := strategy.NewRunner(bus, router, "test-tenant", envMode, nil, strategy.WithIndicator(idx))

	var hits []time.Time
	fs := newFakeStrategy("htf_subscribe_strat", "1.0.0")
	fs.onBarFunc = func(_ start.Context, _ string, bar start.Bar, st start.State) (start.State, []start.Signal, error) {
		hits = append(hits, bar.Time)
		return st, nil, nil
	}
	id, _ := start.NewInstanceID("htf_subscribe_strat:1.0.0:" + sym.String())
	inst := strategy.NewInstance(id, fs, nil, strategy.InstanceAssignment{
		Symbols:    []string{sym.String()},
		Timeframes: []string{"5m"},
		Priority:   100,
	}, start.LifecycleLiveActive, nil)
	tctx := newTestCtx()
	if err := inst.InitSymbol(tctx, sym.String(), nil); err != nil {
		t.Fatalf("init symbol: %v", err)
	}
	router.Register(inst)
	r.InitAggregators(anchor)

	bars := indicatortest.MakeBars(sym, 200.0, anchor, 10)
	for i, bar := range bars {
		if err := r.HandleBarDirectTyped(context.Background(), bar, "test-tenant", envMode); err != nil {
			t.Fatalf("HandleBarDirectTyped bar %d: %v", i, err)
		}
	}

	if len(hits) != 2 {
		t.Fatalf("expected 2 OnBar fires (one per 5m close across 10 1m bars), got %d", len(hits))
	}
	wantFirst := anchor
	if !hits[0].Equal(wantFirst) {
		t.Fatalf("first close bar time: got %v want %v", hits[0], wantFirst)
	}
	wantSecond := anchor.Add(5 * time.Minute)
	if !hits[1].Equal(wantSecond) {
		t.Fatalf("second close bar time: got %v want %v", hits[1], wantSecond)
	}

	idxLast, ok := idx.LastSnapshot(sym, tfHTF)
	if !ok {
		t.Fatalf("indicator.LastSnapshot missing for %s/%s after run", sym, tfHTF)
	}
	if !idxLast.Time.Equal(wantSecond) {
		t.Fatalf("indicator.LastSnapshot Time: got %v want %v", idxLast.Time, wantSecond)
	}
}

// TestRunner_InitAggregators_UnsubscribesPriorCallbacks asserts re-init
// drops the previous Subscribe handles. Without that guard, repeated
// activations would fan out HTF closes to stale instances.
func TestRunner_InitAggregators_UnsubscribesPriorCallbacks(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY location: %v", err)
	}
	anchor := time.Date(2026, 4, 24, 9, 30, 0, 0, loc)
	const sym = domain.Symbol("AAPL")

	bus := memory.NewBus()
	router := strategy.NewRouter()
	envMode, _ := domain.NewEnvMode("paper")
	idx := indicator.NewService("subscribe_reinit")
	r := strategy.NewRunner(bus, router, "test-tenant", envMode, nil, strategy.WithIndicator(idx))

	var oldHits int
	oldFS := newFakeStrategy("old_strat", "1.0.0")
	oldFS.onBarFunc = func(_ start.Context, _ string, _ start.Bar, st start.State) (start.State, []start.Signal, error) {
		oldHits++
		return st, nil, nil
	}
	oldID, _ := start.NewInstanceID("old_strat:1.0.0:" + sym.String())
	oldInst := strategy.NewInstance(oldID, oldFS, nil, strategy.InstanceAssignment{
		Symbols: []string{sym.String()}, Timeframes: []string{"5m"}, Priority: 100,
	}, start.LifecycleLiveActive, nil)
	tctx := newTestCtx()
	if err := oldInst.InitSymbol(tctx, sym.String(), nil); err != nil {
		t.Fatalf("init symbol: %v", err)
	}
	router.Register(oldInst)
	r.InitAggregators(anchor)

	router.Unregister(oldID)

	var newHits int
	newFS := newFakeStrategy("new_strat", "1.0.0")
	newFS.onBarFunc = func(_ start.Context, _ string, _ start.Bar, st start.State) (start.State, []start.Signal, error) {
		newHits++
		return st, nil, nil
	}
	newID, _ := start.NewInstanceID("new_strat:1.0.0:" + sym.String())
	newInst := strategy.NewInstance(newID, newFS, nil, strategy.InstanceAssignment{
		Symbols: []string{sym.String()}, Timeframes: []string{"5m"}, Priority: 100,
	}, start.LifecycleLiveActive, nil)
	if err := newInst.InitSymbol(tctx, sym.String(), nil); err != nil {
		t.Fatalf("init symbol: %v", err)
	}
	router.Register(newInst)
	r.InitAggregators(anchor)

	bars := indicatortest.MakeBars(sym, 200.0, anchor, 10)
	for i, bar := range bars {
		if err := r.HandleBarDirectTyped(context.Background(), bar, "test-tenant", envMode); err != nil {
			t.Fatalf("HandleBarDirectTyped bar %d: %v", i, err)
		}
	}

	if oldHits != 0 {
		t.Fatalf("old strategy fired %d times after unregister + re-init; want 0", oldHits)
	}
	if newHits != 2 {
		t.Fatalf("new strategy fired %d times across 10 1m bars; want 2 (one per 5m close)", newHits)
	}
}
