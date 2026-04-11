package main

import (
	"context"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/oh-my-opentrade/backend/internal/ports"
	"github.com/rs/zerolog"
)

// Feed-age thresholds for gating the systemd watchdog heartbeat.
//
// A bar pipeline can wedge without the process crashing — the goroutine
// that pumps bars stops making progress but the process stays alive, so a
// naive heartbeat loop keeps telling systemd everything is fine while the
// strategy runner is effectively offline. We want systemd to restart us in
// that case.
//
// But we must not trip the watchdog during legitimate off-hours quiet: no
// bars flow between 16:00 ET and 09:30 ET the next session, on weekends,
// or on market holidays. If we skipped heartbeats every time the feed was
// older than 90s we'd get killed at 16:01, restart, get killed again, and
// exhaust systemd's StartLimitBurst overnight.
//
// The compromise is a narrow "active window": only skip heartbeat when
// the feed has seen at least one bar AND the most recent bar is between
// 90s and 5 minutes old. During active trading a real wedge is caught
// within ~30s of crossing the 90s line; beyond 5 minutes we assume the
// session is closed and heartbeat normally, deferring to the Docker
// HEALTHCHECK / operator alerting for long outages.
const (
	watchdogFeedMaxAge      = 90 * time.Second
	watchdogFeedStaleCutoff = 5 * time.Minute
)

// shouldSkipHeartbeat returns (true, age) when the caller must NOT send a
// systemd watchdog notify this tick because the equity bar pipeline looks
// wedged. The decision is pure so it can be exhaustively unit-tested
// without standing up systemd or the ingestion service.
//
// Rules:
//   - lastBar zero   → never skip (pipeline not started yet / warmup).
//   - age ≤ 90s      → never skip (healthy feed).
//   - age ≥ 5 min    → never skip (assume off-hours quiet, don't trip the
//     watchdog overnight).
//   - 90s < age < 5m → SKIP heartbeat; a wedge is the only plausible cause
//     of stale-but-not-ancient bars during an active session.
func shouldSkipHeartbeat(lastBar, now time.Time) (bool, time.Duration) {
	if lastBar.IsZero() {
		return false, 0
	}
	age := now.Sub(lastBar)
	if age > watchdogFeedMaxAge && age < watchdogFeedStaleCutoff {
		return true, age
	}
	return false, age
}

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
// When pipelineHealth is non-nil the heartbeat additionally gates on
// equity-feed freshness so a wedged bar pipeline (process alive but not
// pumping bars) also trips the watchdog. See the thresholds above for
// the off-hours-safe detection window.
func startWatchdogNotify(ctx context.Context, log zerolog.Logger, pipelineHealth ports.PipelineHealthReporter) {
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
				var lastBar time.Time
				if pipelineHealth != nil {
					lastBar = pipelineHealth.LastProcessedAt("equity")
				}
				if skip, age := shouldSkipHeartbeat(lastBar, time.Now()); skip {
					log.Warn().
						Dur("feed_age", age).
						Msg("equity feed stale during active window — skipping watchdog heartbeat, systemd will restart")
					continue
				}
				if _, err := daemon.SdNotify(false, daemon.SdNotifyWatchdog); err != nil {
					log.Warn().Err(err).Msg("systemd watchdog heartbeat failed")
				}
			}
		}
	}()
}
