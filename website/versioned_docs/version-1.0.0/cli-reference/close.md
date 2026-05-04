---
id: close
title: bd close
slug: /cli-reference/close
sidebar_position: 60
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc close`

## bd close

Close one or more issues.

If no issue ID is provided, closes the last touched issue (from most recent
create, update, show, or close operation).

```
bd close [id...] [flags]
```

**Aliases:** done

**Flags:**

```
      --claim-next           Automatically claim the next highest priority available issue
      --continue             Auto-advance to next step in molecule
  -f, --force                Force close pinned issues or unsatisfied gates
      --no-auto              With --continue, show next step but don't claim it
  -r, --reason string        Reason for closing
      --reason-file string   Read close reason from file (use - for stdin)
      --session string       Claude Code session ID (or set CLAUDE_SESSION_ID env var)
      --suggest-next         Show newly unblocked issues after closing
```
