# Beads v0.53.0 — 11,000 Lines Deleted, Zero Features Lost

Beads v0.53.0 is the release where the sync pipeline finally gets out of the way. The entire JSONL intermediary layer is gone, replaced by native Dolt push/pull through git remotes. The result is a smaller, faster, more reliable `bd` with fewer moving parts to break.

## The Big Change: Dolt-in-Git Replaces the JSONL Pipeline

Since v0.50, Dolt has been the default backend. But syncing between repos still went through an awkward dance: export to JSONL, commit to a git sync branch, push, pull on the other side, import back into Dolt. It worked, but it was ~11,000 lines of worktree management, snapshot diffing, 3-way merge logic, deletion tracking, and git hook plumbing.

All of that is gone. `bd sync` now calls Dolt's native `DOLT_PUSH` and `DOLT_PULL` stored procedures directly through git remotes. Your Dolt database travels inside your git repo the same way it always did, but without the JSONL translation layer in between.

What was removed:
- `internal/syncbranch/` -- 5,720 lines of worktree management
- `snapshot_manager`, `deletion_tracking`, and the 3-way merge engine
- Doctor sync-branch checks and fixes
- Legacy daemon infrastructure (lockfile activity signals, orchestrator)
- The dead `bd repair` command

Manual `bd export` and `bd import` remain available as escape hatches.

## New: First-Class Dolt Server Commands

Managing the Dolt server used to mean knowing the right incantation. Now there are proper commands:

- **`bd dolt start` / `bd dolt stop`** -- explicit server lifecycle management
- **`bd dolt commit`** -- commit your Dolt data without dropping into SQL
- **Server mode without CGO** -- `OpenFromConfig()` is now exported, so server-mode connections work on pure-Go builds

## New: Hosted Dolt Support

Beads now supports connecting to Hosted Dolt instances with TLS, authentication, and explicit branch configuration. If your team runs a shared Dolt server, `bd` can talk to it directly.

## New: Storage Interface

The `Storage` interface decouples command logic from the concrete `DoltStore` implementation. This is groundwork for future flexibility -- testing with mock stores, alternate backends, or wrapping the store with middleware.

## Quality-of-Life Additions

- **`bd mol wisp gc --closed`** -- bulk purge closed wisps in one shot
- **`--no-parent` on `bd list`** -- filter out child issues to see only top-level work
- **`bd ready` pretty format** -- improved default output with priority sorting, truncation footer, and parent epic context
- **`bd compact`** -- Dolt database compaction support
- **Codecov integration** -- component-based coverage tracking in CI

## Important Fixes

**Pre-commit deadlock resolved.** If you hit hangs on `git commit` with embedded Dolt, this was a lock ordering issue in the hook path. Fixed in #1841/#1843.

**`bd doctor --fix` no longer hangs.** The fix subcommand was spawning a subprocess that competed for the same database lock. It now runs in-process.

**Dolt lock errors surfaced.** Previously, lock contention could produce silent empty results. Now you get an actionable error message telling you exactly what is stuck and how to fix it. `bd doctor` also detects stale `dolt-access.lock` and noms `LOCK` files.

**Other fixes:** `BEADS_DIR` respected in config loading, `Unknown database` retry after `CREATE DATABASE`, Windows `Expand-Archive` module conflict resolved, `molecule` recognized as a core type, formula `VarDef` correctly distinguishes "no default" from `default=""`.

## Upgrade

```
brew upgrade bd
```

Or via the install script:

```
curl -fsSL https://beads.sh/install | sh
```

Existing projects need no migration -- the JSONL pipeline removal is purely internal. If you had a `sync.git-remote` configured, Dolt-in-Git will use it automatically.
