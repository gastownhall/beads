---
title: Sync Setup Guide
description: "Set up Dolt sync so issue data follows you across machines: remotes, bootstrapping a clone, and day-to-day push and pull"
---

Set up beads with Dolt sync so your issues follow you across computers.

## Prerequisites

You need two tools installed on every machine:

| Tool | Version | Install |
|------|---------|---------|
| **bd** (beads CLI) | 0.59.0+ | See [Installation](/getting-started/installation) |
| **Dolt** | A specific pinned version, not "latest" | See [Which Dolt version to install](/architecture/dolt#which-dolt-version-to-install) |

Verify both are installed:

```bash
bd version     # must be 0.59.0+
dolt version   # must match the pinned version
```

Install a specific Dolt version rather than `releases/latest`. On Dolt 2.3.x a
few percent of freshly created databases come up with `CALL
DOLT_RESET('--hard')` broken for the life of the server process, which breaks
`bd flatten`, `bd admin compact` and the rollback behind `bd dolt pull`;
separately, the upstream `latest` URL can resolve to a *lower* version than
one released days earlier.
[Which Dolt version to install](/architecture/dolt#which-dolt-version-to-install)
has the measurements, the install command, and how to check a database you
already have.

## Initial Setup (First Computer)

### 1. Initialize beads

```bash
cd your-project
bd init
```

This creates the `.beads/` directory with a Dolt database. If the git repo has
an `origin` remote, `bd init` also configures a Dolt remote named `origin`
pointing at that same git URL. Dolt stores issue data under `refs/dolt/data`,
separate from normal source branches.

### 2. Create some issues

```bash
bd create "Set up CI pipeline" -p 1 -t task
bd create "Add authentication" -p 2 -t feature
bd list
```

### 3. Verify or add a Dolt remote

In a normal git repo with `origin`, this should already be configured:

```bash
bd dolt remote list
# Expected: origin  <your git origin URL>
```

If the repo had no `origin` during init, point beads at your Git remote for
sync:

```bash
# GitHub (SSH — recommended)
bd dolt remote add origin git+ssh://git@github.com/org/repo.git

# GitHub (HTTPS)
bd dolt remote add origin git+https://github.com/org/repo.git

# Other options: DoltHub, S3, GCS, local path
# See DOLT.md for all remote types
```

#### Keeping the data on another git ref

A git-backed remote keeps the issue data on one git ref of the repository,
`refs/dolt/data` by default. Two situations call for a different ref, and
`--ref` names it; only a full ref starting with `refs/` is accepted.

A git host that only accepts pushes under `refs/heads/` rejects
`refs/dolt/data` (the push fails with HTTP 403). Keep the data on a branch of
its own instead; that branch holds Dolt storage, not source code, so never
check it out or merge it. Name a branch that does not exist yet: the first
push replaces the branch's tip with Dolt storage, so bd refuses the
repository's default branch and asks before an existing branch (`--yes`
answers without a terminal). When the Dolt remote is the repository you are
working in, its URL matches your git origin, which `bd dolt remote add`
refuses unless you say so:

```bash
bd dolt remote add origin git+https://github.com/org/repo.git --ref refs/heads/beads-data --allow-git-origin
bd dolt push
```

A branch is visible to everything on the host that watches branches, which
`refs/dolt/data` never was. A workflow that triggers on pushes to all
branches runs on every `bd dolt push`. A ruleset or protection rule that
covers all branches blocks the push, and blocks `bd dolt remote reset-data`,
which deletes and force-pushes the branch. Every `git clone` and `git fetch`
of the repository transfers the data branch too, since the default refspec
covers all of `refs/heads/`, and stale-branch cleanup (automation or a
tidy colleague) can delete it. Scope such rules, triggers, and cleanups to
your code branches, and never make the data branch the default branch.

One repository can also hold several Dolt databases, each on its own ref, so
that one project carries one issue database per unit of work:

```bash
bd dolt remote add origin git+ssh://git@host/org/ledgers.git --ref refs/dolt/units/team-a
```

For the remote named `origin`, `bd dolt remote add --ref` also writes
`sync.remote-ref` to `.beads/config.yaml` next to `sync.remote`. Commit that
file: `bd bootstrap` and `bd init` on other clones read the key, look for
the data on that ref, and clone from it. `bd dolt remote list` shows the ref
of each remote, and `bd dolt remote reset-data` rebuilds the configured ref.
To go back to the default, re-add origin with `--ref refs/dolt/data`, which
clears the key (`bd bootstrap --ref refs/dolt/data` does the same for the
workspace it bootstraps). `bd dolt remote remove origin` keeps the key, so
removing and re-adding origin lands on the same ref. Re-adding a remote on a
different ref changes where its data is pushed, so bd asks first and, without
a terminal, requires `--yes`.

### 4. Push your issues

```bash
bd dolt push
```

Verify the push worked:

```bash
git ls-remote origin | grep dolt
# Expected: <hash>  refs/dolt/data
```

## Existing Projects Without a Dolt Remote

Projects initialized by older versions of `bd init` may have a local embedded
Dolt database and a committed `.beads/issues.jsonl`, but no Dolt remote. Fix
that from the machine whose local database is authoritative:

```bash
bd dolt remote list
bd export -o .beads/issues.pre-remote.jsonl   # optional issue audit export
bd dolt remote add origin git+ssh://git@github.com/org/repo.git
bd dolt push
```

`bd dolt remote add origin ...` writes `sync.remote` to `.beads/config.yaml`.
Commit and push that config file with your normal git workflow. Other clones can
then run `bd bootstrap` if their database is missing/stale, or `bd dolt pull`
when they already have the right database.

## Cloning to a New Computer

When you clone a repo that already has beads data on the remote, a standard `git clone` does **not** fetch `refs/dolt/data`. You need to bootstrap the Dolt database.

### Quick path: bd bootstrap

On recent versions of bd, `bd bootstrap` handles everything automatically:

```bash
git clone git@github.com:org/repo.git
cd repo

bd bootstrap
```

`bd bootstrap` auto-detects `refs/dolt/data` on origin, clones the Dolt database, and configures the remote. When the data lives on another ref (see [Keeping the data on another git ref](#keeping-the-data-on-another-git-ref)), bootstrap reads `sync.remote-ref` from the committed `.beads/config.yaml`; for a clone whose config does not carry the key yet, name the ref yourself and bootstrap persists it:

```bash
bd bootstrap --ref refs/heads/beads-data
```

Verify with:

```bash
bd list       # should show your issues
bd history    # should show recent issue history
```

If `bd bootstrap` succeeds, you're done — skip to [Day-to-day Sync](#day-to-day-sync).

### Manual path (if bootstrap fails)

If `bd bootstrap` doesn't work (older bd versions, unusual remote configs), follow these steps:

**Step 1: Confirm the remote has beads data**

```bash
git ls-remote origin | grep dolt
# Expected: <hash>  refs/dolt/data
# If missing, the remote has no beads data — use bd init normally.
```

**Step 2: Initialize beads**

```bash
bd init
```

This creates `.beads/` with an empty database. Ignore any warnings about `bd bootstrap` — we'll replace the empty database manually.

**Step 3: Stop the Dolt server**

```bash
bd dolt stop
```

**Step 4: Find your database name and remove the empty database**

```bash
# Check your database name
cat .beads/metadata.json    # look for "dolt_database"
```

The `dolt_database` field is your `<dbname>` (typically the repo name).

```bash
# Remove the empty database
rm -rf .beads/dolt/<dbname>/
```

**Step 5: Clone the Dolt data from the remote**

```bash
cd .beads/dolt
dolt clone git@github.com:org/repo.git <dbname>
cd ../..
```

**Step 6: Start the server and migrate**

```bash
bd dolt start
bd migrate --yes
```

**Step 7: Ensure the remote is registered**

```bash
bd dolt remote add origin git+ssh://git@github.com/org/repo.git
```

If you see "remote already exists", that's fine — `dolt clone` already set it up.

**Step 8: Verify**

```bash
bd dolt remote list   # should show origin
bd list               # should show your issues
```

## Day-to-day Sync

Once set up on both machines, sync is two commands:

```bash
# Push your changes to the remote
bd dolt push

# Pull changes from the remote
bd dolt pull
```

### Typical workflow

```
Machine A                          Machine B
─────────                          ─────────
bd create "New task" -p 1
bd dolt push
                                   bd dolt pull
                                   bd update bd-a1b2 --claim
                                   bd close bd-a1b2 --reason "Done"
                                   bd dolt push
bd dolt pull
bd list                            # sees the closed task
```

### Important rules

- **Always use `bd dolt ...` commands** — never run raw `dolt` CLI commands while the Dolt server is running. It causes journal corruption.
- **Commit before pulling** — if you have uncommitted working set changes, `bd dolt pull` will fail with "cannot merge with uncommitted changes". Run `bd dolt commit` first.
- **Push before switching machines** — unpushed changes only exist locally.
- **Do not use JSONL as sync** — `.beads/issues.jsonl` is an export for viewers and interchange. It is not the source of truth, not a full database backup, and cannot safely reconcile deletes or pruning.

## Troubleshooting

### "no common ancestor" on push

A stale `refs/dolt/data` from a previous database is conflicting. Clear it and retry:

```bash
git update-ref -d refs/dolt/data
bd dolt push
```

### "cannot merge with uncommitted changes" on pull

Commit your working set first:

```bash
bd dolt commit
bd dolt pull
```

### "no store available" on push or commit

This was a bug in bd < 0.59.0. Upgrade bd:

```bash
brew upgrade beads
# or re-run the install script
```

### bd list shows nothing after clone

The Dolt database wasn't bootstrapped. Either run `bd bootstrap` or follow the [manual path](#manual-path-if-bootstrap-fails) above.

### Stale lock files after crash

```bash
bd doctor --fix --yes
```

**WARNING**: Do NOT manually remove files inside `.dolt/` directories (including
`noms/LOCK`). These are Dolt-internal files and removing them **will cause
unrecoverable data corruption**. Dolt manages these files itself.

### "fatal: Unable to read current working directory"

The Dolt server's working directory no longer exists (common after branch switches). Restart it:

```bash
bd dolt stop
bd dolt start
```

## See Also

- [Sync Concepts](/core-concepts/sync-concepts) — The conceptual model behind this setup (why Dolt is the source of truth, what JSONL is for)
- [Quick Start](/getting-started/quickstart) — Getting started with beads
- [Dolt Backend for Beads](/architecture/dolt) — Dolt backend details, server modes, federation, remote types, and sync modes
- [Installation](/getting-started/installation) — Installation for all platforms

## Attribution

This guide was inspired by [@leonletto](https://github.com/leonletto)'s community setup guide at [leonletto.github.io/thrum](https://leonletto.github.io/thrum/docs.html#guides/beads-setup.html), which documented the end-to-end setup and sync process including the manual bootstrap workflow. Thanks for contributing to the beads community!
