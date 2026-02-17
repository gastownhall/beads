# Git Integration Guide

**For:** AI agents and developers managing bd git workflows

## Overview

bd integrates with git for worktree support and protected branch workflows. The Dolt backend handles all data storage and versioning natively.

## Git Worktrees

Beads has comprehensive Git worktree compatibility with shared database architecture.

### How It Works

Git worktrees share the same `.git` directory and `.beads` database:
- All worktrees use the same `.beads/dolt/` database in the main repository
- Database discovery prioritizes main repository location
- Worktree-aware git operations prevent conflicts

### Worktree-Aware Features

**Database Discovery:**
- Searches main repository first for `.beads` directory
- Falls back to worktree-local search if needed
- Prevents database duplication across worktrees

**Git Operations:**
- Worktree-aware repository root detection
- Proper handling of git directory vs git common directory
- Safe concurrent access to shared database (use server mode for multi-writer)

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

- Works with protected `main` branches
- No disruption to agent workflows
- Platform-agnostic (works on any git platform)
- Backward compatible (opt-in via config)

See [PROTECTED_BRANCHES.md](PROTECTED_BRANCHES.md) for complete setup guide, troubleshooting, and examples.

## Git Hooks

### Installation

```bash
# Install hooks for JSONL export on commit
bd hooks install --beads
```

### What Gets Installed

**pre-commit hook:**
- Exports Dolt database to JSONL before commit
- Ensures JSONL stays current in git for portability

**post-merge hook:**
- Imports updated JSONL after pull/merge
- Keeps Dolt database current after remote changes

### Hook Implementation Details

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
gitDir, err := getGitDir()
```

In **normal repositories**, `.git` is a directory containing the git internals.
In **git worktrees**, `.git` is a file containing `gitdir: /path/to/actual/git/dir`.

#### Hook Detection (`cmd/bd/init.go`)

The `detectExistingHooks()` function scans for existing hooks and classifies them:

- **bd hooks**: Identified by "bd (beads) pre-commit hook" comment in content
- **pre-commit framework hooks**: Detected by "pre-commit framework" or "pre-commit.com" in content
- **Custom hooks**: Any other existing hook

## Multi-Workspace Sync

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

### Team Branch Pattern

```
┌──────────────┐
│  Team Member │────┐
│  (main)      │    │
└──────────────┘    │
                    ▼
┌──────────────┐  ┌─────────────────┐
│  Team Member │─▶│ Shared Repo     │
│  (main)      │  │ (beads-sync)    │
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

## Git Configuration Best Practices

### Recommended .gitignore

```
# Dolt database (not tracked in git)
.beads/dolt/

# Lock files
.beads/dolt-access.lock
```

### Git LFS Considerations

**Do NOT use Git LFS for `.beads/issues.jsonl`:**
- File size stays reasonable (<1MB per 10K issues)
- Text diffs are valuable for review

## See Also

- [AGENTS.md](../AGENTS.md) - Main agent workflow guide
- [PROTECTED_BRANCHES.md](PROTECTED_BRANCHES.md) - Protected branch workflows
- [MULTI_REPO_MIGRATION.md](MULTI_REPO_MIGRATION.md) - Multi-repo patterns
