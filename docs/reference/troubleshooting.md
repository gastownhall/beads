---
title: Troubleshooting
description: Fixes for common bd problems across installation, the database and Dolt server, sync, git hooks, dependencies, and platform-specific issues.
---

Common issues and solutions. For step-by-step runbooks, see the
[Recovery section](/recovery/index).

## Installation Issues

### `bd: command not found`

```bash
# Check if installed
which bd
go list -f {{.Target}} github.com/steveyegge/beads/cmd/bd

# Add Go bin to PATH (add to ~/.bashrc or ~/.zshrc)
export PATH="$PATH:$(go env GOPATH)/bin"

# Or reinstall with the recommended installer
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash
```

### Wrong version of bd running

If `bd version` shows an unexpected version (e.g., older than what you just
installed), you likely have multiple `bd` binaries in your PATH:

```bash
# Check all bd binaries in PATH
which -a bd

# Example output showing conflict:
# /Users/you/go/bin/bd        <- From go install (older)
# /opt/homebrew/bin/bd        <- From Homebrew (newer)

# Remove the old go install version
rm ~/go/bin/bd

# Or remove mise-managed Go installs
rm ~/.local/share/mise/installs/go/*/bin/bd

# Verify
which bd
bd version
```

This happens when a binary from an earlier `go install` sits in `~/go/bin/`
ahead of a newer package-manager install. Choose one installation method
(Homebrew recommended) and stick with it.

### `zsh: killed bd` on macOS

CGO/SQLite compatibility issue:

```bash
CGO_ENABLED=1 GOFLAGS=-tags=gms_pure_go go install github.com/steveyegge/beads/cmd/bd@latest

# Or if building from source
git clone https://github.com/gastownhall/beads
cd beads
CGO_ENABLED=1 go build -tags gms_pure_go -o bd ./cmd/bd
sudo mv bd /usr/local/bin/
```

Homebrew builds already enable CGO, so this shouldn't be necessary there. If
you still see crashes with the Homebrew version, please
[file an issue](https://github.com/gastownhall/beads/issues).

### Permission denied

```bash
chmod +x $(which bd)

# Or install to a user directory instead
mkdir -p ~/.local/bin
mv bd ~/.local/bin/
export PATH="$HOME/.local/bin:$PATH"
```

### Antivirus flags bd as malware

Kaspersky, Windows Defender, and others sometimes flag `bd` as a generic
trojan. This is a **false positive** — Go binaries commonly trigger antivirus
heuristics. Verify the binary's SHA256 checksum against the
[GitHub release page](https://github.com/gastownhall/beads/releases) before
adding an exclusion. See [Antivirus False Positives](/reference/antivirus) for
per-vendor instructions.

## Database Issues

### Database not found

```bash
# Initialize beads
bd init --quiet

# Or point bd at an existing .beads directory
BEADS_DIR=/path/to/.beads bd list
```

### Database locked

```bash
# Stop the Dolt server if running (server mode)
bd dolt stop

# Find and kill hanging bd processes
ps aux | grep bd
kill <pid>

# Try again
bd list
```

<Warning>
Do NOT remove files inside `.dolt/` directories (including `noms/LOCK`).
These are Dolt-internal files — removing them WILL cause unrecoverable data
corruption. Dolt manages these files itself.
</Warning>

For high-concurrency scenarios (multiple agents), server mode
(`bd init --server`) handles concurrent access natively via `dolt sql-server`.

### `bd init` refuses to run

`bd init` and `bd dolt` refuse operations that could destroy local or remote
history, printing a pattern code such as `init-local-exists` or
`pk-fork-refused`. Each code has a runbook — see
[Recovery Playbooks](/recovery/init-safety). Export first
(`bd export -o backup.jsonl`) if you intend to re-initialize over existing
data.

### Corrupted database

Distinguish **logical consistency issues** (ID collisions, wrong prefixes)
from **physical database corruption** (disk failures, power loss, filesystem
errors).

For logical consistency issues — this is not corruption:

```bash
bd doctor --fix
```

For physical corruption, rebuild from a Dolt remote or a backup:

```bash
# Move the damaged data directory aside:
mv .beads/embeddeddolt .beads/embeddeddolt.backup   # embedded mode (default)
mv .beads/dolt .beads/dolt.backup                   # server mode

bd init
bd dolt pull    # Pull from Dolt remote if configured

# Or restore from a backup:
# bd backup restore [path] --force
```

See [Database Corruption](/recovery/database-corruption) for the full runbook.

### Dolt journal corruption after restart

**Symptom (server mode):** After a system restart, `bd` reports that the Dolt
server started but is not accepting connections, and `.beads/dolt-server.log`
contains:

```text
possible data loss detected in journal file at offset ...: corrupted journal
```

**Cause:** Dolt detected damaged journal blocks after an unclean shutdown.
This is not the same as a stale PID, stale port, or stale lock file. `bd`
will not run Dolt's data-loss repair mode automatically.

**Safe recovery when your remote is current:**

```bash
# Server mode data lives at .beads/dolt; embedded mode at .beads/embeddeddolt
mv .beads/dolt .beads/dolt.corrupt.$(date +%Y%m%dT%H%M%S)
bd bootstrap --dry-run
bd bootstrap --yes
bd stats
```

If the remote may be stale, keep the corrupt directory for forensics and
inspect it with `dolt fsck` before considering
`dolt fsck --revive-journal-with-data-loss`. Only use the revive path after
reviewing Dolt's data-loss warning.

### `failed to import: issue already exists`

You're trying to bootstrap a database with issues that conflict with existing
ones. Clear the local database and re-initialize from an export:

```bash
# DESTROYS the local database — export first if unsure
rm -rf .beads/embeddeddolt   # embedded mode (default)
rm -rf .beads/dolt           # server mode

bd init --from-jsonl
```

### Import fails with missing parent errors

Errors like `parent issue bd-abc does not exist` when bootstrapping from JSONL
or pulling hierarchical issues (e.g., `bd-abc.1`) mean the parent issue was
deleted but children still reference it — typically after `bd delete` on a
parent, a branch merge where one side deleted it, or an incomplete import.

Imports accept orphans without validation by default, so the children still
arrive; the error indicates the parent itself is gone. Recreate the parent
(or close out the orphaned children) after the import.

**Prevention:** use `bd delete --cascade` to also delete children, and review
children first with `bd children <parent-id>`.

### Old data returns after reset

`bd admin reset --force` only removes **local** beads data. Old issues can
return from configured Dolt remotes or from other machines that push after
you reset. For a complete clean slate, reset every clone (or clear the
remote's beads data) before re-running `bd init`.

If you previously used the removed legacy sync-branch feature, also delete
its branch and worktrees — see
[Worktrees: Legacy Cleanup](/reference/worktrees#legacy-cleanup).

### `bd` shows 0 issues but the database has data

**Symptom (server mode):** All `bd` commands return empty results even though
your data exists.

**Cause:** `bd` is connecting to a different Dolt server or database than
expected — an empty "shadow" database on the wrong server.

**Diagnosis:**

```bash
# Check what mode and server bd is using
cat .beads/metadata.json | grep -E "dolt_mode|dolt_server_port"

# Run server-mode health checks
bd doctor --server

# Confirm what the connected database contains
bd sql 'SELECT COUNT(*) FROM issues'
```

**Fix:** ensure your Dolt server is running from the correct data directory
and that `metadata.json` points at the right server and port. If a stale
`.beads/dolt/` directory exists alongside an external-server configuration,
it can shadow the real database — confirm your real data lives on the server
before removing the stale directory.

### Configured server unreachable (auto-start disabled)

**Symptom (server mode):** `bd` returns "database not found on Dolt server"
when the configured server is down.

**Cause:** When `metadata.json` has an explicit `dolt_server_port`, bd treats
the server as externally managed and intentionally disables auto-start —
spawning a different server would create a shadow database.

**Fix:**

```bash
# Start your configured Dolt server
bd dolt start

# Or start manually with the correct data directory
dolt sql-server --host 127.0.0.1 --port 3307 --data-dir /path/to/your/dolt/data
```

If you want auto-start behavior, remove `dolt_server_port` from
`.beads/metadata.json`.

### Port conflicts with multiple projects

**Symptom (server mode):** Commands in a second project fail or connect to the
wrong database, and multiple `dolt sql-server` processes are running.

**Cause:** Each server-mode project starts its own Dolt server by default,
which can conflict on machines with many projects.

**Fix:** Enable shared server mode so all projects use a single Dolt server:

```bash
# Option 1: Machine-wide (add to ~/.bashrc or ~/.zshrc)
export BEADS_DOLT_SHARED_SERVER=1

# Option 2: Per-project
bd config set dolt.shared-server true
```

After enabling, existing projects may need `bd init --reinit-local -q` to
create their database on the shared server.

**Verify:** `bd dolt status` from any project should show the same server,
port 3308, and `~/.beads/shared-server/` as the data directory.

### Multiple databases detected warning

bd warns when it finds more than one `.beads` directory in your directory
hierarchy, marking the one in use with `▶` (usually the closest to your
current directory). Multiple databases risk working in the wrong one or
tracking the same work twice.

- **Nested projects (intentional):** this is supported — just note which
  database is active, or pin it explicitly.
- **Accidental duplicates:** export from the unwanted database
  (`bd export -o issue-export.jsonl`), then remove its `.beads` directory.
- **Override selection:**

  ```bash
  # Point bd at a specific .beads directory (recommended)
  export BEADS_DIR=/path/to/.beads

  # Legacy method (deprecated, points at the database file directly)
  export BEADS_DB=/path/to/db
  ```

### Circuit breaker: "server appears down, failing fast"

**Symptom (server mode):** Every `bd` command fails with
`dolt circuit breaker is open: server appears down, failing fast (cooldown 30s)`,
persisting across repeated invocations.

**Cause:** The circuit breaker tripped after repeated connection failures.
Its state lives in a file under `/tmp/beads-circuit/` (named
`beads-dolt-circuit-<host>-<port>[-<db>].json`, keyed on host:port) and is
shared across all `bd` processes. Once tripped, all commands to that host:port
are rejected until a successful probe resets it.

For beads-managed local servers, `bd dolt status` reports from the server's
PID file — a "running" status does not guarantee the server is actually
accepting connections on the expected port.

**Diagnosis:**

```bash
# Check circuit breaker state
cat /tmp/beads-circuit/beads-dolt-circuit-*.json

# Check if the Dolt server is actually listening
lsof -i :<port>

# Compare the configured port with what's running
cat .beads/metadata.json | grep port
```

**Fix:**

```bash
rm /tmp/beads-circuit/beads-dolt-circuit-*.json
bd dolt stop
bd dolt start
bd list
```

On macOS, `/tmp` is a symlink to `/private/tmp`, which is not always cleared
on restart — the state file can persist across reboots.

## Dolt Server Issues

### Server not starting

```bash
# Check server health
bd doctor

# Check server logs (server mode; embedded mode runs in-process, no server log)
cat .beads/dolt-server.log

# Restart the server
bd dolt stop
bd dolt start
```

### Version mismatch

After upgrading bd:

```bash
bd dolt stop
bd dolt start
```

## Sync Issues

### Changes not syncing

```bash
# Force push to Dolt remote
bd dolt push

# Check hooks
bd hooks list
```

### Recovery from backup

```bash
# Restore from a Dolt backup
bd backup restore [path] --force

# Or pull from Dolt remote
bd dolt pull
```

### Merge conflicts

Dolt merges at the cell level, so concurrent changes conflict only when they
touch the same field of the same issue. Hash-based IDs mean different issues
never collide on ID.

```bash
# Check for and fix Dolt conflicts
bd doctor --fix

# Re-push
bd dolt push
```

See [Merge Conflicts](/recovery/merge-conflicts) for the full runbook.

## Git Hook Issues

### Hooks not running

```bash
# Check if installed
ls -la .git/hooks/

# Reinstall
bd hooks install
```

### Hook errors

```bash
# Check hook script
cat .git/hooks/pre-commit

# Run manually
.git/hooks/pre-commit
```

### Hook timeout kills chained pre-commit hooks

**Symptom:** After `bd hooks install`, chained pre-commit hooks (eslint,
prettier, ruff, etc.) stop running, with:
`beads: hook 'pre-commit' timed out after 300s -- continuing without beads`.

**Cause:** The beads hook shim wraps `bd hooks run` with an OS-level timeout.
Since `bd hooks run` chains to your original hook internally, the timeout
covers both beads' own work and your entire hook pipeline.

**Fix:** Increase the timeout (default 300 seconds):

```bash
# Add to ~/.bashrc or ~/.zshrc
export BEADS_HOOK_TIMEOUT=600  # 10 minutes (in seconds)
```

### Permission denied on git hooks

Git hooks need execute permissions:

```bash
chmod +x .git/hooks/pre-commit
chmod +x .git/hooks/post-merge
chmod +x .git/hooks/post-checkout
```

### Corrupted symlinked `CLAUDE.md`

**Symptom:** Git reports `CLAUDE.md` as a symlink entry (mode `120000`), but
the indexed blob contains multi-line Markdown instead of a one-line symlink
target. On macOS this can make clones or checkouts fail.

This affects repositories corrupted by older setup behavior (fixed in
[#4192](https://github.com/gastownhall/beads/pull/4192)). To repair an
existing bad index entry:

```bash
# Confirm the bad entry: mode 120000 but Markdown content
git ls-files -s CLAUDE.md
git cat-file -p :CLAUDE.md | sed -n '1,5p'

# Convert the blob to a regular tracked file without changing content
sha=$(git rev-parse :CLAUDE.md)
git update-index --cacheinfo 100644,$sha,CLAUDE.md
git checkout-index -f -- CLAUDE.md

# Verify: first column should now be 100644
git ls-files -s CLAUDE.md
git diff -- CLAUDE.md
```

Commit the mode repair after review.

### "Branch already checked out" or unexpected `.git/beads-worktrees/`

Older beads versions created hidden git worktrees for a removed sync-branch
feature; leftovers can lock branches (`fatal: 'main' is already checked out
at .../beads-worktrees/...`). Remove them:

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

See [Worktrees: Legacy Cleanup](/reference/worktrees#legacy-cleanup).

## Dependency Issues

### `bd ready` shows nothing but I have open issues

Those issues probably have open blockers:

```bash
# See blocked issues
bd blocked

# Show the dependency tree (default max depth: 50)
bd dep tree <issue-id>
bd dep tree <issue-id> --max-depth 10

# Remove a blocking dependency if needed
bd dep remove <from-id> <to-id>
```

Remember: only `blocks` dependencies affect ready work.

### Circular dependencies

bd prevents dependency cycles, which break ready work detection:

```bash
# Detect cycles
bd dep cycles

# Remove one dependency
bd dep remove bd-A bd-B
```

See [Circular Dependencies](/recovery/circular-dependencies) for the full
runbook.

### Dependencies not showing up

```bash
# Show full issue details including dependencies
bd show <issue-id>

# Visualize the dependency tree
bd dep tree <issue-id>
```

Different dependency types have different meanings — only `blocks` gates
ready work. See [Dependencies](/core-concepts/dependencies).

## Performance Issues

### Slow queries

```bash
# Check database stats
bd stats

# Check on-disk size
du -sh .beads/embeddeddolt   # embedded mode (default)
du -sh .beads/dolt           # server mode

# Preview compaction candidates
bd admin compact --dry-run --all

# Compact if large
bd admin compact --analyze
```

Consider splitting very large projects into multiple databases:

```bash
cd ~/project/component1 && bd init --prefix comp1
cd ~/project/component2 && bd init --prefix comp2
```

### High memory usage

```bash
# Run Dolt garbage collection to compact storage
bd admin compact --dolt
```

## Agent Issues

### Agent creates duplicate issues

Agents may not realize an issue already exists. Prevention strategies:

- Have agents search first: `bd list --json | grep "title"`
- Label auto-created issues: `bd create "..." -l auto-generated`
- Consolidate duplicates: `bd duplicate <dup-id> --of <canonical-id>` closes
  the duplicate with a reference to the canonical issue

### Agent gets confused by complex dependencies

Simplify the dependency structure:

```bash
# Check for overly complex trees
bd dep tree <issue-id>

# Remove unnecessary dependencies
bd dep remove <from-id> <to-id>

# Use labels instead of dependencies for loose relationships
bd label add <issue-id> related-to-feature-X
```

### MCP server not working

```bash
# Verify the MCP server is installed
pip list | grep beads-mcp

# Check MCP configuration (Claude Desktop on macOS)
cat ~/Library/Application\ Support/Claude/claude_desktop_config.json

# Test that the CLI itself works
bd version
bd ready
bd doctor
```

See [MCP Server](/integrations/mcp-server) for setup and configuration.

### Sandboxed environments (Codex, Claude Code, etc.)

Sandboxes that restrict process and network permissions can prevent bd from
controlling a Dolt server, causing persistent "database out of sync" errors
or `bd dolt stop` failing with "operation not permitted".

bd auto-detects sandboxed environments and prints
`Sandbox detected, using direct mode`. If auto-detection fails, pass the
global `--sandbox` flag explicitly:

```bash
bd --sandbox ready
bd --sandbox create "Fix bug" -p 1
```

Sandbox mode disables Dolt auto-push so bd works without server control or
network access. Sync manually once outside the sandbox:

```bash
bd dolt push
```

If staleness errors persist, `bd doctor --fix` forces a metadata refresh
(low risk — it updates tracking metadata, not issues). Background:
[GH#353](https://github.com/gastownhall/beads/issues/353).

## Platform-Specific Issues

### Windows: Path issues

```pwsh
# Check if bd.exe is in PATH
where.exe bd

# Add Go bin to PATH (permanently)
[Environment]::SetEnvironmentVariable(
    "Path",
    $env:Path + ";$env:USERPROFILE\go\bin",
    [EnvironmentVariableTarget]::User
)

# Reload PATH in current session
$env:Path = [Environment]::GetEnvironmentVariable("Path", "User")
```

### Windows: Firewall blocking the Dolt server

In server mode, the Dolt server listens on loopback TCP. Allow `bd.exe`
through Windows Firewall: Windows Security → Firewall & network protection →
"Allow an app through firewall" → add `bd.exe` for Private networks.

### Windows: Controlled Folder Access blocks `bd init`

**Symptom:** `bd init` hangs indefinitely with high CPU usage, and CTRL+C
doesn't work. Controlled Folder Access may block bd without showing a
notification, making this hard to diagnose without the `-v` flag:

```pwsh
bd init -v
# Error: failed to create .beads directory: mkdir .beads: The system cannot find the file specified
```

**Solution:** Whitelist `bd.exe`: Windows Security → Virus & threat
protection → Ransomware protection → Controlled folder access → "Allow an
app through Controlled folder access" → browse to `bd.exe` (typically
`%USERPROFILE%\go\bin\bd.exe`). Then retry `bd init`.

### macOS: Gatekeeper blocking execution

1. Verify the downloaded binary checksum matches the release `checksums.txt`.
2. If you used `scripts/install.sh`, note that macOS ad-hoc re-signing is
   opt-in (`BEADS_INSTALL_RESIGN_MACOS=1`).
3. Approve the binary:

```bash
# Remove quarantine attribute
xattr -d com.apple.quarantine /usr/local/bin/bd

# Or: System Preferences → Security & Privacy → General → "Allow anyway"
```

## Debug Environment Variables

bd supports environment variables for debugging specific subsystems. Enable
them when troubleshooting or when requested by maintainers.

| Variable | Purpose | Output |
|----------|---------|--------|
| `BD_DEBUG` | General debug logging | stderr |
| `BD_DEBUG_RPC` | RPC communication between CLI and Dolt server | stderr |
| `BD_DEBUG_SYNC` | Sync and import timestamp protection | stderr |
| `BD_DEBUG_ROUTING` | Issue routing and multi-repo resolution | stderr |
| `BD_DEBUG_FRESHNESS` | Database file replacement detection | server log |

Set any of them to `1` to enable; `unset` to disable.

```bash
# General debugging
BD_DEBUG=1 bd ready

# Capture debug output to a file
BD_DEBUG=1 bd dolt push 2> debug.log

# Sync timestamp protection, e.g.:
# [debug] Protected bd-123: local=2024-01-20T10:00:00Z >= incoming=2024-01-20T09:55:00Z
BD_DEBUG_SYNC=1 bd dolt push

# Freshness output goes to the server log (server mode), not stderr
BD_DEBUG_FRESHNESS=1 bd dolt start
tail -f .beads/dolt-server.log | grep freshness
```

For multi-repo routing configuration, see [Routing](/multi-agent/routing).

## Getting Help

### Debug output

```bash
bd --verbose list
```

### Logs

```bash
# Server mode (embedded mode runs in-process, no server log)
cat .beads/dolt-server.log
```

### System info

```bash
bd info --json
```

### File an issue

```bash
# Include this info
bd version
bd info --json
uname -a
```

Report at: https://github.com/gastownhall/beads/issues — or ask in
[GitHub Discussions](https://github.com/gastownhall/beads/discussions).
