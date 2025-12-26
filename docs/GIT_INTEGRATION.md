# Git Integration Guide

**For:** AI agents and developers managing bd git workflows  
**Version:** 0.21.0+

## Overview

bd integrates deeply with git for issue tracking synchronization. This guide covers merge conflict resolution, intelligent merge drivers, git worktrees, and protected branch workflows.

## Git Worktrees

**🚧 Enhanced Support:** Beads now has comprehensive Git worktree compatibility with shared database architecture. While thoroughly tested internally, real-world usage may reveal additional edge cases.

### How It Works

Git worktrees share the same `.git` directory and `.beads` database:
- All worktrees use the same `.beads/beads.db` file in the main repository
- Database discovery prioritizes main repository location
- Worktree-aware git operations prevent conflicts
- Git hooks automatically adapt to worktree context

### Daemon Mode Limitations

**⚠️ Important:** Daemon mode does NOT work correctly with `git worktree` due to shared database state.

The daemon maintains its own view of the current working directory and git state. When multiple worktrees share the same `.beads` database, the daemon may commit changes intended for one branch to a different branch.

### Solutions for Worktree Users

**1. Use `--no-daemon` flag (recommended):**

```bash
bd --no-daemon ready
bd --no-daemon create "Fix bug" -p 1
bd --no-daemon update bd-42 --status in_progress
```

**2. Disable daemon via environment (entire session):**

```bash
export BEADS_NO_DAEMON=1
bd ready  # All commands use direct mode
```

**3. Disable auto-start (less safe, still warns):**

```bash
export BEADS_AUTO_START_DAEMON=false
```

### Automatic Detection & Warnings

bd automatically detects worktrees and shows prominent warnings if daemon mode is active:

```
╔══════════════════════════════════════════════════════════════════════════╗
║ WARNING: Git worktree detected with daemon mode                         ║
╠══════════════════════════════════════════════════════════════════════════╣
║ Git worktrees share the same .beads directory, which can cause the      ║
║ daemon to commit/push to the wrong branch.                               ║
║                                                                          ║
║ Shared database: /path/to/main/.beads                                    ║
║ Worktree git dir: /path/to/shared/.git                                   ║
║                                                                          ║
║ RECOMMENDED SOLUTIONS:                                                   ║
║   1. Use --no-daemon flag:    bd --no-daemon <command>                   ║
║   2. Disable daemon mode:     export BEADS_NO_DAEMON=1                   ║
╚══════════════════════════════════════════════════════════════════════════╝
```

### Worktree-Aware Features

**Database Discovery:**
- Searches main repository first for `.beads` directory
- Falls back to worktree-local search if needed
- Prevents database duplication across worktrees

**Git Hooks:**
- Pre-commit hook adapts to worktree context
- Automatically stages JSONL in regular repos
- Safely skips staging in worktrees (files outside working tree)
- Post-merge hook works correctly in both contexts

**Sync Operations:**
- Worktree-aware repository root detection
- Proper handling of git directory vs git common directory
- Safe concurrent access to shared database

### Why This Architecture Works

- **Shared Database:** Eliminates data duplication and sync conflicts
- **Priority Search:** Main repository database takes precedence
- **SQLite Locking:** Prevents corruption during concurrent access
- **Git Integration:** Hooks and sync operations adapt to context
- **Clear Warnings:** Users are guided to safe usage patterns

## Handling Merge Conflicts

**With hash-based IDs (v0.20.1+), ID collisions are eliminated!** Different issues get different hash IDs, so most git merges succeed cleanly.

### When Conflicts Occur

Git conflicts in `.beads/issues.jsonl` happen when:
- **Same issue modified on both branches** (different timestamps/fields)
- This is a **same-issue update conflict**, not an ID collision
- Conflicts are rare in practice since hash IDs prevent structural collisions

### Automatic Detection

bd automatically detects conflict markers and shows clear resolution steps:

```bash
# bd import rejects files with conflict markers
bd import -i .beads/issues.jsonl
# Error: JSONL file contains git conflict markers
# Resolve with: git checkout --theirs .beads/issues.jsonl

# Validate for conflicts
bd validate --checks=conflicts
```

Conflict markers detected: `<<<<<<<`, `=======`, `>>>>>>>`

### Resolution Workflow

```bash
# After git merge creates conflict in .beads/issues.jsonl

# Option 1: Accept their version (remote)
git checkout --theirs .beads/issues.jsonl
bd import -i .beads/issues.jsonl

# Option 2: Keep our version (local)
git checkout --ours .beads/issues.jsonl
bd import -i .beads/issues.jsonl

# Option 3: Manual resolution in editor
# Edit .beads/issues.jsonl to remove conflict markers
bd import -i .beads/issues.jsonl

# Commit the merge
git add .beads/issues.jsonl
git commit
```

**Note:** `bd import` automatically handles updates - same ID with different content is a normal update operation. No special flags needed. If you accidentally modified the same issue in both branches, just pick whichever version is more complete.

## Intelligent Merge Driver (Auto-Configured)

**As of v0.21+**, bd automatically configures its own merge driver during `bd init`. This uses the beads-merge algorithm (by @neongreen, vendored into bd) to provide intelligent JSONL merging.

### What It Does

- **Field-level 3-way merging** (not line-by-line)
- **Matches issues by identity** (id + created_at + created_by)
- **Smart field merging:**
  - Timestamps → max value
  - Dependencies → union
  - Status/priority → 3-way merge
- **Conflict markers** only for unresolvable conflicts
- **Auto-configured** during `bd init` (both interactive and `--quiet` modes)

### Auto-Configuration

**Happens automatically during `bd init`:**

```bash
# These are configured automatically:
git config merge.beads.driver "bd merge %A %O %A %B"
git config merge.beads.name "bd JSONL merge driver"

# .gitattributes entry added:
# .beads/issues.jsonl merge=beads
```

### Manual Setup

**If you skipped merge driver with `--skip-merge-driver`:**

```bash
git config merge.beads.driver "bd merge %A %O %A %B"
git config merge.beads.name "bd JSONL merge driver"
echo ".beads/issues.jsonl merge=beads" >> .gitattributes
```

### How It Works

During `git merge`, beads-merge:
1. Parses JSONL from all 3 versions (base, ours, theirs)
2. Matches issues by identity (id + created_at + created_by)
3. Merges fields intelligently per issue
4. Outputs merged JSONL or conflict markers

**Benefits:**
- Prevents spurious conflicts from line renumbering
- Handles timestamp updates gracefully
- Merges dependency/label changes intelligently
- Only conflicts on true semantic conflicts

### Alternative: Standalone beads-merge Binary

**If you prefer the standalone binary (same algorithm):**

```bash
# Install (requires Go 1.24+)
git clone https://github.com/neongreen/mono.git
cd mono/beads-merge
go install

# Configure Git merge driver
git config merge.beads.name "JSONL merge driver for beads"
git config merge.beads.driver "beads-merge %A %O %A %B"
```

### Jujutsu Integration

**For Jujutsu users**, add to `~/.jjconfig.toml`:

```toml
[merge-tools.beads-merge]
program = "beads-merge"
merge-args = ["$output", "$base", "$left", "$right"]
merge-conflict-exit-codes = [1]
```

Then resolve with:
```bash
jj resolve --tool=beads-merge
```

## Protected Branch Workflows

**If your repository uses protected branches** (GitHub, GitLab, etc.), bd can commit to a separate branch instead of `main`:

### Configuration

```bash
# Initialize with separate sync branch
bd init --branch beads-sync

# Or configure existing setup
bd config set sync.branch beads-sync
```

### How It Works

- Beads commits issue updates to `beads-sync` instead of `main`
- Uses git worktrees (lightweight checkouts) in `.git/beads-worktrees/`
- Your main working directory is never affected
- Periodically merge `beads-sync` back to `main` via pull request

### Daily Workflow (Unchanged for Agents)

```bash
# Agents work normally - no changes needed!
bd create "Fix authentication" -t bug -p 1
bd update bd-a1b2 --status in_progress
bd close bd-a1b2 "Fixed"
```

All changes automatically commit to `beads-sync` branch (if daemon is running with `--auto-commit`).

### Merging to Main (Humans)

```bash
# Check what's changed
bd sync --status

# Option 1: Create pull request
git push origin beads-sync
# Then create PR on GitHub/GitLab

# Option 2: Direct merge (if allowed)
bd sync --merge
```

### Benefits

- ✅ Works with protected `main` branches
- ✅ No disruption to agent workflows
- ✅ Platform-agnostic (works on any git platform)
- ✅ Backward compatible (opt-in via config)

See [PROTECTED_BRANCHES.md](PROTECTED_BRANCHES.md) for complete setup guide, troubleshooting, and examples.

## Git Hooks Integration

**STRONGLY RECOMMENDED:** Install git hooks for automatic sync and consistency.

### Installation

```bash
# One-time setup in each beads workspace
./examples/git-hooks/install.sh
```

### What Gets Installed

**pre-commit hook:**
- Flushes pending changes immediately before commit
- Bypasses 30-second debounce
- Guarantees JSONL is current

**post-merge hook:**
- Imports updated JSONL after pull/merge
- Guarantees database sync after remote changes

**pre-push hook:**
- Exports database to JSONL before push
- Prevents stale JSONL from reaching remote
- **Critical for multi-workspace consistency**

**post-checkout hook:**
- Imports updated JSONL after branch switches
- Ensures database reflects checked-out branch state

### Why Hooks Matter

**Without pre-push hook:**
- Database changes committed locally
- Stale JSONL pushed to remote
- Other workspaces diverge from truth

**With pre-push hook:**
- JSONL always reflects database state
- All workspaces stay synchronized
- No manual `bd sync` needed

See [examples/git-hooks/README.md](../examples/git-hooks/README.md) for details.

### Implementation Details

#### Hook Installation (`cmd/bd/hooks.go`)

The `installHooks()` function:
- Writes embedded hook scripts to the `.git/hooks/` directory
- Creates the hooks directory with `os.MkdirAll()` if needed
- Backs up existing hooks with `.backup` extension (unless `--force` flag used)
- Sets execute permissions (0755) on installed hooks
- Supports shared mode via `--shared` flag (installs to `.beads-hooks/` instead)

#### Git Directory Resolution

**Critical for worktree support:** The `getGitDir()` helper uses `git rev-parse --git-dir` to resolve the actual git directory:

```go
// Returns ".git" in normal repos
// Returns "/path/to/shared/.git" in git worktrees
// (where .git is a file containing "gitdir: /path/to/actual/git/dir")
gitDir, err := getGitDir()
```

In **normal repositories**, `.git` is a directory containing the git internals.
In **git worktrees**, `.git` is a file containing `gitdir: /path/to/actual/git/dir`, pointing to the shared git directory.

This difference breaks code that assumes `.git` is always a directory. Using `getGitDir()` ensures hooks work correctly in both cases.

#### Hook Detection (`cmd/bd/init.go`)

The `detectExistingHooks()` function scans for existing hooks and classifies them:

- **bd hooks**: Identified by "bd (beads) pre-commit hook" comment in content
- **pre-commit framework hooks**: Detected by "pre-commit framework" or "pre-commit.com" in content
- **Custom hooks**: Any other existing hook

This classification allows bd to:
- Avoid re-installing already-installed bd hooks
- Support chaining with pre-commit framework hooks
- Warn when overwriting custom hooks

#### Hook Testing

Tests in `hooks_test.go` and `init_hooks_test.go`:

1. Initialize real git repositories via `exec.Command("git", "init")`
2. Call `getGitDir()` to get the actual git directory path
3. Construct hooks path with `filepath.Join(gitDirPath, "hooks")`
4. Create hooks directory if needed with `os.MkdirAll()`
5. Execute hook operations and verify results

This approach ensures tests work correctly in both normal repos and git worktrees, preventing failures when running in worktree environments where `.git` is a file.

## Multi-Workspace Sync Strategies

### Centralized Repository Pattern

```
┌──────────────┐
│  Developer A │────┐
│  (Workspace) │    │
└──────────────┘    │
                    ▼
┌──────────────┐  ┌─────────────────┐
│  Developer B │─▶│ Central Repo    │
│  (Workspace) │  │ (.beads/*.jsonl)│
└──────────────┘  └─────────────────┘
                    ▲
┌──────────────┐    │
│  CI/CD       │────┘
│  (Workspace) │
└──────────────┘
```

**Best for:**
- Teams working on shared repository
- CI/CD integration
- Multi-agent workflows

**Key points:**
- Each workspace has its own daemon
- Git is the source of truth
- Auto-sync keeps workspaces consistent

### Fork-Based Pattern

```
┌──────────────┐      ┌─────────────────┐
│  OSS Contrib │─────▶│ Planning Repo   │
│  (Fork)      │      │ (.beads/*.jsonl)│
└──────────────┘      └─────────────────┘
       │
       │ PR
       ▼
┌─────────────────┐
│ Upstream Repo   │
│ (no .beads/)    │
└─────────────────┘
```

**Best for:**
- Open source contributors
- Solo developers
- Private task tracking on public repos

**Setup:**
```bash
bd init --contributor  # Interactive wizard
```

See [MULTI_REPO_MIGRATION.md](MULTI_REPO_MIGRATION.md) for complete guide.

### Team Branch Pattern

```
┌──────────────┐
│  Team Member │────┐
│  (main)      │    │
└──────────────┘    │
                    ▼
┌──────────────┐  ┌─────────────────┐
│  Team Member │─▶│ Shared Repo     │
│  (main)      │  │ (beads-sync)│
└──────────────┘  └─────────────────┘
```

**Best for:**
- Teams on protected branches
- Managed git workflows
- Review-before-merge policies

**Setup:**
```bash
bd init --team  # Interactive wizard
```

See [MULTI_REPO_MIGRATION.md](MULTI_REPO_MIGRATION.md) for complete guide.

## Sync Timing and Control

### Automatic Sync (Default)

**With daemon running:**
- Export to JSONL: 30-second debounce after changes
- Import from JSONL: when file is newer than DB
- Commit/push: configurable via `--auto-commit` / `--auto-push`

**30-second debounce provides transaction window:**
- Multiple changes within 30s get batched
- Single JSONL export/commit for the batch
- Prevents commit spam

### Manual Sync

```bash
# Force immediate sync (bypass debounce)
bd sync

# What it does:
# 1. Export pending changes to JSONL
# 2. Commit to git
# 3. Pull from remote
# 4. Import any updates
# 5. Push to remote
```

**ALWAYS run `bd sync` at end of agent sessions** to ensure changes are committed/pushed.

### Disable Automatic Sync

```bash
# Disable auto-flush (no export until manual sync)
bd --no-auto-flush ready

# Disable auto-import (no import on file changes)
bd --no-auto-import ready

# Disable both (manual sync only)
export BEADS_NO_DAEMON=1  # Direct mode
```

## Git Configuration Best Practices

### Recommended .gitignore

```
# bd database (not tracked - JSONL is source of truth)
.beads/beads.db
.beads/beads.db-*
.beads/bd.sock
.beads/bd.pipe

# bd daemon state
.beads/.exclusive-lock

# Git worktrees (if using protected branches)
.git/beads-worktrees/
```

### Recommended .gitattributes

**IMPORTANT:** The `.gitattributes` file should be **committed to git**, not ignored. It configures merge behavior for the entire team.

```
# Intelligent merge driver for JSONL (auto-configured by bd init)
.beads/issues.jsonl merge=beads

# Treat JSONL as text for diffs
.beads/*.jsonl text diff
```

This file is automatically created by `bd init` and is essential for:
- Preventing spurious merge conflicts in `.beads/issues.jsonl`
- Enabling field-level 3-way merging instead of line-by-line
- Ensuring all team members get intelligent JSONL merging

### Git LFS Considerations

**Do NOT use Git LFS for `.beads/issues.jsonl`:**
- JSONL needs intelligent merge (doesn't work with LFS)
- File size stays reasonable (<1MB per 10K issues)
- Text diffs are valuable for review

## Troubleshooting Git Issues

### Issue: "JSONL file is ahead of database"

**Symptoms:**
```
WARN Database timestamp older than JSONL, importing...
```

**Solutions:**
```bash
# Normal after git pull - auto-import handles it
# If stuck, force import:
bd import -i .beads/issues.jsonl
```

### Issue: "Database is ahead of JSONL"

**Symptoms:**
```
WARN JSONL timestamp older than database, exporting...
```

**Solutions:**
```bash
# Normal after local changes - auto-export handles it
# If stuck, force export:
bd sync
```

### Issue: Merge conflicts every time

**Symptoms:**
- Git merge always creates conflicts in `.beads/issues.jsonl`
- Merge driver not being used

**Solutions:**
```bash
# Check merge driver configured
git config merge.beads.driver

# Reinstall if missing
bd init --skip-db  # Only reconfigure git, don't touch database

# Verify .gitattributes
grep "issues.jsonl" .gitattributes
# Expected: .beads/issues.jsonl merge=beads
```

### Issue: Changes not syncing to other workspaces

**Symptoms:**
- Agent A creates issue
- Agent B doesn't see it after `git pull`

**Solutions:**
```bash
# Agent A: Ensure changes were pushed
bd sync
git push

# Agent B: Force import
git pull
bd import -i .beads/issues.jsonl

# Check git hooks installed (prevent future issues)
./examples/git-hooks/install.sh
```

## See Also

- [AGENTS.md](../AGENTS.md) - Main agent workflow guide
- [DAEMON.md](DAEMON.md) - Daemon management and configuration
- [PROTECTED_BRANCHES.md](PROTECTED_BRANCHES.md) - Protected branch workflows
- [MULTI_REPO_MIGRATION.md](MULTI_REPO_MIGRATION.md) - Multi-repo patterns
- [examples/git-hooks/README.md](../examples/git-hooks/README.md) - Git hooks integration
