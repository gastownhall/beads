# Beads v0.62.0 — Standalone & The Road to 1.0

**March 21, 2026**

Beads v0.62 is the release where beads becomes fully standalone. Gas Town-specific concepts have been systematically removed from the codebase — no more GUPP references, polecat terminology, HOP fields, or hardcoded `~/gt/` paths. Beads is beads. It works with Gas Town, but it doesn't need it.

The other headline: embedded Dolt support has made dramatic progress. DoltHub's coffeegoddd landed 73 commits advancing in-process Dolt — the last major gate before v1.0.0.

## The Road to v1.0.0

We're actively targeting Beads v1.0.0. The gate is embedded Dolt completion — running Dolt in-process without a separate server. This matters because standalone users (the majority) should get a zero-config experience: `bd init`, `bd create`, done. No server to manage.

The embedded Dolt work is making excellent progress. 73 commits from the Dolt team landed in this release alone, covering create, list, update, close, show, delete, search, query, label, gate, graph, views, and more. A shared `issueops` package now provides transaction-based operations used by both the server and embedded backends.

Once embedded Dolt covers the full command surface and standalone users report a smooth experience, we ship 1.0. We estimate this is weeks away, not months.

## Beads Is Now Standalone

The biggest internal change in v0.62 is the systematic removal of Gas Town-specific concepts:

- **GUPP references** removed from all code and docs
- **Polecat/crew/overseer terminology** replaced with generic types
- **HOP schema fields** removed from the database schema
- **Agent-as-bead subsystem** removed entirely
- **Patrol molecule references** removed from commands
- **Role templates** (deacon, witness, refinery) removed
- **Hardcoded `~/gt/` paths** replaced with config-driven paths
- **`BEADS_ACTOR`** is now the primary env var (`BD_ACTOR` remains as deprecated fallback)

Beads integrates with Gas Town through clean interfaces, not internal coupling. This separation is prerequisite for a credible 1.0 — beads should work identically whether you're running a full Gas Town, using it with another orchestrator, or just tracking issues solo.

## Custom Status Categories

Beads has always let you define custom statuses. Now you can assign them to categories that control behavior:

```bash
bd config set status.custom "in_review:active,qa_testing:wip,archived:done,on_hold:frozen"
```

- **active** statuses appear in `bd ready` (available for claiming)
- **wip** statuses are in-progress (shown in lists, excluded from ready)
- **done** statuses are excluded from `bd list` by default
- **frozen** statuses are excluded from both

The new `bd statuses` command lists all statuses with their icons and categories. `--json` for programmatic use.

## Azure DevOps Integration

The fifth tracker plugin: `bd ado sync`, `bd ado status`, `bd ado projects`. Follows the same pattern as GitHub, GitLab, Jira, and Linear — configure your ADO org and project, and work items sync bidirectionally. This was a community-requested integration driven by enterprise users.

## New Commands

**`bd note <id> <text>`** — Shorthand for appending notes without `bd update --note` ceremony. Small ergonomic win that adds up.

**`bd statuses`** — Lists all built-in and custom statuses with icons, categories, and descriptions.

## Audit Logging

Status, assignee, and priority changes are now logged to `.beads/interactions.jsonl`. Close reasons are captured in the audit trail. This log survives Dolt GC flatten — even if you aggressively compact your database history, the audit trail persists as a flat file.

## Dolt Reliability

- **ServerMode enum** consolidates Dolt server ownership inference into four clean modes (Auto, External, Shared, Embedded), replacing ad-hoc string checks
- **Windows lifecycle** — stale PID/port files cleaned up; false "failed to stop" warnings eliminated
- **Hook preservation** — `bd init` preserves ALL pre-existing git hooks, not just beads-managed ones
- **Shim timeout** increased from 30s to 300s (configurable via `BEADS_HOOK_TIMEOUT`) for chained hooks
- **Concurrent schema init** serialized with `GET_LOCK` to prevent journal corruption in multi-agent environments
- **External server safety** — `KillStaleServers` respects server ownership, won't kill externally-managed servers
- **Doctor infinite loop** — no longer triggers infinite Dolt server restart cycles

## Embedded Dolt Progress

73 commits from DoltHub engineer coffeegoddd. Commands now working in embedded mode: create, list, update, close, show, delete, search, query, label, gate, promote, move, merge-slot, quick, diff, count, find-duplicates, graph, dep, duplicate, epic, supersede, swarm, and views/reports. The `issueops` package extraction means both server and embedded backends share the same transactional logic.

This is the most active area of development and the primary gate for v1.0.0.

## Community

Contributors: coffeegoddd (Dustin Brown / DoltHub), matt wilkie (maphew), harry-miller-trimble, gzur, Algorune, sfncore, angelamayxie, paf0186, Patrick Farrell, Tim Visher.

## Upgrade

```bash
brew upgrade bd
# or
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

**Breaking change**: `BD_ACTOR` is deprecated in favor of `BEADS_ACTOR`. The old variable still works as a fallback but will be removed in a future release.

Full changelog: [CHANGELOG.md](CHANGELOG.md) | GitHub release: [v0.62.0](https://github.com/gastownhall/beads/releases/tag/v0.62.0)
