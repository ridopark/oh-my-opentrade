package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/alpaca"
	"github.com/oh-my-opentrade/backend/internal/adapters/eventbus/memory"
	"github.com/oh-my-opentrade/backend/internal/app/execution"
	"github.com/oh-my-opentrade/backend/internal/app/ingestion"
	"github.com/oh-my-opentrade/backend/internal/app/monitor"
	"github.com/oh-my-opentrade/backend/internal/config"
	"github.com/oh-my-opentrade/backend/internal/ports"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Println("omo-core: starting...")

	// 1. Load configuration
	cfg, err := config.Load(".env", "configs/config.yaml")
	if err != nil {
		log.Fatalf("omo-core: failed to load config: %v", err)
	}
	log.Println("omo-core: config loaded")

	// 2. Initialize event bus
	eventBus := memory.NewBus()
	log.Println("omo-core: event bus initialized")

	// 3. Initialize Alpaca adapter (MarketDataPort + BrokerPort + QuoteProvider)
	alpacaAdapter, err := alpaca.NewAdapter(cfg.Alpaca)
	if err != nil {
		log.Fatalf("omo-core: failed to create Alpaca adapter: %v", err)
	}
	log.Println("omo-core: Alpaca adapter initialized")

	// 4. TimescaleDB repository placeholder
	// TODO: Initialize pgx connection pool and create repository
	// For now, repo is nil — services will panic on first DB operation
	log.Println("omo-core: WARNING: TimescaleDB not connected (needs pgx dependency)")
	var repo ports.RepositoryPort // nil placeholder

	// 5. Initialize application services
	zscoreFilter := ingestion.NewZScoreFilter(20, 4.0) // 20-bar rolling window, 4σ threshold
	ingestionSvc := ingestion.NewService(eventBus, repo, zscoreFilter)

	monitorSvc := monitor.NewService(eventBus, repo)

	riskEngine := execution.NewRiskEngine(cfg.Trading.MaxRiskPercent)
	slippageGuard := execution.NewSlippageGuard(alpacaAdapter) // Adapter implements QuoteProvider
	killSwitch := execution.NewKillSwitch(
		cfg.Trading.KillSwitchMaxStops,
		cfg.Trading.KillSwitchWindow,
		cfg.Trading.KillSwitchHaltDuration,
		time.Now,
	)
	executionSvc := execution.NewService(
		eventBus,
		alpacaAdapter, // BrokerPort
		repo,
		riskEngine,
		slippageGuard,
		killSwitch,
		100000.0, // TODO: fetch account equity from broker API or config
	)

	// 6. Start services (subscribe to event bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ingestionSvc.Start(ctx); err != nil {
		log.Fatalf("omo-core: failed to start ingestion: %v", err)
	}
	if err := monitorSvc.Start(ctx); err != nil {
		log.Fatalf("omo-core: failed to start monitor: %v", err)
	}
	if err := executionSvc.Start(ctx); err != nil {
		log.Fatalf("omo-core: failed to start execution: %v", err)
	}
	log.Println("omo-core: all services started")

	// 7. TODO: Start Alpaca WebSocket streaming in a goroutine
	// Disabled until Docker Compose provides valid config
	log.Println("omo-core: ready (WebSocket streaming disabled until deployment)")

	// 8. Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("omo-core: received %v, shutting down...", sig)

	// 9. Graceful shutdown
	cancel() // Cancel context to stop all services
	if err := alpacaAdapter.Close(); err != nil {
		log.Printf("omo-core: error closing Alpaca adapter: %v", err)
	}
	// TODO: Close DB connection pool when implemented
	log.Println("omo-core: shutdown complete")
}
