# Developer Instructions

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
