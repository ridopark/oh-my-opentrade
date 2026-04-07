#!/usr/bin/env bash
# Post-commit review hook for Claude Code
# Runs after every Bash(git commit*) via PostToolUse hook.
#
# Exit 0 + JSON stdout = feedback to Claude
# The script checks if the commit should be reviewed and tells Claude
# to spawn the post-commit-reviewer agent if needed.

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

# Get the last commit message
LAST_MSG=$(git log -1 --pretty=format:"%s" 2>/dev/null || echo "")

# Skip review if commit message contains [skip-review]
if echo "$LAST_MSG" | grep -qF '[skip-review]' 2>/dev/null; then
  exit 0
fi

# Get commit info for context
COMMIT_SHA=$(git log -1 --pretty=format:"%h" 2>/dev/null || echo "unknown")
COMMIT_MSG=$(git log -1 --pretty=format:"%s" 2>/dev/null || echo "unknown")
FILES_CHANGED=$(git diff --name-only HEAD~1..HEAD 2>/dev/null | head -20 | tr '\n' ', ' || echo "")

# Return JSON telling Claude to run the post-commit-reviewer agent
cat <<EOF
{
  "decision": "allow",
  "hookSpecificOutput": {
    "hookEventName": "PostToolUse",
    "additionalContext": "POST-COMMIT REVIEW TRIGGERED for commit ${COMMIT_SHA} (${COMMIT_MSG}). Files changed: ${FILES_CHANGED}. Please spawn the 'post-commit-reviewer' agent to review this commit. If the reviewer returns VERDICT: NEEDS_FIX, ask the user if they want to spawn the 'code-fixer' agent to apply the fixes."
  }
}
EOF
exit 0
