#!/usr/bin/env bash
# Post-commit Discord notification hook for Claude Code
# Runs after every Bash(git commit*) via PostToolUse hook.
# Sends commit summary to Discord using the existing discord-notify.sh script.

set -uo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
NOTIFY="$ROOT_DIR/scripts/discord-notify.sh"

# Read hook input JSON from stdin
INPUT=$(cat || true)

# Check if this Bash call was a git commit command
CMD=$(echo "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null || true)
if [ -z "$CMD" ]; then
  # Fallback: try .input.command
  CMD=$(echo "$INPUT" | jq -r '.input.command // empty' 2>/dev/null || true)
fi

if ! echo "$CMD" | grep -q 'git commit' 2>/dev/null; then
  exit 0
fi

# Skip if discord-notify.sh doesn't exist
[ -x "$NOTIFY" ] || exit 0

# Extract commit info
COMMIT_SHA=$(git log -1 --pretty=format:"%h" 2>/dev/null || echo "unknown")
COMMIT_MSG=$(git log -1 --pretty=format:"%s" 2>/dev/null || echo "unknown")
COMMIT_BODY=$(git log -1 --pretty=format:"%b" 2>/dev/null || echo "")
FILES_CHANGED=$(git diff --name-only HEAD~1..HEAD 2>/dev/null | wc -l || echo "0")
INSERTIONS=$(git diff --stat HEAD~1..HEAD 2>/dev/null | tail -1 | grep -oP '\d+ insertion' | grep -oP '\d+' || echo "0")
DELETIONS=$(git diff --stat HEAD~1..HEAD 2>/dev/null | tail -1 | grep -oP '\d+ deletion' | grep -oP '\d+' || echo "0")
BRANCH=$(git branch --show-current 2>/dev/null || echo "unknown")

# Build summary
TITLE="🚀 Commit \`${COMMIT_SHA}\` on \`${BRANCH}\`"
BODY="${COMMIT_MSG}"
if [ -n "$COMMIT_BODY" ]; then
  # Truncate body to 300 chars for Discord embed
  TRUNCATED=$(echo "$COMMIT_BODY" | head -5 | cut -c1-300)
  BODY="${BODY}

${TRUNCATED}"
fi
BODY="${BODY}

📁 **${FILES_CHANGED} files** | +${INSERTIONS} -${DELETIONS}"

# Send to Discord (foreground — background &>/dev/null & gets killed on hook exit)
"$NOTIFY" "$TITLE" "$BODY" "green" 2>/dev/null

exit 0
