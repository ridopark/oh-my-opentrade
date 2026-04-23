# discord-copytrade sidecar

Playwright-based Discord channel watcher that emits parsed trade signals to
omo-core. Runs in a container; the `copytrade_v1` strategy in omo-core owns
sizing, state, and execution.

## Layout

- `parser.py` — regex parser for `BTO|STC|AVG TICKER M/D STRIKE(C|P) @ PRICE` lines. Tested.
- `discord_dom.py` — DOM extraction helpers (breaks here if Discord changes selectors).
- `watcher.py` — polling loop that scrapes new messages and POSTs to omo-core.
- `bootstrap.py` — one-time visible-browser login flow.
- `emit.py` — HTTP client to `/internal/copytrade/signal`.
- `test_parser.py` — 40 unit tests covering sample + adversarial cases.

## One-time setup

1. Copy `.env.example` to `.env` and fill in:
   - `DISCORD_CHANNEL_URL` — full URL of the source channel.
   - `OMO_COPYTRADE_SECRET` — generate with `openssl rand -hex 32`. Must match the value
     configured in omo-core.
   - `OMO_BACKEND_URL` — defaults to `http://localhost:8080`.

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

## Recovering a dead session

Discord occasionally invalidates sessions. Signs: watcher logs
`storage_state.json missing` or `timeout waiting for chat-messages-*`.

Re-run the bootstrap step above — the saved state is overwritten and the
watcher resumes from `seen_ids.json`.

## DOM selector notes

If Discord ships a web rewrite and the watcher stops seeing messages,
inspect the DOM under `li[id^="chat-messages-"]` and update
`discord_dom.py`. The current selectors target:

- Message row: `li[id^="chat-messages-"]`
- Author: nearest preceding `h3 span[class*="username"]` (messages group)
- Timestamp: `time[datetime]`
- Content: `div[id^="message-content-"]`

## Safety notes

- The watcher does not log into Discord automatically. Credentials never
  leave the bootstrap browser window.
- The shared secret is the only thing standing between the sidecar and
  real paper orders. Rotate it if the `.env` file is ever exposed.
- `docker compose` is configured with `network_mode: host` on purpose so
  the sidecar can reach omo-core on localhost. This means the sidecar
  shares the host network namespace — do not expose additional ports.
