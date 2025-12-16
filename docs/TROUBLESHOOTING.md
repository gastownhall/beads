# Troubleshooting bd

Common issues and solutions for bd users.

## Table of Contents

- [Installation Issues](#installation-issues)
- [Antivirus False Positives](#antivirus-false-positives)
- [Database Issues](#database-issues)
- [Git and Sync Issues](#git-and-sync-issues)
- [Ready Work and Dependencies](#ready-work-and-dependencies)
- [Performance Issues](#performance-issues)
- [Agent-Specific Issues](#agent-specific-issues)
- [Platform-Specific Issues](#platform-specific-issues)

## Installation Issues

### `bd: command not found`

bd is not in your PATH. Either:

```bash
# Check if installed
go list -f {{.Target}} github.com/steveyegge/beads/cmd/bd

# Add Go bin to PATH (add to ~/.bashrc or ~/.zshrc)
export PATH="$PATH:$(go env GOPATH)/bin"

# Or reinstall
go install github.com/steveyegge/beads/cmd/bd@latest
```

### Wrong version of bd running / Multiple bd binaries in PATH

If `bd version` shows an unexpected version (e.g., older than what you just installed), you likely have multiple `bd` binaries in your PATH.

**Diagnosis:**
```bash
# Check all bd binaries in PATH
which -a bd

# Example output showing conflict:
# /Users/you/go/bin/bd        <- From go install (older)
# /opt/homebrew/bin/bd        <- From Homebrew (newer)
```

**Solution:**
```bash
# Remove old go install version
rm ~/go/bin/bd

# Or remove mise-managed Go installs
rm ~/.local/share/mise/installs/go/*/bin/bd

# Verify you're using the correct version
which bd        # Should show /opt/homebrew/bin/bd or your package manager path
bd version      # Should show the expected version
```

**Why this happens:** If you previously installed bd via `go install`, the binary was placed in `~/go/bin/`. When you later install via Homebrew or another package manager, the old `~/go/bin/bd` may appear earlier in your PATH, causing the wrong version to run.

**Recommendation:** Choose one installation method (Homebrew recommended) and stick with it. Avoid mixing `go install` with package managers.

### `zsh: killed bd` or crashes on macOS

Some users report crashes when running `bd init` or other commands on macOS. This is typically caused by CGO/SQLite compatibility issues.

**Workaround:**
```bash
# Build with CGO enabled
CGO_ENABLED=1 go install github.com/steveyegge/beads/cmd/bd@latest

# Or if building from source
git clone https://github.com/steveyegge/beads
cd beads
CGO_ENABLED=1 go build -o bd ./cmd/bd
sudo mv bd /usr/local/bin/
```

If you installed via Homebrew, this shouldn't be necessary as the formula already enables CGO. If you're still seeing crashes with the Homebrew version, please [file an issue](https://github.com/steveyegge/beads/issues).

## Antivirus False Positives

### Antivirus software flags bd as malware

**Symptom**: Kaspersky, Windows Defender, or other antivirus software detects `bd` or `bd.exe` as a trojan or malicious software and removes it.

**Common detections**:
- Kaspersky: `PDM:Trojan.Win32.Generic`
- Windows Defender: Various generic trojan detections

**Cause**: This is a **false positive**. Go binaries are commonly flagged by antivirus heuristics because some malware is written in Go. This is a known industry-wide issue affecting many legitimate Go projects.

**Solutions**:

1. **Add bd to antivirus exclusions** (recommended):
   - Add the bd installation directory to your antivirus exclusion list
   - This is safe - beads is open source and checksums are provided

2. **Verify file integrity before excluding**:
   ```bash
   # Windows PowerShell
   Get-FileHash bd.exe -Algorithm SHA256

   # macOS/Linux
   shasum -a 256 bd
   ```
   Compare with checksums from the [GitHub release page](https://github.com/steveyegge/beads/releases)

3. **Report the false positive**:
   - Help improve detection by reporting to your antivirus vendor
   - Most vendors have false positive submission forms

**Detailed guide**: See [docs/ANTIVIRUS.md](ANTIVIRUS.md) for complete instructions including:
- How to add exclusions for specific antivirus software
- How to report false positives to vendors
- Why Go binaries trigger these detections
- Future plans for code signing

## Database Issues

### `database is locked`

Another bd process is accessing the database, or SQLite didn't close properly. Solutions:

```bash
# Find and kill hanging processes
ps aux | grep bd
kill <pid>

# Remove lock files (safe if no bd processes running)
rm .beads/*.db-journal .beads/*.db-wal .beads/*.db-shm
```

**Note**: bd uses a pure Go SQLite driver (`modernc.org/sqlite`) for better portability. Under extreme concurrent load (100+ simultaneous operations), you may see "database is locked" errors. This is a known limitation of the pure Go implementation and does not affect normal usage. For very high concurrency scenarios, consider using the CGO-enabled driver or PostgreSQL (planned for future release).

### `bd init` fails with "directory not empty"

`.beads/` already exists. Options:

```bash
# Use existing database
bd list  # Should work if already initialized

# Or remove and reinitialize (DESTROYS DATA!)
rm -rf .beads/
bd init
```

### `failed to import: issue already exists`

You're trying to import issues that conflict with existing ones. Options:

```bash
# Skip existing issues (only import new ones)
bd import -i issues.jsonl --skip-existing

# Or clear database and re-import everything
rm .beads/*.db
bd import -i .beads/issues.jsonl
```

### Import fails with missing parent errors

If you see errors like `parent issue bd-abc does not exist` when importing hierarchical issues (e.g., `bd-abc.1`, `bd-abc.2`), this means the parent issue was deleted but children still reference it.

**Quick fix using resurrection:**

```bash
# Auto-resurrect deleted parents from JSONL history
bd import -i issues.jsonl --orphan-handling resurrect

# Or set as default behavior
bd config set import.orphan_handling "resurrect"
bd sync  # Now uses resurrect mode
```

**What resurrection does:**

1. Searches the full JSONL file for the missing parent issue
2. Recreates it as a tombstone (Status=Closed, Priority=4)
3. Preserves the parent's original title and description
4. Maintains referential integrity for hierarchical children
5. Also resurrects dependencies on best-effort basis

**Other handling modes:**

```bash
# Allow orphans (default) - import without validation
bd config set import.orphan_handling "allow"

# Skip orphans - partial import with warnings
bd config set import.orphan_handling "skip"

# Strict - fail fast on missing parents
bd config set import.orphan_handling "strict"
```

**When this happens:**

- Parent issue was deleted using `bd delete`
- Branch merge where one side deleted the parent
- Manual JSONL editing that removed parent entries
- Database corruption or incomplete import

**Prevention:**

- Use `bd delete --cascade` to also delete children
- Check for orphans before cleanup: `bd list --id bd-abc.*`
- Review impact before deleting epic/parent issues

See [CONFIG.md](CONFIG.md#example-import-orphan-handling) for complete configuration documentation.

### Database corruption

**Important**: Distinguish between **logical consistency issues** (ID collisions, wrong prefixes) and **physical SQLite corruption**.

For **physical database corruption** (disk failures, power loss, filesystem errors):

```bash
# Check database integrity
sqlite3 .beads/*.db "PRAGMA integrity_check;"

# If corrupted, reimport from JSONL (source of truth in git)
mv .beads/*.db .beads/*.db.backup
bd init
bd import -i .beads/issues.jsonl
```

For **logical consistency issues** (ID collisions from branch merges, parallel workers):

```bash
# This is NOT corruption - use collision resolution instead
bd import -i .beads/issues.jsonl
```

See [FAQ](FAQ.md#whats-the-difference-between-sqlite-corruption-and-id-collisions) for the distinction.

### Multiple databases detected warning

If you see a warning about multiple `.beads` databases in the directory hierarchy:

```
╔══════════════════════════════════════════════════════════════════════════╗
║ WARNING: 2 beads databases detected in directory hierarchy             ║
╠══════════════════════════════════════════════════════════════════════════╣
║ Multiple databases can cause confusion and database pollution.          ║
║                                                                          ║
║ ▶ /path/to/project/.beads (15 issues)                                   ║
║   /path/to/parent/.beads (32 issues)                                    ║
║                                                                          ║
║ Currently using the closest database (▶). This is usually correct.      ║
║                                                                          ║
║ RECOMMENDED: Consolidate or remove unused databases to avoid confusion. ║
╚══════════════════════════════════════════════════════════════════════════╝
```

This means bd found multiple `.beads` directories in your directory hierarchy. The `▶` marker shows which database is actively being used (usually the closest one to your current directory).

**Why this matters:**
- Can cause confusion about which database contains your work
- Easy to accidentally work in the wrong database
- May lead to duplicate tracking of the same work

**Solutions:**

1. **If you have nested projects** (intentional):
   - This is fine! bd is designed to support this
   - Just be aware which database you're using
   - Set `BEADS_DIR` environment variable to point to your `.beads` directory if you want to override the default selection
   - Or use `BEADS_DB` (deprecated) to point directly to the database file

2. **If you have accidental duplicates** (unintentional):
   - Decide which database to keep
   - Export issues from the unwanted database: `cd <unwanted-dir> && bd export -o backup.jsonl`
   - Remove the unwanted `.beads` directory: `rm -rf <unwanted-dir>/.beads`
   - Optionally import issues into the main database if needed

3. **Override database selection**:
   ```bash
   # Temporarily use specific .beads directory (recommended)
   BEADS_DIR=/path/to/.beads bd list

   # Or add to shell config for permanent override
   export BEADS_DIR=/path/to/.beads

   # Legacy method (deprecated, points to database file directly)
   BEADS_DB=/path/to/.beads/issues.db bd list
   export BEADS_DB=/path/to/.beads/issues.db
   ```

**Note**: The warning only appears when bd detects multiple databases. If you see this consistently and want to suppress it, you're using the correct database (marked with `▶`).

## Git and Sync Issues

### Git merge conflict in `issues.jsonl`

When both sides add issues, you'll get conflicts. Resolution:

1. Open `.beads/issues.jsonl`
2. Look for `<<<<<<< HEAD` markers
3. Most conflicts can be resolved by **keeping both sides**
4. Each line is independent unless IDs conflict
5. For same-ID conflicts, keep the newest (check `updated_at`)

Example resolution:
```bash
# After resolving conflicts manually
git add .beads/issues.jsonl
git commit
bd import -i .beads/issues.jsonl  # Sync to SQLite
```

See [ADVANCED.md](ADVANCED.md) for detailed merge strategies.

### Git merge conflicts in JSONL

**With hash-based IDs (v0.20.1+), ID collisions don't occur.** Different issues get different hash IDs.

If git shows a conflict in `.beads/issues.jsonl`, it's because the same issue was modified on both branches:

```bash
# Preview what will be updated
bd import -i .beads/issues.jsonl --dry-run

# Resolve git conflict (keep newer version or manually merge)
git checkout --theirs .beads/issues.jsonl  # Or --ours, or edit manually

# Import updates the database
bd import -i .beads/issues.jsonl
```

See [ADVANCED.md#handling-git-merge-conflicts](ADVANCED.md#handling-git-merge-conflicts) for details.

### Permission denied on git hooks

Git hooks need execute permissions:

```bash
chmod +x .git/hooks/pre-commit
chmod +x .git/hooks/post-merge
chmod +x .git/hooks/post-checkout
```

### "Branch already checked out" when switching branches

**Symptom:**
```bash
$ git checkout main
fatal: 'main' is already checked out at '/path/to/.git/beads-worktrees/beads-sync'
```

**Cause:** Beads creates git worktrees internally when using the sync-branch feature (configured via `bd init --branch` or `bd config set sync.branch`). These worktrees lock the branches they're checked out to.

**Solution:**
```bash
# Remove beads-created worktrees
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune

# Now you can checkout the branch
git checkout main
```

**Permanent fix (disable sync-branch):**
```bash
bd config set sync.branch ""
```

See [WORKTREES.md#beads-created-worktrees-sync-branch](WORKTREES.md#beads-created-worktrees-sync-branch) for details.

### Unexpected worktree directories in .git/

**Symptom:** You notice `.git/beads-worktrees/` or `.git/worktrees/beads-*` directories you didn't create.

**Explanation:** Beads automatically creates these worktrees when using the sync-branch feature to commit issue updates to a separate branch without switching your working directory.

**If you don't want these:**
```bash
# Disable sync-branch feature
bd config set sync.branch ""

# Clean up existing worktrees
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

See [WORKTREES.md](WORKTREES.md) for details on how beads uses worktrees.

### Auto-sync not working

Check if auto-sync is enabled:

```bash
# Check if daemon is running
ps aux | grep "bd daemon"

# Manually export/import
bd export -o .beads/issues.jsonl
bd import -i .beads/issues.jsonl

# Install git hooks for guaranteed sync
cd examples/git-hooks && ./install.sh
```

If you disabled auto-sync with `--no-auto-flush` or `--no-auto-import`, remove those flags or use `bd sync` manually.

## Ready Work and Dependencies

### `bd ready` shows nothing but I have open issues

Those issues probably have open blockers. Check:

```bash
# See blocked issues
bd blocked

# Show dependency tree (default max depth: 50)
bd dep tree <issue-id>

# Limit tree depth to prevent deep traversals
bd dep tree <issue-id> --max-depth 10

# Remove blocking dependency if needed
bd dep remove <from-id> <to-id>
```

Remember: Only `blocks` dependencies affect ready work.

### Circular dependency errors

bd prevents dependency cycles, which break ready work detection. To fix:

```bash
# Detect all cycles
bd dep cycles

# Remove the dependency causing the cycle
bd dep remove <from-id> <to-id>

# Or redesign your dependency structure
```

### Dependencies not showing up

Check the dependency type:

```bash
# Show full issue details including dependencies
bd show <issue-id>

# Visualize the dependency tree
bd dep tree <issue-id>
```

Remember: Different dependency types have different meanings:
- `blocks` - Hard blocker, affects ready work
- `related` - Soft relationship, doesn't block
- `parent-child` - Hierarchical (child depends on parent)
- `discovered-from` - Work discovered during another issue

## Performance Issues

### Export/import is slow

For large databases (10k+ issues):

```bash
# Export only open issues
bd export --format=jsonl --status=open -o .beads/issues.jsonl

# Or filter by priority
bd export --format=jsonl --priority=0 --priority=1 -o critical.jsonl
```

Consider splitting large projects into multiple databases.

### Commands are slow

Check database size and consider compaction:

```bash
# Check database stats
bd stats

# Preview compaction candidates
bd compact --dry-run --all

# Compact old closed issues
bd compact --days 90
```

### Large JSONL files

If `.beads/issues.jsonl` is very large:

```bash
# Check file size
ls -lh .beads/issues.jsonl

# Remove old closed issues
bd compact --days 90

# Or split into multiple projects
cd ~/project/component1 && bd init --prefix comp1
cd ~/project/component2 && bd init --prefix comp2
```

## Agent-Specific Issues

### Agent creates duplicate issues

Agents may not realize an issue already exists. Prevention strategies:

- Have agents search first: `bd list --json | grep "title"`
- Use labels to mark auto-created issues: `bd create "..." -l auto-generated`
- Review and deduplicate periodically: `bd list | sort`
- Use `bd merge` to consolidate duplicates: `bd merge bd-2 --into bd-1`

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

### Agent can't find ready work

Check if issues are blocked:

```bash
# See what's blocked
bd blocked

# See what's actually ready
bd ready --json

# Check specific issue
bd show <issue-id>
bd dep tree <issue-id>
```

### MCP server not working

Check installation and configuration:

```bash
# Verify MCP server is installed
pip list | grep beads-mcp

# Check MCP configuration
cat ~/Library/Application\ Support/Claude/claude_desktop_config.json

# Test CLI works
bd version
bd ready

# Check for daemon
ps aux | grep "bd daemon"
```

See [integrations/beads-mcp/README.md](../integrations/beads-mcp/README.md) for MCP-specific troubleshooting.

### Sandboxed environments (Codex, Claude Code, etc.)

**Issue:** Sandboxed environments restrict permissions, preventing daemon control and causing "out of sync" errors.

**Common symptoms:**
- "Database out of sync with JSONL" errors that persist after running `bd import`
- `bd daemon --stop` fails with "operation not permitted"
- Cannot kill daemon process with `kill <pid>`
- JSONL hash mismatch warnings (bd-160)
- Commands intermittently fail with staleness errors

**Root cause:** The sandbox can't signal/kill the existing daemon process, so the DB stays stale and refuses to import.

---

#### Quick fix: Sandbox mode (auto-detected)

**As of v0.21.1+**, bd automatically detects sandboxed environments and enables sandbox mode.

When auto-detected, you'll see: `ℹ️  Sandbox detected, using direct mode`

**Manual override** (if auto-detection fails):

```bash
# Explicitly enable sandbox mode
bd --sandbox ready
bd --sandbox create "Fix bug" -p 1
bd --sandbox update bd-42 --status in_progress

# Equivalent to:
bd --no-daemon --no-auto-flush --no-auto-import <command>
```

**What sandbox mode does:**
- Disables daemon (uses direct SQLite mode)
- Disables auto-export to JSONL
- Disables auto-import from JSONL
- Allows bd to work in network-restricted environments

**Note:** You'll need to manually sync when outside the sandbox:
```bash
# After leaving sandbox, sync manually
bd sync
```

---

#### Escape hatches for stuck states

If you're stuck in a "database out of sync" loop with a running daemon you can't stop, use these flags:

**1. Force metadata update (`--force` flag on import)**

When `bd import` reports "0 created, 0 updated" but staleness persists:

```bash
# Force metadata refresh even when DB appears synced
bd import --force

# This updates internal metadata tracking without changing issues
# Fixes: stuck state caused by stale daemon cache
```

**Shows:** `Metadata updated (database already in sync with JSONL)`

**2. Skip staleness check (`--allow-stale` global flag)**

Emergency escape hatch to bypass staleness validation:

```bash
# Allow operations on potentially stale data
bd --allow-stale ready
bd --allow-stale list --status open

# Shows warning:
# ⚠️  Staleness check skipped (--allow-stale), data may be out of sync
```

**⚠️ Caution:** Use sparingly - you may see incomplete or outdated data.

**3. Use sandbox mode (preferred)**

```bash
# Most reliable for sandboxed environments
bd --sandbox ready
bd --sandbox import -i .beads/issues.jsonl
```

---

#### Troubleshooting workflow

If stuck in a sandboxed environment:

```bash
# Step 1: Try sandbox mode (cleanest solution)
bd --sandbox ready

# Step 2: If you get staleness errors, force import
bd import --force -i .beads/issues.jsonl

# Step 3: If still blocked, use allow-stale (emergency only)
bd --allow-stale ready

# Step 4: When back outside sandbox, sync normally
bd sync
```

---

#### Understanding the flags

| Flag | Purpose | When to use | Risk |
|------|---------|-------------|------|
| `--sandbox` | Disable daemon and auto-sync | Sandboxed environments (Codex, containers) | Low - safe for sandboxes |
| `--force` (import) | Force metadata update | Stuck "0 created, 0 updated" loop | Low - updates metadata only |
| `--allow-stale` | Skip staleness validation | Emergency access to database | **High** - may show stale data |

**Related:**
- See [DAEMON.md](DAEMON.md) for daemon troubleshooting
- See [Claude Code sandboxing documentation](https://www.anthropic.com/engineering/claude-code-sandboxing) for more about sandbox restrictions
- GitHub issue [#353](https://github.com/steveyegge/beads/issues/353) for background

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

### Windows: Firewall blocking daemon

The daemon listens on loopback TCP. Allow `bd.exe` through Windows Firewall:

1. Open Windows Security → Firewall & network protection
2. Click "Allow an app through firewall"
3. Add `bd.exe` and enable for Private networks
4. Or disable firewall temporarily for testing

### macOS: Gatekeeper blocking execution

If macOS blocks bd:

```bash
# Remove quarantine attribute
xattr -d com.apple.quarantine /usr/local/bin/bd

# Or allow in System Preferences
# System Preferences → Security & Privacy → General → "Allow anyway"
```

### Linux: Permission denied

If you get permission errors:

```bash
# Make bd executable
chmod +x /usr/local/bin/bd

# Or install to user directory
mkdir -p ~/.local/bin
mv bd ~/.local/bin/
export PATH="$HOME/.local/bin:$PATH"
```

## Getting Help

If none of these solutions work:

1. **Check existing issues**: [GitHub Issues](https://github.com/steveyegge/beads/issues)
2. **Enable debug logging**: `bd --verbose <command>`
3. **File a bug report**: Include:
   - bd version: `bd version`
   - OS and architecture: `uname -a`
   - Error message and full command
   - Steps to reproduce
4. **Join discussions**: [GitHub Discussions](https://github.com/steveyegge/beads/discussions)

## Related Documentation

- **[README.md](../README.md)** - Core features and quick start
- **[ADVANCED.md](ADVANCED.md)** - Advanced features
- **[FAQ.md](FAQ.md)** - Frequently asked questions
- **[INSTALLING.md](INSTALLING.md)** - Installation guide
