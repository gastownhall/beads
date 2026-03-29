#!/bin/bash
# Block commands that burn through GitHub API rate limits or violate workflow.
# The GitHub API allows 5000 requests/hour. `gh run watch` polls every 3
# seconds (1200 req/hr), and has repeatedly exhausted the quota during
# releases, blocking all crew members for up to an hour.

COMMAND=$(jq -r '.tool_input.command' < /dev/stdin)

if echo "$COMMAND" | grep -qE 'gh run watch|gh run list.*--watch'; then
  jq -n '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: "BLOCKED: gh run watch polls every 3s and burns through the 5000/hr GitHub API rate limit. Use `gh run view <run-id>` for a single status check, or `sleep 600 && gh run view <id>` to wait and check once."
    }
  }'
  exit 0
fi

# Block PR creation for crew/maintainers. Crew workers push directly to main.
# External contributors (beads.role=contributor or fork origin) need PRs.
if echo "$COMMAND" | grep -qE 'gh pr create'; then
  ROLE=$(git config --get beads.role 2>/dev/null || echo "")
  ORIGIN=$(git config --get remote.origin.url 2>/dev/null || echo "")

  # Allow if role is explicitly contributor
  if [ "$ROLE" = "contributor" ]; then
    exit 0
  fi

  # Allow if origin points to a fork (not the upstream repo)
  if [ -n "$ORIGIN" ] && echo "$ORIGIN" | grep -qvE 'steveyegge/beads'; then
    exit 0
  fi

  jq -n '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: "BLOCKED: Crew workers do NOT create PRs. Push directly to main with `git push`. PRs are for external contributors only. If you are reviewing an external PR, use fix-merge: checkout the PR, fix/rebase, merge to main, push, then close the PR."
    }
  }'
  exit 0
fi

exit 0
