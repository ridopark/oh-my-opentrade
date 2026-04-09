---
name: live-ops
description: "Live trading operations harness. Monitors omo-core health, detects bugs, and orchestrates autonomous investigation → analysis → review → fix pipeline. Use when the user says /live-ops, monitor live, watch trading, or ops check."
---

# Live Trading Operations Harness

Monitor the live trading system and autonomously detect, investigate, and fix issues.

## Quick Reference

- **Single check**: `/live-ops`
- **Continuous**: `/loop 10m /live-ops`
- **Emergency**: Automatic shutdown on 10%+ daily loss

---

## Step 0: Open Tmux Split for Live Visibility

Split the Claude Code terminal so the user can watch omo-core logs side-by-side.

**Important**: Claude Code runs in `claude-session`, NOT in `omo-core`. All tmux commands must target `claude-session`.

```bash
# Create a horizontal split showing omo-core logs (right pane, 80 cols)
tmux split-window -h -t claude-session:0 -l 80 "tail -f /home/ridopark/src/oh-my-opentrade/logs/omo-core.log" 2>/dev/null || true
```

When the pipeline completes (healthy or after handling), clean up:

```bash
# Close the log tail split pane
tmux kill-pane -t claude-session:0.1 2>/dev/null || true
```

---

## Step 1: Run Health Check

```bash
/home/ridopark/src/oh-my-opentrade/scripts/ops-monitor.sh
```

**If exit code 0 (no output)**: System is healthy. Report "All clear" and stop.

**If exit code 1**: Capture stdout lines. Each line is `SEVERITY|summary|detail`. Clean up stale workspace files, then continue to Step 2.

```bash
rm -f _workspace/incident_report.md _workspace/fix_plan.md _workspace/review_verdict.md
```

---

## Step 2: Check for EMERGENCY

Scan the monitor output for lines starting with `EMERGENCY`.

**If EMERGENCY found** (catastrophic loss >= 10% of equity):

Execute these commands immediately, in order, with no delay:

```bash
# 1. Force-liquidate all positions
/home/ridopark/src/oh-my-opentrade/scripts/force-liquidate.sh
```

```bash
# 2. Shutdown omo-core
/home/ridopark/src/oh-my-opentrade/scripts/shutdown.sh
```

```bash
# 3. Send critical Discord alert
/home/ridopark/src/oh-my-opentrade/scripts/discord-notify.sh \
  "EMERGENCY SHUTDOWN" \
  "Daily loss >= 10% of account equity. All positions liquidated. omo-core stopped. MANUAL RESTART REQUIRED." \
  red
```

**STOP HERE.** Do not investigate, do not attempt fixes. Human must review and manually restart.

---

## Step 3: Send Discord Notification

For non-emergency issues, notify Discord that issues were detected:

```bash
/home/ridopark/src/oh-my-opentrade/scripts/discord-notify.sh \
  "Live Ops: Issues Detected" \
  "[paste the monitor output lines here]" \
  yellow
```

---

## Step 4: Investigate

Spawn an agent to investigate. It must read the ops-investigator instructions first:

```
Agent(
  prompt: "You are the ops-investigator for the oh-my-opentrade live trading system.
Read your full instructions from /home/ridopark/src/oh-my-opentrade/.claude/agents/ops-investigator.md first, then follow them.

Investigate these live trading issues detected by ops-monitor.sh:

[paste monitor output lines here]

Write your findings to /home/ridopark/src/oh-my-opentrade/_workspace/incident_report.md"
)
```

Wait for the agent to complete and write `_workspace/incident_report.md`.

---

## Step 5: Route by Classification

Read `_workspace/incident_report.md` and check the **Classification** field.

### If NOT `CODE_BUG`:

Handle operationally based on classification:

| Classification | Action |
|---|---|
| `PROCESS_CRASH` | Run `scripts/start.sh` to restart, notify Discord |
| `FEED_DOWN` | Notify Discord, wait for auto-reconnect (built-in) |
| `ORDER_STUCK` | Cancel stuck orders via API: `curl -X DELETE localhost:8080/api/portfolio/orders/{id}`, notify Discord |
| `RISK_BREACH` | Notify Discord only. **NEVER auto-override risk controls.** |
| `DATA_ISSUE` | Notify Discord for manual review |

After handling, **STOP**. Do not proceed to the fix pipeline.

### If `CODE_BUG`:

Continue to Step 6.

---

## Step 6: Analyze & Plan Fix

Spawn an agent to analyze the bug and create a fix plan:

```
Agent(
  model: "opus",
  prompt: "You are the ops-analyst for the oh-my-opentrade live trading system.
Read your full instructions from /home/ridopark/src/oh-my-opentrade/.claude/agents/ops-analyst.md first, then follow them.

Read /home/ridopark/src/oh-my-opentrade/_workspace/incident_report.md and analyze the CODE_BUG.
Research the codebase to find the exact bug and create a minimal fix plan.
Write your plan to /home/ridopark/src/oh-my-opentrade/_workspace/fix_plan.md"
)
```

Wait for `_workspace/fix_plan.md` to be written.

**Send Discord summary of the bug and proposed fix:**

Read `_workspace/incident_report.md` and `_workspace/fix_plan.md`, then compose a concise summary (3-5 sentences) covering: what the bug is, what component is affected, and what the proposed fix is. Send it:

```bash
/home/ridopark/src/oh-my-opentrade/scripts/discord-notify.sh \
  "Bug Found — Fix Plan Created" \
  "[compose summary here: what the bug is, affected component, proposed fix]" \
  yellow
```

---

## Step 7: Review Fix Plan

Spawn an agent to review the fix for safety:

```
Agent(
  model: "opus",
  prompt: "You are the ops-reviewer for the oh-my-opentrade live trading system.
Read your full instructions from /home/ridopark/src/oh-my-opentrade/.claude/agents/ops-reviewer.md first, then follow them.

Review the proposed fix for the live trading system.
Read /home/ridopark/src/oh-my-opentrade/_workspace/incident_report.md and /home/ridopark/src/oh-my-opentrade/_workspace/fix_plan.md.
Verify the fix is correct, safe, and minimal.
Write your verdict to /home/ridopark/src/oh-my-opentrade/_workspace/review_verdict.md"
)
```

Wait for `_workspace/review_verdict.md` to be written.

---

## Step 8: Check Verdict

Read `_workspace/review_verdict.md` and check the **VERDICT** field.

| Verdict | Action |
|---|---|
| `REJECTED` | Notify Discord with reason. **STOP.** Human reviews. |
| `NEEDS_CHANGES` | Notify Discord with feedback. **STOP.** Human decides. |
| `APPROVED` | Continue to Step 9. |

---

## Step 9: Apply Fix

Spawn an agent to apply the fix, commit, and restart:

```
Agent(
  prompt: "You are the ops-fixer for the oh-my-opentrade live trading system.
Read your full instructions from /home/ridopark/src/oh-my-opentrade/.claude/agents/ops-fixer.md first, then follow them.

Apply the approved fix to the live trading system.
Read /home/ridopark/src/oh-my-opentrade/_workspace/fix_plan.md and /home/ridopark/src/oh-my-opentrade/_workspace/review_verdict.md.
Apply the code change, build, test, commit with [ops-fix][skip-review], then restart via scripts/shutdown.sh && scripts/start.sh."
)
```

Wait for the agent to complete.

**Send Discord notification that the fix was applied:**

Read `_workspace/fix_plan.md` for the problem statement and file changed. Compose a concise message (2-3 sentences) covering: what was fixed, what file was changed, and that the service was restarted.

```bash
/home/ridopark/src/oh-my-opentrade/scripts/discord-notify.sh \
  "Ops Fix Applied" \
  "[compose summary: what was fixed, file changed, service restarted]" \
  green
```

---

## Step 10: Verify Fix

Wait 2 minutes for the service to stabilize, then re-run the monitor:

```bash
sleep 120
/home/ridopark/src/oh-my-opentrade/scripts/ops-monitor.sh
```

**If exit 0**: Fix verified. Notify Discord:
```bash
/home/ridopark/src/oh-my-opentrade/scripts/discord-notify.sh \
  "Fix Verified" \
  "Issue resolved. System healthy after ops-fix." \
  green
```

**If exit 1 with same issue**: Fix did not resolve the problem. Notify Discord:
```bash
/home/ridopark/src/oh-my-opentrade/scripts/discord-notify.sh \
  "Fix Did Not Resolve" \
  "Issue persists after ops-fix. Manual intervention required." \
  red
```

**STOP.** Do not retry — a second automated fix attempt risks making things worse.

---

## Workspace Cleanup

After the pipeline completes (success or failure), the `_workspace/` files remain for human review:
- `_workspace/incident_report.md`
- `_workspace/fix_plan.md`
- `_workspace/review_verdict.md`

These are overwritten on the next `/live-ops` run.
