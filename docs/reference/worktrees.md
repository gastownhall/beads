---
title: Git Worktrees Guide
description: Using beads from Git worktrees, which share one .beads workspace, plus external BEADS_DIR setups and legacy sync-branch cleanup.
---

Beads works from normal Git worktrees without a separate sync branch. Current
beads stores issue data in Dolt under `refs/dolt/data`, so issue sync is
separate from Git branch commits.

## Current Model

All worktrees in the same repository use the same beads workspace unless you
override discovery with `BEADS_DIR`.

Startup *config* discovery — the `rev-parse` probe in `internal/config`'s
`gitDirsForRepo`, which locates a linked worktree's shared `.beads` config —
scrubs inherited Git routing variables such as `GIT_DIR` and `GIT_WORK_TREE`
from its subprocess environment whenever it runs, so a routed environment cannot
point that one lookup at another repository. The scrub is unconditional for that
probe, but the probe is a fallback, not a per-command step: it runs only when
nothing earlier in startup has already resolved a config path
(`internal/config/config.go`), so an ancestor `.beads/config.yaml` or an explicit
`BEADS_DIR` short-circuits it. It is a single probe, not a general guarantee:
other startup Git probes still honor inherited routing, including `internal/git`'s
process-cached repository context backing repository-root and hooks-directory
lookups and the `.beads` database discovery probes in `internal/beads`, so those
variables can still select which workspace `bd` opens. Scrubbing also drops any
inherited `GIT_CEILING_DIRECTORIES`, which *widens* upward discovery rather than
narrowing it: the explicit working directory fixes where that probe's search
starts, not where it stops, so the probe can select a containing parent
repository above that directory. Clearing those variables from the `bd` process
environment applies only to `bd worktree` commands.

`GIT_CEILING_DIRECTORIES` is scrubbed along with the redirects. That removes a
*bound* rather than a redirect, so discovery from the working directory is
widened, not narrowed: run in a directory that is not itself a repository, `bd`
can select a containing parent repository that an inherited ceiling would have
hidden.

```
project/
├── .git/                 # Shared Git directory
├── .beads/               # Shared beads config and local Dolt data
├── main-worktree/
└── feature-worktree/
```

Key points:

- `bd` discovers the repository's `.beads` directory from linked worktrees.
- Issue changes are stored in Dolt, not committed to the current Git branch.
- Cross-clone sync uses `bd dolt pull` and `bd dolt push`.
- No `sync.branch` or beads-managed Git worktree is required.

### Which `.beads` a worktree uses

From a linked Git worktree (or a jj secondary workspace), automatic discovery
checks these in order:

1. `BEADS_DIR`, when set, always wins.
2. A valid `.beads/redirect` file in the worktree.
3. The worktree's own `.beads`, when it holds a database (`embeddeddolt/`,
   `dolt/`, or a `*.db` file). This is separate-database mode.
4. The main repository's shared `.beads`.

Step 3 has one exception. A worktree database with no `metadata.json` (or
legacy `config.json`) beside it yields to the shared `.beads` when that
directory has readable `metadata.json`. `bd init` writes metadata, so an
initialized separate database keeps working; a store with none is usually left
behind by a command that fell back to embedded mode inside the worktree. The
first time this happens in a process, `bd` prints a warning to stderr naming
both directories:

```text
Warning: ignoring the beads database in /path/to/worktree/.beads because it has no metadata.json or config.json; using the shared workspace /path/to/main/.beads instead
  (embedded databases observed: beads; shared metadata declares dolt_database "myproject")
  To inspect the skipped database: BEADS_DIR='/path/to/worktree/.beads' bd list
  Preserve any data you need from it before removing it.
```

The names in the warning are what is on disk and what the shared metadata
declares; they do not prove the skipped database is empty. Discovery never
opens, changes, or deletes either database. The exception does not apply when
the worktree `.beads` has its own metadata (even metadata copied from the main
repository), when the shared `.beads` has no readable metadata, or when the
worktree is not linked to a shared workspace: those keep the worktree's
database, as before.

## Basic Usage

Initialize beads once in the repository:

```bash
cd project
bd init
```

Create linked worktrees normally:

```bash
git worktree add ../project-feature feature-branch
cd ../project-feature
bd ready
bd create "Implement feature X" -t feature -p 1
```

Sync issue data through the configured Dolt remote:

```bash
bd dolt pull
bd dolt push
```

## External Beads Workspace

If you want a separate issue-tracker repository shared by many code worktrees,
point `BEADS_DIR` at that workspace:

```bash
export BEADS_DIR=~/project-beads/.beads

cd ~/project/main       && bd list
cd ~/project/feature-1  && bd list
cd ~/project/feature-2  && bd list
```

With an external `BEADS_DIR`, `bd dolt push` and `bd dolt pull` target the
external beads workspace, not the code repository.

## Hooks

Git hooks installed by beads are worktree-aware. If hooks are stale or mention
removed legacy sync commands, refresh them:

```bash
bd hooks install
```

## Legacy Cleanup

Older beads versions had an experimental `sync.branch` workflow that created
hidden worktrees such as `.git/beads-worktrees/<branch>/`. That workflow has
been removed.

If a legacy checkout cannot switch branches because a beads-created worktree
still holds the branch, remove the stale worktree records:

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

If old config still contains a sync branch, clear it:

```bash
bd config set sync.branch ""
```

## Troubleshooting

### Database Not Found In A Worktree

Check that the main repository has a `.beads` directory and that the worktree
belongs to that repository:

```bash
git worktree list
cd /path/to/main/repo
ls -la .beads
```

If the repository has no beads workspace yet, run `bd init` from the main
repository.

### Multiple `.beads` Directories

If a worktree has its own accidental `.beads` directory, remove or archive the
extra copy after confirming it does not contain unique issue data. By default,
worktrees should share the repository workspace.

A worktree database without `metadata.json` is skipped in favor of the shared
workspace, with a warning (see
[Which `.beads` a worktree uses](#which-beads-a-worktree-uses)). Inspect it
with the command in the warning, for example
`BEADS_DIR='/path/to/worktree/.beads' bd list`, before removing it. Do not run
`bd init` in the worktree to repair it: in a linked worktree, `bd init` targets
the main repository's workspace.

If the worktree's own database was intentional, use `BEADS_DIR` to select it
explicitly.

### Concurrent Writers

For ordinary single-user worktree use, run commands directly. For true
multi-writer workflows across machines or agents, sync frequently with
`bd dolt pull` and `bd dolt push`, and coordinate through the tracker to avoid
working the same issue concurrently.

## See Also

- [Protected Branches](/reference/protected-branches) - protected branch behavior
- [Git Integration](/reference/git-integration) - general Git integration guide
- [Multi-Repo Migration Guide](/multi-agent/multi-repo-migration) - multi-workspace patterns
