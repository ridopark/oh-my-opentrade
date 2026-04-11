package main

import (
	"context"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/rs/zerolog"
)

// startWatchdogNotify wires the process into the systemd Type=notify
// watchdog protocol when — and only when — systemd has set WatchdogSec in
// the unit file. On Docker or bare invocations (SdWatchdogEnabled returns
// 0) the function logs once and returns; nothing else in the system is
// affected.
//
// Why we want this: if omo-core deadlocks or silently hangs (not a hard
// crash — a wedged goroutine that stops servicing the event loop), there
// is no external supervisor that notices. restart: unless-stopped in
// docker-compose only catches process exits, and Kubernetes-style liveness
// probes are not yet wired. systemd's watchdog fills that gap for bare-
// metal deployments: if we stop sending the heartbeat, systemd SIGKILLs
// the process and restarts it on the next tick of Restart=on-failure.
//
// The heartbeat is currently unconditional — it fires as long as the
// goroutine is alive. A future improvement tracked in SPRINT_2_PLAN is to
// gate the heartbeat on feed-age < N seconds so a stuck bar pipeline also
// trips the watchdog.
func startWatchdogNotify(ctx context.Context, log zerolog.Logger) {
	interval, err := daemon.SdWatchdogEnabled(false)
	if err != nil {
		log.Warn().Err(err).Msg("systemd watchdog check failed; heartbeat disabled")
		return
	}
	if interval == 0 {
		log.Info().Msg("systemd watchdog not active (Docker or bare run); skipping heartbeat")
		return
	}

	heartbeat := interval / 2
	log.Info().Dur("heartbeat_interval", heartbeat).Msg("systemd watchdog enabled")

	if _, err := daemon.SdNotify(false, daemon.SdNotifyReady); err != nil {
		log.Warn().Err(err).Msg("systemd SdNotify(READY) failed")
	}

	go func() {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_, _ = daemon.SdNotify(false, daemon.SdNotifyStopping)
				return
			case <-ticker.C:
				if _, err := daemon.SdNotify(false, daemon.SdNotifyWatchdog); err != nil {
					log.Warn().Err(err).Msg("systemd watchdog heartbeat failed")
				}
			}
		}
	}()
}
