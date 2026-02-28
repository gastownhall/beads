#!/bin/bash
# Block cp/mv/rm without -f flag to prevent interactive prompt hangs.
#
# Problem: macOS shell profiles often alias cp/mv/rm with -i (interactive),
# causing AI agents to hang indefinitely waiting for y/n input they can't
# provide. Observed in multiple polecats during swarm operations.
#
# Solution: Block these commands unless -f flag or alias-bypass is present.
# The agent can easily retry with -f added.

COMMAND=$(jq -r '.tool_input.command' < /dev/stdin)

for cmd in cp mv rm; do
  # Match: cmd at word boundary, followed by space and arguments
  # Skip if: -f flag present, or using 'command' builtin / absolute path / backslash
  if echo "$COMMAND" | grep -qE "(^|[;&|] *)${cmd} " && \
     ! echo "$COMMAND" | grep -qE "(^|[;&|] *)${cmd} +-[a-eg-zA-Z]*f" && \
     ! echo "$COMMAND" | grep -qE "(command +${cmd}|/bin/${cmd}|/usr/bin/${cmd})" && \
     ! echo "$COMMAND" | grep -qE "(^|[;&|] *)\\\\${cmd} "; then
    jq -n --arg cmd "$cmd" '{
      hookSpecificOutput: {
        hookEventName: "PreToolUse",
        permissionDecision: "deny",
        permissionDecisionReason: ("BLOCKED: `" + $cmd + "` without `-f` flag may hang on interactive prompts (macOS often aliases `" + $cmd + "` to `" + $cmd + " -i`). Use `" + $cmd + " -f ...` instead, or prefix with `command " + $cmd + "` to bypass aliases.")
      }
    }'
    exit 0
  fi
done

exit 0
