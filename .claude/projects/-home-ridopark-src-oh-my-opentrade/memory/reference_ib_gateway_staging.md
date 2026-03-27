---
name: IB Gateway Staging Setup
description: IB Gateway connection details, env vars, and troubleshooting for staging deployment
type: reference
---

IB Gateway runs on staging via Docker (`gnzsnz/ib-gateway:10.44`) in `deployments/docker-compose.yml`.
SSH access: `ssh ubuntu@100.117.52.34`

**Connection:** omo-core connects to `100.117.52.34:4002` (set via `IBKR_GATEWAY_HOST` env var).

**GitHub environment secrets (staging):**
- `TWS_USERID` — paper account login username (`staging100`), NOT the main `ridopark` login
- `TWS_PASSWORD` — password for the paper account login
- `IBKR_ACCOUNT_ID` — Paper account ID (`DU...` number), used by both IBC (`ACCOUNT` env var) for auto-selecting the account on multi-account dialog, and by omo-core for position filtering

**IBKR account structure:**
- Main login: `ridopark` — has multiple accounts, triggers multi-account selector
- Paper login: `staging100` — goes directly into paper trading, avoids account selector

**VNC access:** port 5900, password in `VNC_PASS` env var.

**Auto-restart:** IBC restarts gateway daily at 11:55 PM (`AUTO_RESTART_TIME`). `TWOFA_TIMEOUT_ACTION=restart` retries if 2FA times out. Paper mode (`TRADING_MODE=paper`) should not require 2FA.

**Common issues:**
- Multi-account selector dialog blocks auto-login — fixed by setting `ACCOUNT=${IBKR_ACCOUNT_ID}` in docker-compose, or by using the paper-specific login (`staging100`)
- Login screen stuck — check `TWS_USERID`/`TWS_PASSWORD` are set in `.env` (they come via `env_file`)
