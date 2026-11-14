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

### Worktree stores without metadata yield to the shared workspace

When automatic discovery finds a worktree-local `.beads` that owns a database
directory (`embeddeddolt/`, `dolt/`, or a `*.db` file) but has **no
workspace metadata of its own** — no `metadata.json` and no legacy
`config.json` — and the shared workspace is itself recognized by readable
metadata, discovery selects the shared workspace and skips the local store.
Such a metadata-less store is usually accidental: a command whose embedded
fallback ran inside the worktree (for example `bd prime`) can leave an empty
embedded store there, and without this rule it would silently shadow the
repository's real database for every command.

The skip emits one warning per worktree store per `bd` process (stderr
only), naming the skipped and shared directories, the embedded database
names observed in the skipped store (a directory-marker probe, not a content
check), the shared workspace's declared `dolt_database`, and an explicit
inspection command:

```text
Warning: skipping the beads store at /path/worktree/.beads: it has no
metadata.json or config.json, so discovery cannot tell it from an
accidentally created store (GH#6552); using the shared workspace at
/path/main/.beads instead.
  ...
  To inspect the skipped store, run: BEADS_DIR='/path/worktree/.beads' bd list
```

Nothing is deleted, moved, initialized, or migrated; preserve any unique
data in the skipped store before removing the directory. Do not run
`bd init` inside the worktree as a repair — in a linked worktree that
targets the main repository.

The rule is conditional, and its limits matter:

- A worktree store **with its own metadata** — however it got there, and
  whether or not it names a `dolt_database` — is deliberate
  separate-database mode and keeps priority (see
  [Multiple `.beads` Directories](#multiple-beads-directories)).
- The yield never applies to explicit choices: `BEADS_DIR`, `BEADS_DB`, and
  a valid per-worktree `redirect` file all take precedence and produce no
  warning.
- A shared candidate that is not a recognized workspace (no readable
  metadata, or only project files) does not displace the local store, and a
  standalone repository — not a worktree — is never subject to the rule.
- Metadata is a routing signal, not proof of intent: a deliberately created
  metadata-less store beside a recognized shared workspace is routed to the
  shared one. To keep such a store local, give the worktree `.beads` its own
  `metadata.json` or point `BEADS_DIR` at it.

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

A worktree `.beads` with its own `metadata.json` (or legacy `config.json`) is
a separate-database workspace by design: discovery keeps selecting it, and it
does not warn. A worktree `.beads` that owns a database directory but no
metadata is skipped in favor of a recognized shared workspace, with the
warning described in
[Worktree stores without metadata yield to the shared workspace](#worktree-stores-without-metadata-yield-to-the-shared-workspace).

If a worktree has its own accidental `.beads` directory, remove or archive the
extra copy after confirming it does not contain unique issue data. By default,
worktrees should share the repository workspace.

### Concurrent Writers

For ordinary single-user worktree use, run commands directly. For true
multi-writer workflows across machines or agents, sync frequently with
`bd dolt pull` and `bd dolt push`, and coordinate through the tracker to avoid
working the same issue concurrently.

## See Also

- [Protected Branches](/reference/protected-branches) - protected branch behavior
- [Git Integration](/reference/git-integration) - general Git integration guide
- [Multi-Repo Migration Guide](/multi-agent/multi-repo-migration) - multi-workspace patterns
