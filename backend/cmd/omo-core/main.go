package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
)

func main() {
	log := initLogger()
	cfg := initConfig(log)
	infra := initInfra(cfg, log)
	svc := initCoreServices(cfg, infra, log)
	initStrategyPipeline(cfg, infra, svc, log)
	initMultiAccount(cfg, infra, svc, log)
	initDebateService(cfg, infra, svc, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startServices(ctx, cfg, infra, svc, log)
	server := initHTTPServer(ctx, cfg, infra, svc, log)
	syms := buildSymbolLists(cfg)
	warmupIndicators(ctx, cfg, infra, svc, syms, log)
	startStreaming(ctx, infra, svc, syms, log)
	waitForShutdown(cancel, server, infra, svc, log)
}

func waitForShutdown(cancel context.CancelFunc, server *http.Server, infra *infraDeps, svc *appServices, log zerolog.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("received signal, shutting down")

	// 1. Send shutdown notification FIRST (uses fresh context, independent of app lifecycle).
	svc.notifySvc.NotifySync("🛑 System Stopped: omo-core — shutting down (signal: " + sig.String() + ")")

	// 2. Stop orchestrator (no new trades).
	if svc.orchestrator != nil {
		svc.orchestrator.Stop()
	}

	// 3. Close WebSocket connections — waits up to 3s for RFC6455 close frames.
	if err := infra.broker.Close(); err != nil {
		log.Error().Err(err).Msg("error closing Alpaca adapter")
	}

	// 4. Cancel app context so stream goroutines and services exit.
	cancel()
	svc.barWriter.Close()
	svc.notifySvc.Stop()
	infra.eventBus.Close()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("HTTP server shutdown error")
	}
	if err := infra.sqlDB.Close(); err != nil {
		log.Error().Err(err).Msg("error closing DB connection")
	}
	if infra.tracerProvider != nil {
		if err := infra.tracerProvider.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("error shutting down tracer provider")
		}
	}
	log.Info().Msg("shutdown complete")
}
