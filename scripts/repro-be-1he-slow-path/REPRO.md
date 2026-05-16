# be-1he: 12-second slow path on bd commands (multi-DB server root repro)

## Environment

- `bd` binary from any version before be-1he fix
- `dolt` CLI in PATH (required by `ListCLIRemotes`)
- Multi-DB Dolt server root: `.beads/dolt/` has `.dolt/sql-server.info` but
  no `.dolt/repo_state.json`

## Running the repro

```bash
./scripts/repro-be-1he-slow-path/repro.sh
```

## What triggers the slow path

When `.local_version` contains a stale bd version string, `autoMigrateOnVersionBump`
fires in `PersistentPreRun` — on every `bd` command until the file is updated.
That migration path calls `syncCLIRemotesToSQL → ListCLIRemotes → dolt remote -v`
against the Dolt server root. On a multi-database server installation, the server
root has `.dolt/sql-server.info` (written by `dolt sql-server`) but no
`.dolt/repo_state.json`. The `dolt remote -v` subprocess takes ~12 seconds to
fail with:

```
fatal: The current directory's repository state is invalid.
open .dolt/repo_state.json: no such file or directory
```

## Reproducing the 12-second hang manually

```bash
# 1. Create the broken server root structure
TMPDIR=$(mktemp -d)
mkdir -p "$TMPDIR/.dolt"
echo '[{"host":"127.0.0.1","port":3307}]' > "$TMPDIR/.dolt/sql-server.info"
# Note: no repo_state.json

# 2. Observe the slow subprocess (the pre-fix behavior)
cd "$TMPDIR"
time dolt remote -v  # takes ~12 s to fail
cd -

# 3. Observe the fix: repo_state.json check prevents the call
ls "$TMPDIR/.dolt/repo_state.json" 2>/dev/null || echo "absent → ListCLIRemotes skipped"
```

## bd command sequence (to verify with a real workspace)

On a workspace running a multi-database Dolt server:

```bash
# Simulate stale .local_version (any different version string triggers it)
OLD_VERSION=$(cat .beads/.local_version)
echo "0.0.0" > .beads/.local_version

# Time a bd command (any command that goes through PersistentPreRun)
time bd version

# Restore
echo "$OLD_VERSION" > .beads/.local_version
```

Pre-fix: `bd version` takes ~12 s (the migration fires, calls `dolt remote -v`
on the server root).

Post-fix: `bd version` returns in < 1 s (Layer 1 sentinel skips the call;
Layer 2 would cap it at 2 s even if Layer 1 weren't there).

## Three layers of the fix

| Layer | File | What it does |
|-------|------|-------------|
| 1 | `internal/storage/dolt/federation.go` | `migrateServerRootRemotes` sentinel-stats `repo_state.json` before invoking `ListCLIRemotes`; absent → skip |
| 2 | `internal/storage/doltutil/remotes.go` | `ListCLIRemotes` wraps `dolt remote -v` in a `context.WithTimeout(2s)`; backstops any future variant |
| 3 | `cmd/bd/version_tracking.go` | `autoMigrateOnVersionBump` does a read-only probe before opening DB writeable; read-only store skips `syncCLIRemotesToSQL` entirely |
