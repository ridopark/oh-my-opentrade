#!/usr/bin/env bash
# Post-commit harness learning hook for Claude Code
# Runs after every Bash(git commit*) via PostToolUse hook.
# Only triggers on strategy tuning related commits.
#
# Analyzes the session for lessons learned and suggests updates to:
# - .claude/agents/strategy-tuner.md
# - .claude/skills/strategy-tuning/SKILL.md

# Read hook input JSON from stdin
INPUT=$(cat || true)

# Check if the commit actually succeeded
STDOUT=$(echo "$INPUT" | jq -r '.tool_response.stdout // empty' 2>/dev/null || true)
if echo "$STDOUT" | grep -qiE '(error|fatal|nothing to commit|no changes)' 2>/dev/null; then
  exit 0
fi

# Get the last commit message
LAST_MSG=$(git log -1 --pretty=format:"%s" 2>/dev/null || echo "")

# Only trigger for strategy tuning related commits
if ! echo "$LAST_MSG" | grep -qiE '(tune|strategy|harness|swing.?stop|backtest|param.?change|engine.?change|regime|confluence|PF [0-9])' 2>/dev/null; then
  exit 0
fi

# Skip if commit message contains [skip-learn]
if echo "$LAST_MSG" | grep -qF '[skip-learn]' 2>/dev/null; then
  exit 0
fi

# Get commit details
COMMIT_SHA=$(git log -1 --pretty=format:"%h" 2>/dev/null || echo "unknown")
COMMIT_MSG=$(git log -1 --pretty=format:"%B" 2>/dev/null || echo "unknown")
FILES_CHANGED=$(git diff --name-only HEAD~1..HEAD 2>/dev/null | head -30 | tr '\n' ', ' || echo "")
DIFF_STAT=$(git diff --stat HEAD~1..HEAD 2>/dev/null | tail -1 || echo "")

# Check if harness files were already updated in this commit
if echo "$FILES_CHANGED" | grep -qE '(strategy-tuner\.md|strategy-tuning/SKILL\.md)' 2>/dev/null; then
  exit 0
fi

# Return JSON telling Claude to analyze for harness updates
cat <<EOF
{
  "decision": "allow",
  "hookSpecificOutput": {
    "hookEventName": "PostToolUse",
    "additionalContext": "HARNESS LEARNING TRIGGERED for strategy commit ${COMMIT_SHA}.\n\nCommit message:\n${COMMIT_MSG}\n\nFiles changed: ${FILES_CHANGED}\nDiff stats: ${DIFF_STAT}\n\nPlease analyze this commit and the session context for lessons learned that should be captured in the strategy tuning harness. Check if any of these warrant an update to .claude/agents/strategy-tuner.md or .claude/skills/strategy-tuning/SKILL.md:\n\n1. New bug classes discovered (like counter-based bar counting)\n2. New parameter interaction effects (like slot backfill)\n3. New tuning priorities or ordering insights\n4. New strategy-specific notes (parameter ranges, correlated pairs)\n5. New operational lessons (rebuild/restart gotchas, TOML parsing issues)\n6. Updated parameter guides with tested ranges and results\n\nIf there are learnings worth capturing, update the relevant files. If not, say 'No new harness learnings from this commit' and move on. Do NOT update if the lesson is already documented."
  }
}
EOF
exit 0
