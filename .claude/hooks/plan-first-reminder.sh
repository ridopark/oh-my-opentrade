#!/usr/bin/env bash
# Plan-first reminder hook for Claude Code.
# Fires PreToolUse on Edit|Write|MultiEdit. Injects a short additionalContext
# string asking the assistant to confirm a plan exists before proceeding with
# non-trivial implementation. Non-blocking (no permissionDecision) — the
# assistant uses its judgment per the rule in CLAUDE.md.
#
# Hooks cannot read the conversation transcript, so they cannot verify a
# plan was actually shared. The real enforcement lives in CLAUDE.md; this
# hook exists to reinforce the rule on every edit call in case the rule
# slipped out of the model's attention.

jq -n '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    additionalContext: "PLAN-FIRST REMINDER: if this edit is part of a non-trivial implementation (>1 file, >~50 LOC, or touches execution/risk/control paths), confirm a plan was shared earlier in this turn (files, approach, tests, blast radius). If not, pause and write the plan before continuing. Single-line/typo/comment edits are exempt. See the CLAUDE.md plan-first rule."
  }
}'
