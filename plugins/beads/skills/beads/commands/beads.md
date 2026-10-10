---
description: One-screen beads status — list, ready, blocked, stats, or a single issue
argument-hint: "[issue-id] | close <issue-id> <reason>"
---

Show a one-screen beads status view. Run `bd` from the repo containing `.beads/`.

With no arguments, run `bd list`, `bd ready`, `bd blocked`, and `bd stats`. Combine the
ready and blocked queues into one compact table (ID, title, priority, status), then a
counts line summarizing totals from `bd stats` (open, ready, blocked by status).

With an issue ID argument ($1), run `bd show <id>` and display the full issue —
description, status, priority, dependencies, and notes.

With `close <id> <reason>`, run `bd close <id> --reason "<reason>"`. Always include a
reason; never close bare. Then run `bd close --suggest-next` (or `bd ready`) and suggest
the next issue to pick up.

Never use `bd edit` — it opens $EDITOR. For field changes, use the beads MCP `update`
tool instead.
