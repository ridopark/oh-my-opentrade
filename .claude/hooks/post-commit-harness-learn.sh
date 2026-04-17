#!/usr/bin/env bash
# Post-commit harness learning hook for Claude Code
# Runs after every Bash(git commit*) via PostToolUse hook.
# Analyzes commits for lessons learned and suggests updates to the
# relevant agents (.claude/agents/) and skills (.claude/skills/).
#
# Covers ALL harness domains — not just strategy tuning.

# Read hook input JSON from stdin
INPUT=$(cat || true)

# Only run for git commit commands (double-check — "if" filter should handle this)
CMD=$(echo "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null || true)
if ! echo "$CMD" | grep -q '^git commit' 2>/dev/null; then
  exit 0
fi

# Check if the commit actually succeeded
STDOUT=$(echo "$INPUT" | jq -r '.tool_response.stdout // empty' 2>/dev/null || true)
if echo "$STDOUT" | grep -qiE '(error|fatal|nothing to commit|no changes)' 2>/dev/null; then
  exit 0
fi

# Skip if commit message contains [skip-learn]
LAST_MSG=$(git log -1 --pretty=format:"%s" 2>/dev/null || echo "")
if echo "$LAST_MSG" | grep -qF '[skip-learn]' 2>/dev/null; then
  exit 0
fi

# Get commit details
COMMIT_SHA=$(git log -1 --pretty=format:"%h" 2>/dev/null || echo "unknown")
COMMIT_MSG=$(git log -1 --pretty=format:"%B" 2>/dev/null | tr '\n' ' ' | sed 's/  */ /g' || echo "unknown")
FILES_CHANGED=$(git diff --name-only HEAD~1..HEAD 2>/dev/null | head -30 | tr '\n' ', ' || echo "")
DIFF_STAT=$(git diff --stat HEAD~1..HEAD 2>/dev/null | tail -1 || echo "")

# Skip if ONLY harness files were changed (pure docs commit, no learnings to extract)
NON_HARNESS=$(git diff --name-only HEAD~1..HEAD 2>/dev/null | grep -cvE '\.claude/(agents|skills|hooks)/' || true)
if [ "$NON_HARNESS" = "0" ]; then
  exit 0
fi

# --- Detect which harness domains are relevant based on files changed and commit message ---
DOMAINS=""

# Strategy tuning
if echo "$FILES_CHANGED$LAST_MSG" | grep -qiE '(tune|strategy|backtest|param.?change|engine.?change|regime|confluence|PF [0-9]|avwap|orb_break|phm_power)' 2>/dev/null; then
  DOMAINS="${DOMAINS}strategy-tuning, "
fi

# Go backend / hexagonal architecture
if echo "$FILES_CHANGED" | grep -qiE '(backend/internal/|\.go,)' 2>/dev/null; then
  DOMAINS="${DOMAINS}go-backend, "
fi

# Frontend / dashboard
if echo "$FILES_CHANGED" | grep -qiE '(dashboard/|frontend/|\.tsx,|\.ts,|components/)' 2>/dev/null; then
  DOMAINS="${DOMAINS}frontend, "
fi

# TDD workflow
if echo "$FILES_CHANGED$LAST_MSG" | grep -qiE '(_test\.go|\.test\.|test.?fix|tdd|red.?green)' 2>/dev/null; then
  DOMAINS="${DOMAINS}tdd, "
fi

# Code review
if echo "$LAST_MSG" | grep -qiE '(review|code.?fix|lint|refactor)' 2>/dev/null; then
  DOMAINS="${DOMAINS}code-review, "
fi

# Monitoring / observability
if echo "$FILES_CHANGED$LAST_MSG" | grep -qiE '(monitor|loki|grafana|prometheus|alert|log)' 2>/dev/null; then
  DOMAINS="${DOMAINS}monitoring, "
fi

# Quant / risk / trading
if echo "$FILES_CHANGED$LAST_MSG" | grep -qiE '(quant|risk|trading|position|execution|broker|options|slippage)' 2>/dev/null; then
  DOMAINS="${DOMAINS}quant-trading, "
fi

# QA / integration
if echo "$LAST_MSG" | grep -qiE '(qa|integration|contract|mismatch|boundary)' 2>/dev/null; then
  DOMAINS="${DOMAINS}qa-integration, "
fi

# If no domain matched, skip
if [ -z "$DOMAINS" ]; then
  exit 0
fi

# Trim trailing comma
DOMAINS=$(echo "$DOMAINS" | sed 's/, $//')

# Build the list of potentially relevant harness files
HARNESS_FILES="Relevant agents/skills to consider updating:\n"
case "$DOMAINS" in
  *strategy-tuning*)
    HARNESS_FILES="${HARNESS_FILES}- .claude/agents/strategy-tuner.md\n- .claude/skills/strategy-tuning/SKILL.md\n- .claude/agents/quant-analyst.md\n- .claude/skills/backtest-analysis/SKILL.md\n" ;;
esac
case "$DOMAINS" in
  *go-backend*)
    HARNESS_FILES="${HARNESS_FILES}- .claude/agents/go-architect.md\n- .claude/skills/go-hexagonal/SKILL.md\n- .claude/agents/tdd-green.md\n- .claude/agents/tdd-refactor.md\n" ;;
esac
case "$DOMAINS" in
  *frontend*)
    HARNESS_FILES="${HARNESS_FILES}- .claude/agents/dashboard-dev.md\n- .claude/skills/senior-frontend/SKILL.md\n- .claude/skills/react-best-practices/SKILL.md\n" ;;
esac
case "$DOMAINS" in
  *tdd*)
    HARNESS_FILES="${HARNESS_FILES}- .claude/agents/tdd-red.md\n- .claude/agents/tdd-green.md\n- .claude/agents/tdd-refactor.md\n- .claude/skills/tdd-workflow/SKILL.md\n- .claude/skills/testing-patterns/SKILL.md\n" ;;
esac
case "$DOMAINS" in
  *code-review*)
    HARNESS_FILES="${HARNESS_FILES}- .claude/agents/code-reviewer.md\n- .claude/agents/post-commit-reviewer.md\n- .claude/agents/code-fixer.md\n- .claude/skills/code-reviewer/SKILL.md\n" ;;
esac
case "$DOMAINS" in
  *monitoring*)
    HARNESS_FILES="${HARNESS_FILES}- .claude/skills/monitor-omo-services/SKILL.md\n" ;;
esac
case "$DOMAINS" in
  *quant-trading*)
    HARNESS_FILES="${HARNESS_FILES}- .claude/agents/quant-analyst.md\n- .claude/agents/risk-manager.md\n" ;;
esac
case "$DOMAINS" in
  *qa-integration*)
    HARNESS_FILES="${HARNESS_FILES}- .claude/agents/qa-inspector.md\n" ;;
esac

# Return JSON telling Claude to analyze for harness updates.
# Use jq -n so that commit messages containing quotes, backticks, or
# other JSON special characters are escaped correctly. HEREDOC-style
# interpolation breaks here because commit bodies regularly contain
# literal `"` and `\` from code fences.
jq -n \
  --arg sha "$COMMIT_SHA" \
  --arg domains "$DOMAINS" \
  --arg msg "$COMMIT_MSG" \
  --arg files "$FILES_CHANGED" \
  --arg diffstat "$DIFF_STAT" \
  --arg harness "$HARNESS_FILES" \
  '{
    hookSpecificOutput: {
      hookEventName: "PostToolUse",
      additionalContext: (
        "HARNESS LEARNING TRIGGERED for commit " + $sha + ".\n" +
        "Detected domains: " + $domains + "\n\n" +
        "Commit message:\n" + $msg + "\n\n" +
        "Files changed: " + $files + "\n" +
        "Diff stats: " + $diffstat + "\n\n" +
        $harness + "\n" +
        "Please analyze this commit and the session context for lessons learned. Check if any of these warrant an update to the relevant agent or skill files:\n\n" +
        "1. New bug classes or gotchas discovered\n" +
        "2. New interaction effects or ordering dependencies\n" +
        "3. New workflow priorities or best practices\n" +
        "4. Updated guides with tested values or ranges\n" +
        "5. New operational lessons (rebuild, restart, config parsing, etc.)\n" +
        "6. Patterns that worked well and should be codified\n" +
        "7. Patterns that failed and should be warned against\n\n" +
        "If there are learnings worth capturing, update the relevant files. If not, say \"No new harness learnings from this commit\" and move on. Do NOT update if the lesson is already documented. Keep updates minimal and focused."
      )
    }
  }'
exit 0
