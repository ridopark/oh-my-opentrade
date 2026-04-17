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

# Return JSON telling Claude to invoke /simplify then /review skills.
# Use jq -n to safely escape commit messages that contain quotes,
# backticks, or other JSON special characters.
jq -n \
  --arg sha "$COMMIT_SHA" \
  --arg msg "$COMMIT_MSG" \
  --arg files "$FILES_CHANGED" \
  '{
    hookSpecificOutput: {
      hookEventName: "PostToolUse",
      additionalContext: (
        "POST-COMMIT CHECKS TRIGGERED for commit " + $sha + " (" + $msg + "). " +
        "Files changed: " + $files + ". " +
        "Invoke the /simplify skill FIRST on this commit'"'"'s changes — it reviews for reuse, quality, and efficiency and applies fixes if any are found. " +
        "If /simplify produces a follow-up commit, that commit message MUST include [skip-review] to avoid re-triggering this hook. " +
        "AFTER /simplify finishes, invoke the /review skill on the final commit for structured review feedback (read-only; no fix commit). " +
        "If either skill would produce a risky or destructive change, pause and ask the user before applying."
      )
    }
  }'
exit 0
