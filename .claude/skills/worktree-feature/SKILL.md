---
name: worktree-feature
description: Create a git worktree for a new feature branch. Use when the user asks to start work on a new feature in an isolated worktree, or phrases like 'new worktree', 'create worktree', 'worktree for feature', 'spin up a worktree'.
---

# Worktree Feature Branch

Create an isolated git worktree for a new feature branch, branching from the current HEAD.
Then open a fresh `opencode` session rooted in the new worktree.

## Constants

```
WORKTREE_BASE=/home/ridopark/src/omo-worktree
```

All worktrees land under this directory.

## Workflow

Execute these steps **in order**. Stop on any failure.

### Step 1: Get current state

```bash
# Confirm which branch you're branching from
git branch --show-current

# Confirm working tree is clean (stash or commit first if not)
git status
```

If the working tree has uncommitted changes, **stop and ask the user** whether to stash, commit, or proceed anyway.

### Step 2: Determine the feature name

Ask the user for the feature name if not already provided. The branch will be named `feature/<name>`.

- Use lowercase, hyphen-separated words (e.g., `order-risk-filter`)
- No spaces or special characters

### Step 3: Create the worktree base directory if needed

```bash
mkdir -p /home/ridopark/src/omo-worktree
```

### Step 4: Create the branch and worktree

```bash
FEATURE_NAME="<name>"   # Replace with actual feature name
BRANCH="feature/$FEATURE_NAME"
WORKTREE_PATH="/home/ridopark/src/omo-worktree/$FEATURE_NAME"

# Create the branch and worktree in one command
git worktree add -b "$BRANCH" "$WORKTREE_PATH"
```

Result:

```
~/src/
  oh-my-opentrade/                    ← main repo
  omo-worktree/
    order-risk-filter/                ← new worktree on feature/order-risk-filter
```

### Step 5: Verify

```bash
git worktree list
```

Expected output shows both the main worktree and the new one with the feature branch.

### Step 6: Open opencode in the new worktree

Use tmux to open a new window with `opencode` rooted at the worktree path, then switch to it:

```bash
FEATURE_NAME="<name>"
WORKTREE_PATH="/home/ridopark/src/omo-worktree/$FEATURE_NAME"

# Open a new tmux window named after the feature, running opencode in the worktree
tmux new-window -n "$FEATURE_NAME" "opencode $WORKTREE_PATH"

# Switch focus to the new window
tmux select-window -t "$FEATURE_NAME"
```

This launches a fresh `opencode` session in the correct directory. The current session remains open in its original window — the user can close it with `/quit` or `Ctrl+C`.

## Quick Reference

| Step | Command | Abort on failure? |
|------|---------|-------------------|
| Check status | `git status` | Ask user |
| Create base dir | `mkdir -p /home/ridopark/src/omo-worktree` | **Yes** |
| Create worktree | `git worktree add -b feature/<name> <path>` | **Yes** |
| Verify | `git worktree list` | Warn if missing |
| Open opencode | `tmux new-window -n <name> "opencode <path>"` | **Yes** |

## Cleanup (when feature is merged)

When work is done and the branch is merged:

```bash
# From main repo — remove the worktree
git worktree remove /home/ridopark/src/omo-worktree/<feature-name>

# Delete the branch
git branch -d feature/<name>

# Prune stale worktree refs (if manually deleted)
git worktree prune
```

## Important Notes

- Worktrees share the same `.git` directory — commits in the worktree are immediately visible in the main repo
- You **cannot** check out the same branch in two worktrees simultaneously
- The worktree directory is outside the main repo — no `.gitignore` needed
- All project commands (`go build`, `npm run dev`, etc.) work normally from the worktree directory
- The new `opencode` session opens with the worktree as its project root — `AGENTS.md`, skills, and configs are inherited from the `.git`-linked repo
