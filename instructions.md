# Developer Instructions

## Last OpenCode Session
opencode -s ses_3472f60fdffeS3ASnzTaiZ08v2

## Git Worktrees & Multi-Agent Workflows

Git worktrees let you check out multiple branches simultaneously in separate directories — no stashing or switching required. This is especially useful for running parallel Claude Code agents on different tasks.

### Create a Worktree

```bash
git worktree add -b <branch-name> <folder-path> <source-branch>
```

Creates a new branch based on `<source-branch>` and checks it out in `<folder-path>`.

**Example:** Create a feature branch off `main` in a sibling folder:
```bash
git worktree add -b feat/stop-loss-manager ../oh-my-opentrade-stoploss origin/main
```

### Work in a Worktree

```bash
cd <folder-path>   # Move into the worktree directory
ls                  # Verify the code is there
claude              # Launch a Claude Code agent in this isolated environment
```

Each worktree is a fully independent working directory with its own branch, staged files, and index. You can run a separate Claude Code instance in each one for parallel development.

### Remove a Worktree

Once you've pushed your changes and no longer need the worktree:

```bash
git worktree remove <folder-path>
```

### Clean Up Stale Worktrees

If a worktree folder was manually deleted (e.g., `rm -rf`) but Git still tracks it:

```bash
git worktree prune
```

This removes references to any worktrees whose directories no longer exist on disk.

### Typical Multi-Agent Workflow

1. **Create worktrees** for each parallel task:
   ```bash
   git worktree add -b feat/task-a ../omo-task-a origin/main
   git worktree add -b feat/task-b ../omo-task-b origin/main
   ```

2. **Launch agents** in separate terminals:
   ```bash
   # Terminal 1
   cd ../omo-task-a && claude

   # Terminal 2
   cd ../omo-task-b && claude
   ```

3. **Merge and clean up** when done:
   ```bash
   cd /home/ridopark/src/oh-my-opentrade
   git merge feat/task-a
   git merge feat/task-b
   git worktree remove ../omo-task-a
   git worktree remove ../omo-task-b
   git worktree prune
   ```

## Tmux Quick Reference

### View Backend/Dashboard Logs

```bash
tmux attach-session -t omo-core        # backend logs
tmux attach-session -t omo-dashboard   # dashboard logs
tmux list-sessions                     # check sessions
```

### Detach from Tmux (without killing the process)

Press `Ctrl+B` then `D`

## Debugging & Troubleshooting

### IB Gateway VNC (visual debugging)

Connect to the IB Gateway container desktop to inspect settings, check market data subscriptions, or configure API permissions:

```bash
vncviewer localhost:5900
# Password: password
```

Only port 5900 (TigerVNC) is exposed — the browser-based viewer (6080) is not available in this setup.

Useful for: Configure → API → Precautions, checking entitlements, manual login issues.

### Grafana Logs (preferred)

All omo-core logs are streamed to Grafana via Loki. Use this as the primary debugging tool.

1. Open http://localhost:3001/explore
2. Select **Loki** from the datasource dropdown
3. Query: `{job="omo-core"}`

Useful LogQL filters:

```logql
{job="omo-core"} |= "ERR"                         # errors only
{job="omo-core"} |= "WARN"                        # warnings
{job="omo-core"} |= "AAPL"                        # specific symbol
{job="omo-core"} |= "order" |= "filled"           # order fills
{job="omo-core"} |= "component=execution"         # specific component
{job="omo-core"} |= "panic"                       # panics/crashes
```

Requires infra to be running (`./scripts/start-infra.sh`).

### Tmux Logs (fallback)

If Grafana is not running, attach to the tmux session directly:

```bash
tmux attach-session -t omo-core        # backend logs
tmux attach-session -t omo-dashboard   # dashboard logs
```

Detach with `Ctrl+B` then `D`.

### Log Level

The backend uses **zerolog**. Log level is controlled via the `LOG_LEVEL` env var (not the `log_level` field in `config.yaml`).

```bash
LOG_LEVEL=debug go run ./cmd/omo-core/
LOG_LEVEL=debug LOG_PRETTY=true go run ./cmd/omo-core/   # human-readable
```

Available levels: `trace` | `debug` | `info` (default) | `warn` | `error` | `fatal` | `panic`

---
