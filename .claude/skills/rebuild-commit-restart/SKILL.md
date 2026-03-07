---
name: rebuild-commit-restart
description: Rebuild the backend, commit changes, and restart all services. Use when the user asks to deploy local changes, rebuild and restart, or ship what's been working on. Triggers on phrases like 'rebuild', 'restart', 'ship it', 'deploy local', 'rebuild and commit', 'RCR'.
---

# Rebuild, Commit & Restart

Full cycle: rebuild backend, commit changes, shutdown and restart services.

## Workflow

Execute these steps **in order**. Stop on any failure.

### Step 1: Rebuild backend

```bash
cd backend && go build -o bin/omo-core ./cmd/omo-core
```

Verify the build succeeds (exit code 0) before proceeding. If the build fails, **stop here** — do not commit broken code.

### Step 2: Commit changes

**Load and follow the `git-commit-helper` skill for this step.** It defines the commit message format, conventions, and workflow. Defer entirely to that skill for staging, message authoring, and committing.

### Step 3: Shutdown running services

```bash
./scripts/shutdown.sh
```

This gracefully stops `omo-core` and `omo-dashboard` tmux sessions. Monitoring stack (Grafana, Prometheus, Loki) is left running.

### Step 4: Restart all services

```bash
./scripts/start.sh
```

This rebuilds the binary, kills stale processes on ports 8080/8000, and starts `omo-core` + `omo-dashboard` in tmux sessions with logging.

### Step 5: Verify services are running

```bash
tmux has-session -t omo-core 2>/dev/null && echo "omo-core: running" || echo "omo-core: NOT running"
tmux has-session -t omo-dashboard 2>/dev/null && echo "dashboard: running" || echo "dashboard: NOT running"
```

Both services must be running. If either failed, check logs:

```bash
tail -20 logs/omo-core.log
```

## Quick Reference

| Step | Command | Abort on failure? |
|------|---------|-------------------|
| Build | `cd backend && go build -o bin/omo-core ./cmd/omo-core` | **Yes** |
| Commit | *(per git-commit-helper skill)* | **Yes** |
| Shutdown | `./scripts/shutdown.sh` | No (warn if already stopped) |
| Restart | `./scripts/start.sh` | **Yes** |
| Verify | `tmux has-session -t omo-core` | Report status |

## Important Notes

- All commands run from the project root: `/home/ridopark/src/oh-my-opentrade`
- The `start.sh` script also rebuilds the binary, so the binary is always fresh on restart
- Shutdown leaves the monitoring stack (Docker Compose) running intentionally
- Logs are written to `logs/omo-core.log`
