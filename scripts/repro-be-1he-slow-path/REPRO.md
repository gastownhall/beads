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

**Historical framing, as originally diagnosed:** when `.local_version` contained
a stale bd version string, `autoMigrateOnVersionBump` fired in `PersistentPreRun`
on every `bd` command until the file was updated, opening the store writeable
and (at the time) calling `syncCLIRemotesToSQL → ListCLIRemotes → dolt remote -v`
against the Dolt server root. On a multi-database server installation, the server
root has `.dolt/sql-server.info` (written by `dolt sql-server`) but no
`.dolt/repo_state.json`. The `dolt remote -v` subprocess takes ~12 seconds to
fail with:

```
fatal: The current directory's repository state is invalid.
open .dolt/repo_state.json: no such file or directory
```

`syncCLIRemotesToSQL` and `migrateServerRootRemotes` no longer exist on
current `main` — that writeable-open path was separately reworked around
`doltutil.PersistedRemotes`, a fast on-disk `repo_state.json` read with no
`dolt` subprocess. `ListCLIRemotes` itself is still present and still shells
out to `dolt remote -v`, though: it's called from `cmd/bd/doctor/federation.go`
(federation health checks) and from CLI push/pull/fetch remote routing in
`internal/storage/doltutil/remotes.go`. Those call sites are what be-1he's
Layer 2 timeout protects.

## Reproducing the 12-second hang manually

```bash
# 1. Create the broken server root structure
TMPDIR=$(mktemp -d)
mkdir -p "$TMPDIR/.dolt"
echo '[{"host":"127.0.0.1","port":3307}]' > "$TMPDIR/.dolt/sql-server.info"
# Note: no repo_state.json

# 2. Observe the raw, uncapped subprocess — the underlying dolt slowness
#    this repro targets. This calls dolt directly, not through bd's
#    ListCLIRemotes, so nothing here caps it.
cd "$TMPDIR"
time dolt remote -v  # takes ~12 s to fail
cd -

# 3. Observe how bd scopes its Layer 2 cap: absence of repo_state.json is
#    what selects the aggressive 2s timeout inside ListCLIRemotes (present
#    → 30s, generous, so a slow-but-valid answer is never mistaken for
#    "remote absent").
ls "$TMPDIR/.dolt/repo_state.json" 2>/dev/null || echo "absent → bd's ListCLIRemotes uses the 2s cap here"
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

Historically (pre-fix, and before the unrelated `PersistedRemotes` rework on
`main`): `bd version` could take ~12 s when the migration fired and called
`dolt remote -v` on the server root.

With be-1he's two shipped layers: `ListCLIRemotes` (still reachable from
doctor federation checks and CLI remote routing, not from the ordinary
auto-migrate path on current `main`) caps that same call at 2 s against a
directory like this one; `bd version` itself returns in < 1 s regardless,
since Layer 3's read-only probe avoids the writeable open when no migration
is needed.

## Two layers of the fix

| Layer | File | What it does |
|-------|------|-------------|
| 2 | `internal/storage/doltutil/remotes.go` | `ListCLIRemotes` wraps `dolt remote -v` in `context.WithTimeout`: 2 s when the target directory lacks `repo_state.json` (this repro's case), 30 s otherwise — a slow-but-valid answer from a real repo is never mistaken for "remote absent" |
| 3 | `cmd/bd/version_tracking.go` | `autoMigrateOnVersionBump` does a read-only `bd_version` probe before opening the store writeable, skipping an unnecessary `initSchema` round-trip when no migration is needed |

An earlier draft of this fix also described a "Layer 1" sentinel in
`internal/storage/dolt/federation.go` (`migrateServerRootRemotes` stat-checking
`repo_state.json` before calling `ListCLIRemotes`). That never shipped in
be-1he — `federation.go` has no hunk in this PR's diff — and is not part of
what this repro demonstrates.
