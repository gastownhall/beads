# Beads v0.60.0 — The Shared Server Release

**March 12, 2026**

Beads v0.60 is an infrastructure and reliability release. The headline feature is shared Dolt server mode — multiple repos and agents can now share a single Dolt instance — but the real story is the dozens of fixes that make Dolt operations safe under concurrency. Port collisions are gone, metadata merge conflicts are auto-resolved, and journal corruption from concurrent server kills is prevented by design.

## Shared Dolt Server Mode

The most-requested infrastructure feature lands in v0.60. Previously, each project auto-started its own Dolt server on a hash-derived port. This worked for solo developers but created problems for multi-repo setups (port collisions) and agent workflows (resource waste from dozens of idle servers).

`bd init --server` now supports pointing multiple projects at a single Dolt instance. Each project gets its own database on the shared server, with full isolation. The shared server config flows through `BEADS_DOLT_*` environment variables or `config.yaml` settings.

Combined with this, Dolt port allocation has moved from deterministic hash-derived ports to OS-assigned ephemeral ports stored in a repo-local state file. The hash-derived scheme suffered from birthday-problem collisions as installations scaled; ephemeral ports eliminate this class of bug entirely.

## Bootstrap Gets Hands-Free

`bd bootstrap` previously printed a list of things you should do. Now it does them. When `bd doctor` detects a fixable problem — missing `metadata.json`, stale hook sidecars, project ID gaps — `bd bootstrap` executes the recovery actions directly. This matters most for agent workflows where printing advice to a terminal nobody reads is useless.

The new `bd context` command complements this with safe-first error guidance: when something goes wrong, it surfaces the relevant context so agents (and humans) can diagnose without guessing.

## New Commands and Flags

**`bd done`** is now an alias for `bd close`, and `bd done <id> <message>` treats the last argument as the close reason. This aligns with Gas Town's vocabulary — agents run `gt done` to finish sessions, and now `bd done` works the same way for beads.

**`bd help --list` and `bd help --doc`** produce machine-readable command listings and full documentation. Useful for generating docs, feeding to agents, or building tooling.

**`--design-file`** lets you pass design documents from files instead of piping through stdin. Community contribution from Matthew Endsley.

**`--destroy-token`** enables safe non-interactive re-initialization — you must provide the exact token to confirm destruction, preventing accidental data loss in scripts.

**`bd search`** now searches the `external_ref` field, so you can find beads by their GitHub issue URL or Linear ID.

## GitHub Issues Integration

A new tracker plugin syncs GitHub Issues with beads. This is the third tracker integration (after Linear and Jira) and follows the same plugin pattern: configure a repo, and issues flow bidirectionally. `bd` becomes your unified interface regardless of where your team tracks work.

## Global PRIME.md

`~/.config/beads/PRIME.md` is now a fallback when no project-level `PRIME.md` exists. If you use the same priming context across projects, write it once and it applies everywhere. Project-level files still take precedence.

## Epic Close Guards

Accidentally closing an epic with open children was a common footgun. v0.60 adds guards: closing an epic with open children now requires explicit confirmation. The close operation also shows a progress summary and handles merge re-parenting — if you close a parent, its children are re-parented to the grandparent instead of becoming orphans.

## 35+ Dolt Reliability Fixes

This release is dense with Dolt stability work:

- **Journal corruption prevention** — `KillStaleServers` now runs inside `flock`, preventing concurrent server kills from corrupting the Dolt journal
- **Config corruption** — explicit `DOLT_ADD` prevents config corruption from stale working set state
- **Pull safety** — pending changes are auto-committed before pull, and `DOLT_PULL` runs in an explicit transaction for autocommit compatibility
- **Metadata merge conflicts** — auto-resolved during `bd dolt pull` by moving auto-push state to a local file
- **Remote directory** — `bd dolt remote add/list/remove` now operate on the correct CLI directory
- **Endpoint drift detection** — warns when auto-started server endpoint doesn't match config
- **CLI remotes synced** — CLI-managed remotes are synced into the SQL server on store open

## Embedded Dolt Storage Interfaces

DoltHub engineer coffeegoddd contributed a new storage abstraction layer with interfaces, schema migrations, and tests. This is groundwork for potential future embedded Dolt support — running Dolt in-process without a separate server. The interfaces are internal and don't affect the public API, but they represent the first step toward making the storage layer pluggable.

## Legacy Cleanup

The final remnants of three removed subsystems are now gone:

- **Daemon infrastructure** — the last idle monitor and activity signal code has been removed
- **3-way merge engine** — leftover merge strategy code cleaned up
- **Sync mode scaffolding** — dead code from the JSONL era removed

The Charm library stack has been upgraded to v2: `glamour` (terminal markdown rendering) and `huh` (interactive forms) are both on their latest major versions.

## Community Contributions

This release includes work from 10+ external contributors. Matt Wilkie (maphew) contributed 11 commits: WSL/MINGW detection in the installer, stale doc purges across the entire codebase, and CI doc validation to catch stale references going forward. DoltHub's coffeegoddd delivered the embedded Dolt storage interfaces across 5 PRs. MelsovCOZY added global PRIME.md fallback. Matthew Endsley contributed the `--design-file` flag. Weselow added community fork listings.

## Upgrade

```bash
brew upgrade bd
# or
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

No breaking changes. If you're running multiple Dolt servers per machine, consider consolidating to a shared server with `bd init --server` for reduced resource usage.

Full changelog: [CHANGELOG.md](CHANGELOG.md) | GitHub release: [v0.60.0](https://github.com/steveyegge/beads/releases/tag/v0.60.0)
