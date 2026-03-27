# Sync Setup Guide

Set up beads with Dolt sync so your issues follow you across computers.

## Prerequisites

You need two tools installed on every machine:

| Tool | Minimum Version | Install |
|------|-----------------|---------|
| **bd** (beads CLI) | 0.59.0+ | See [INSTALLING.md](INSTALLING.md) |
| **Dolt** | 1.81.8+ | `brew install dolt` or [dolt install script](https://github.com/dolthub/dolt/releases/latest/download/install.sh) |

Verify both are installed:

```bash
bd version     # must be 0.59.0+
dolt version   # must be 1.81.8+
```

## Initial Setup (First Computer)

### 1. Initialize beads

```bash
cd your-project
bd init
```

This creates the `.beads/` directory with a Dolt database and starts a background `dolt sql-server`.

### 2. Create some issues

```bash
bd create "Set up CI pipeline" -p 1 -t task
bd create "Add authentication" -p 2 -t feature
bd list
```

### 3. Add a Dolt remote

Point beads at your Git remote for sync. Dolt stores data under `refs/dolt/data`, separate from your normal Git refs — so you can use the same remote as your source code.

```bash
# GitHub (SSH — recommended)
bd dolt remote add origin git+ssh://git@github.com/org/repo.git

# GitHub (HTTPS)
bd dolt remote add origin https://github.com/org/repo.git

# Other options: DoltHub, S3, GCS, local path
# See DOLT-BACKEND.md for all remote types
```

### 4. Push your issues

```bash
bd dolt push
```

Verify the push worked:

```bash
git ls-remote origin | grep dolt
# Expected: <hash>  refs/dolt/data
```

## Cloning to a New Computer

When you clone a repo that already has beads data on the remote, a standard `git clone` does **not** fetch `refs/dolt/data`. You need to bootstrap the Dolt database.

### Quick path: bd bootstrap

On recent versions of bd, `bd bootstrap` handles everything automatically:

```bash
git clone git@github.com:org/repo.git
cd repo

bd bootstrap
```

`bd bootstrap` auto-detects `refs/dolt/data` on origin, clones the Dolt database, and configures the remote. Verify with:

```bash
bd list       # should show your issues
bd vc log     # should show commit history
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

If a noms LOCK file persists:

```bash
rm -f .beads/dolt/<dbname>/.dolt/noms/LOCK
```

### "fatal: Unable to read current working directory"

The Dolt server's working directory no longer exists (common after branch switches). Restart it:

```bash
bd dolt stop
bd dolt start
```

## See Also

- [QUICKSTART.md](QUICKSTART.md) — Getting started with beads
- [DOLT.md](DOLT.md) — Dolt backend details, server modes, federation
- [DOLT-BACKEND.md](DOLT-BACKEND.md) — Remote types, sync modes, advanced usage
- [INSTALLING.md](INSTALLING.md) — Installation for all platforms

## Attribution

This guide was inspired by [@leonletto](https://github.com/leonletto)'s community setup guide at [leonletto.github.io/thrum](https://leonletto.github.io/thrum/docs.html#guides/beads-setup.html), which documented the end-to-end setup and sync process including the manual bootstrap workflow. Thanks for contributing to the beads community!
