---
title: "bd close"
description: "Close one or more issues"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc close`.

Close one or more issues.

If no issue ID is provided, closes the last touched issue (from most recent
create, update, show, or close operation). This fallback only applies in
interactive sessions (stdin is a terminal); in scripts and agent sessions a
missing ID is an error, so a command built from an empty variable cannot
silently close an unrelated issue. Set BD_LAST_TOUCHED_FALLBACK=1 to allow
the fallback anywhere, or =0 to disable it entirely.

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
      --no-auto              With --continue, show next step but don't claim it
  -r, --reason string        Reason for closing
      --reason-file string   Read close reason from file (use - for stdin)
      --session string       Claude Code session ID (or set CLAUDE_SESSION_ID env var)
      --suggest-next         Show newly unblocked issues after closing
```
