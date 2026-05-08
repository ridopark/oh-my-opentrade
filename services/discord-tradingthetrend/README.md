# discord-tradingthetrend sidecar

Playwright-based Discord channel watcher for the TradingTheTrend morning
watchlist. Emits parsed entry signals to omo-core's
`/internal/tradingthetrend/signal` endpoint. The `tradingthetrend_v1`
strategy in omo-core owns the break-and-retest entry filter, OCC contract
construction, sizing, and mechanical exits.

## Layout

- `parser.py` — regex parser for `TICKER STRIKE[c|p] > TRIGGER` lines.
- `watcher.py` — polling loop that scrapes new messages and POSTs to omo-core.
- `emit.py` — HTTP client to `/internal/tradingthetrend/signal`.
- `scrape_history.py` — one-shot historical channel scraper for backtest input.
- `test_parser.py` — unit tests covering sample + adversarial cases.

DOM extraction (`discord_common.discord_dom`) and the one-time login flow
(`discord_common.bootstrap`) live in `services/discord_common/` and are
shared with the discord-copytrade sidecar.

## One-time setup

1. Copy `.env.example` to `.env` and fill in:
   - `DISCORD_CHANNEL_URL` — full URL of the TradingTheTrend channel.
   - `OMO_TRADINGTHETREND_SECRET` — generate with `openssl rand -hex 32`.
     Must match the value configured in omo-core
     (`OMO_TRADINGTHETREND_SECRET` env var).
   - `OMO_BACKEND_URL` — defaults to `http://host.docker.internal:8080`.

2. Build the container:

   ```
   docker compose build
   ```

3. Log in once (visible browser via X forwarding on Linux workstation):

   ```
   xhost +local:docker
   docker compose --profile bootstrap run --rm bootstrap
   xhost -local:docker
   ```

   Log into Discord (including 2FA) in the Chromium window that appears.
   Navigate to the target channel. When messages are visible, press Enter
   in the terminal to save `state/storage_state.json` and exit.

## Normal operation

```
docker compose up -d
docker compose logs -f watcher
```

The watcher seeds the last ~50 messages as "already seen" on startup (so
historical messages are not re-fired) and only emits for messages posted
after that point. State is persisted to `./state/`:

- `storage_state.json` — Discord session cookies.
- `seen_ids.json` — message IDs already processed.
- `heartbeat` — touched each tick; healthcheck fails if stale >120s.

## Historical scrape (for backtest)

```
docker compose --profile scrape run --rm scrape --days 90 --out /app/state/history.jsonl
```

Writes JSONL sorted oldest-first. Re-run the scrape ~7 days later and diff
the two outputs to detect post deletions (per the strategy pre-register).

## Recovering a dead session

Discord occasionally invalidates sessions. Signs: watcher logs
`storage_state.json missing` or `timeout waiting for chat-messages-*`.

Re-run the bootstrap step above — the saved state is overwritten and the
watcher resumes from `seen_ids.json`.

## DOM selector notes

If Discord ships a web rewrite and the watcher stops seeing messages,
inspect the DOM under `li[id^="chat-messages-"]` and update
`services/discord_common/discord_dom.py`. Both this sidecar and
discord-copytrade share the same selectors.

## Safety notes

- The watcher does not log into Discord automatically. Credentials never
  leave the bootstrap browser window.
- The shared secret is the only thing standing between the sidecar and
  real paper orders. Rotate it if the `.env` file is ever exposed.
- `docker compose` is configured with `network_mode: host` on the bootstrap
  and scrape profiles so they can reach localhost. The watcher uses
  `host.docker.internal` via `WSL_HOST_IP` to reach omo-core.
