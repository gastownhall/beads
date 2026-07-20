---
title: Cursor
description: Set up beads for Cursor with an always-applied project rules file
---

Use Beads with Cursor through a project rules file that keeps the workflow
guidance in front of the agent every turn.

```bash
bd setup cursor
bd setup cursor --check
```

`bd setup cursor` installs **`.cursor/rules/beads.mdc`** — an always-applied
rule with the canonical Beads workflow guidance (the same content every other
editor integration uses), so it stays in sync as the workflow evolves. Because
it is always applied, Cursor re-includes it every turn, including after a
context compaction.

Restart Cursor after installing so the rule loads.

## Verifying it works

```bash
bd setup cursor --check
```

In a session, the agent should know your bd workflow without being told — ask
it what work is ready and it should run `bd ready`. If it doesn't, confirm
`.cursor/rules/beads.mdc` exists and restart Cursor.

After a context compaction, have the agent run `bd prime` to restore the
workflow context, ready work, and project memories.

## Remove

```bash
bd setup cursor --remove
```

This removes the rules file while leaving the rest of `.cursor/` intact.

## Related

- [IDE Setup](/getting-started/ide-setup) — all editor integrations
- [Claude Code](/integrations/claude-code)
- [Codex](/integrations/codex)
