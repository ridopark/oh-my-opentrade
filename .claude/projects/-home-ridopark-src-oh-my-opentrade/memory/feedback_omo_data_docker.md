---
name: omo-data runs as Docker container
description: omo-data must run as a container to access IB Gateway for VIX — local process cannot connect to the Docker network
type: feedback
---

omo-data runs as a Docker container, not a local process. The reason is IBKR VIX fetching — IB Gateway runs in Docker and omo-data needs to be on the same Docker network to connect to it.

**Why:** When omo-data ran as a local process, it could not reach IB Gateway (which is a Docker container on the internal network). Moving omo-data into a container on the same compose network resolved this.

**How to apply:** Always rebuild and restart omo-data via `docker compose`. Never run omo-data as a local tmux process. Use `docker compose run --rm omo-data --run-once` for manual triggers.
