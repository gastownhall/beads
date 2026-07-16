---
title: "bd close"
description: "Close one or more issues"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc close`.

Close one or more issues.

If no issue ID is provided, closes the last touched issue (from most recent
create, update, show, or close operation).

When closing multiple issues, provide one --reason for all IDs or repeat
--reason once per ID. Reasons map positionally: the first --reason applies
to the first ID, the second --reason to the second ID, regardless of where
the flags appear in the command line.

```
bd close [id...] [flags]
```

**Aliases:** done

**Flags:**

```
      --claim-next           Automatically claim the next highest priority available issue
      --continue             Auto-advance to next step in molecule
  -f, --force                Force close pinned issues or unsatisfied gates
      --if-assignee string   Only mutate while the issue is still assigned to this actor; empty asserts unassigned (mismatch: exit 9, unsupported path: exit 13)
      --if-fence int         Only mutate while claim_fence still equals this snapshot value (mismatch: exit 9, unsupported path: exit 13)
      --no-auto              With --continue, show next step but don't claim it
  -r, --reason string        Reason for closing
      --reason-file string   Read close reason from file (use - for stdin)
      --session string       Claude Code session ID (or set CLAUDE_SESSION_ID env var)
      --suggest-next         Show newly unblocked issues after closing
```
