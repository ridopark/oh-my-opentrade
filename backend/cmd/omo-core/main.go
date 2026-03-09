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
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("received signal, shutting down")

	// Graceful shutdown — close WebSocket FIRST so Alpaca receives an RFC6455
	// close frame and immediately releases the session slot. Only then cancel the
	// context so the stream goroutine and services can exit cleanly.
	if svc.orchestrator != nil {
		svc.orchestrator.Stop()
	}
	if err := infra.alpacaAdapter.Close(); err != nil {
		log.Error().Err(err).Msg("error closing Alpaca adapter")
	}
	cancel() // Cancel context to stop all services
	svc.notifySvc.Stop()
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
