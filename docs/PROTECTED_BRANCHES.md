# Protected Branch Workflow

This guide explains how to use beads with protected branches on platforms like GitHub, GitLab, and Bitbucket.

## Table of Contents

- [Overview](#overview)
- [Quick Start](#quick-start)
- [How It Works](#how-it-works)
- [Setup](#setup)
- [Daily Workflow](#daily-workflow)
- [Merging Changes](#merging-changes)
- [Troubleshooting](#troubleshooting)
- [FAQ](#faq)

## Overview

**Problem:** GitHub and other platforms let you protect branches (like `main`) to require pull requests for all changes. This prevents beads from auto-committing issue updates directly to `main`.

**Solution:** Beads can commit to a separate branch (like `beads-sync`) using git worktrees, while keeping your main working directory untouched. Periodically merge the metadata branch back to `main` via a pull request.

**Benefits:**
- ✅ Works with any git platform's branch protection
- ✅ Main branch stays protected
- ✅ No disruption to your primary working directory
- ✅ Backward compatible (opt-in via config)
- ✅ Minimal disk overhead (uses sparse checkout)
- ✅ Platform-agnostic solution

## Quick Start

**1. Initialize beads with a separate sync branch:**

```bash
cd your-project
bd init --branch beads-sync
```

This creates a `.beads/` directory and configures beads to commit to `beads-sync` instead of `main`.

**Important:** After initialization, you'll see some untracked files that should be committed to your protected branch:

```bash
# Check what files were created
git status

# Commit the beads configuration to your protected branch
git add .beads/.gitignore .gitattributes
git commit -m "Initialize beads issue tracker"
git push origin main  # Or create a PR if required
```

**Files created by `bd init --branch`:**

Files that should be committed to your protected branch (main):
- `.beads/.gitignore` - Tells git what to ignore in .beads/ directory
- `.gitattributes` - Configures merge driver for beads data

Files that are automatically gitignored (do NOT commit):
- `.beads/dolt/` - Dolt database directory (local only)
- `.beads/dolt/sql-server.pid`, `sql-server.log` - Dolt server runtime files

The sync branch (beads-sync) will contain:
- `.beads/metadata.json` - Metadata about the beads installation
- `.beads/config.yaml` - Configuration template (optional)

**2. Start the Dolt server:**

```bash
dolt sql-server
```

With git hooks installed (`bd hooks install`), issue changes are automatically committed to the `beads-sync` branch.

**3. When ready, merge to main:**

```bash
# Check what's changed
bd dolt show

# Merge to main
git merge beads-sync
```

That's it! The complete workflow is described below.

## How It Works

### Git Worktrees

Beads uses [git worktrees](https://git-scm.com/docs/git-worktree) to maintain a lightweight checkout of your sync branch. Think of it as a mini git clone that shares the same repository history.

**Directory structure:**

```
your-project/
├── .git/                    # Main git directory
│   └── beads-worktrees/
│       └── beads-sync/  # Worktree (only .beads/ checked out)
│           └── .beads/
│               └── dolt/
├── .beads/                  # Your main copy
│   ├── dolt/
│   └── .gitignore
├── .gitattributes           # Merge driver config (in main branch)
└── src/                     # Your code (untouched)
```

**What lives in each branch:**

Main branch (protected):
- `.beads/.gitignore` - Tells git what to ignore
- `.gitattributes` - Merge driver configuration

Sync branch (beads-sync):
- `.beads/metadata.json` - Repository metadata
- `.beads/config.yaml` - Configuration template

Not tracked (gitignored):
- `.beads/dolt/` - Dolt database directory (local only)
- `.beads/dolt/sql-server.*` - Dolt server runtime files

**Key points:**
- The worktree is in `.git/beads-worktrees/` (hidden from your workspace)
- Only `.beads/` is checked out in the worktree (sparse checkout)
- Changes to issues are committed in the worktree
- Your main working directory is never affected
- Disk overhead is minimal (~few MB for the worktree)

### Automatic Sync

When you update an issue:

1. Issue is updated in the Dolt database (`.beads/dolt/`)
2. Dolt automatically commits the change to its version history
3. Changes are synced to remotes via `bd dolt push` or `bd sync`
4. Main branch stays untouched (no commits on `main`)

## Setup

### Option 1: Initialize New Project

```bash
cd your-project
bd init --branch beads-sync
```

This will:
- Create `.beads/` directory with database
- Set `sync.branch` config to `beads-sync`
- Import any existing issues from git (if present)
- Prompt to install git hooks (recommended: say yes)

### Option 2: Migrate Existing Project

If you already have beads set up and want to switch to a separate branch:

```bash
# Set the sync branch
bd config set sync.branch beads-sync

# Start the Dolt server and install git hooks
bd dolt start
bd hooks install
```

### Sync Configuration

For automatic commits to the sync branch, install git hooks:

```bash
bd hooks install
```

Git hooks help maintain sync consistency. Use `bd sync` for manual sync when needed.

### Environment Variables

You can also configure the sync branch via environment variable:

```bash
export BEADS_SYNC_BRANCH=beads-sync
```

This is useful for CI/CD or temporary overrides.

## Daily Workflow

### For AI Agents

AI agents work exactly the same way as before:

```bash
# Create issues
bd create "Implement user authentication" -t feature -p 1

# Update issues
bd update bd-a1b2 --claim

# Close issues
bd close bd-a1b2 "Completed authentication"
```

All changes are automatically committed to the `beads-sync` branch via git hooks. No changes are needed to agent workflows!

### For Humans

**Check status:**

```bash
# See what's changed on the sync branch
bd dolt show
```

This shows the current Dolt configuration and connection status.

**Manual commit:**

```bash
bd dolt commit  # Commit pending changes
```

**Pull changes from remote:**

```bash
# Pull updates from other collaborators
bd sync --no-push
```

This pulls changes from the remote sync branch and imports them to your local database.

## Merging Changes

### Option 1: Via Pull Request (Recommended)

For protected branches with required reviews:

```bash
# 1. Push your sync branch
git push origin beads-sync

# 2. Create PR on GitHub/GitLab/etc.
#    - Base: main
#    - Compare: beads-sync

# 3. After PR is merged, update your local main
git checkout main
git pull
bd import  # Import the merged changes
```

### Option 2: Direct Merge (If Allowed)

If you have push access to `main`:

```bash
# Check what will be merged
git log main..beads-sync --oneline

# Merge sync branch to main
git checkout main
git merge beads-sync --no-ff
git push
bd import  # Import merged changes to database
```

**Safety checks:**
- ✅ Verifies you're not on the sync branch
- ✅ Checks for uncommitted changes in working tree
- ✅ Detects merge conflicts and provides resolution steps
- ✅ Uses `--no-ff` for clear history

### Merge Conflicts

If you encounter conflicts during merge:

```bash
# git merge may detect conflicts and show:
Auto-merging .beads/...
CONFLICT (content): Merge conflict in .beads/...

To resolve:
1. Use bd vc conflicts to view conflicts
2. Resolve conflicts
3. Commit the resolution
```

**Resolving merge conflicts:**

Dolt handles merge conflicts natively with cell-level merge. When concurrent changes affect the same issue field, Dolt detects the conflict and allows resolution:

```bash
# After a Dolt pull with conflicts
bd vc conflicts     # View conflicts
bd vc resolve       # Resolve conflicts
```

## Troubleshooting

### "fatal: refusing to merge unrelated histories"

This happens if you created the sync branch independently. Merge with `--allow-unrelated-histories`:

```bash
git merge beads-sync --allow-unrelated-histories --no-ff
```

Or merge manually with `git merge beads-sync --allow-unrelated-histories --no-ff`.

### "worktree already exists"

If the worktree is corrupted or in a bad state:

```bash
# Remove the worktree
rm -rf .git/beads-worktrees/beads-sync

# Prune stale worktree entries
git worktree prune

# Restart Dolt server (it will recreate the worktree)
bd dolt stop && bd dolt start
```

### "branch 'beads-sync' not found"

The sync branch doesn't exist yet. It will be created on the first commit. Create it manually:

```bash
git checkout -b beads-sync
git checkout main  # Switch back
```

### "Cannot push to protected branch"

If the sync branch itself is protected:

1. **Option 1:** Unprotect the sync branch (it's metadata, doesn't need protection)
2. **Option 2:** Use `--auto-commit` without `--auto-push`, and push manually when ready
3. **Option 3:** Use a different branch name that's not protected

### Dolt server won't start

Check server status and logs:

```bash
# Check status
bd dolt status

# View logs
tail -f .beads/dolt/sql-server.log

# Restart server
bd dolt stop && bd dolt start
```

Common issues:
- Port already in use: Another Dolt server is running
- Permission denied: Check `.beads/` directory permissions
- Git errors: Ensure git is installed and repository is initialized

### Changes not syncing between clones

Ensure all clones are configured the same way:

```bash
# On each clone, verify:
bd config get sync.branch  # Should be the same (e.g., beads-sync)

# Pull latest changes
bd sync --no-push

# Check Dolt server is running
bd dolt status
```

## FAQ

### Do I need to configure anything on GitHub/GitLab?

No! This is a pure git solution that works on any platform. Just protect your `main` branch as usual.

### Can I use a different branch name?

Yes! Use any branch name except `main` or `master` (git worktrees cannot checkout the same branch in multiple locations):

```bash
bd init --branch my-custom-branch
# or
bd config set sync.branch my-custom-branch
```

### Can I change the branch name later?

Yes:

```bash
bd config set sync.branch new-branch-name
bd dolt stop && bd dolt start
```

The old worktree will remain (no harm), and a new worktree will be created for the new branch.

### What if I want to go back to committing to main?

Unset the sync branch config:

```bash
bd config set sync.branch ""
bd dolt stop && bd dolt start
```

Beads will go back to committing directly to your current branch.

### Does this work with multiple collaborators?

Yes! Each collaborator configures their own sync branch:

```bash
# All collaborators use the same branch
bd config set sync.branch beads-sync
```

Everyone's changes sync via the `beads-sync` branch. Periodically merge to `main` via PR.

### How often should I merge to main?

This depends on your workflow:

- **Daily:** If you want issue history in `main` frequently
- **Per sprint:** If you batch metadata updates
- **As needed:** Only when you need others to see issue updates

There's no "right" answer - choose what fits your team.

### Can I review changes before merging?

Yes! Use `bd dolt show` to check current status, or use git to compare branches:

```bash
git log main..beads-sync --oneline
# Shows commits on beads-sync not yet in main
```

Or create a pull request and review on GitHub/GitLab.

### What about disk space?

Worktrees are very lightweight:
- Sparse checkout means only `.beads/` is checked out
- Typically < 1 MB for the worktree
- Shared git history (no duplication)

### Can I delete the worktree?

Yes, but it may be recreated on next sync. If you want to clean up permanently:

```bash
# Stop Dolt server
bd dolt stop

# Remove worktree
git worktree remove .git/beads-worktrees/beads-sync

# Unset sync branch
bd config set sync.branch ""
```

### Does this work with `bd sync`?

Yes! `bd sync` works normally and includes special commands for the merge workflow:

- `bd dolt show` - Show current Dolt configuration and connection status
- `git merge beads-sync` - Merge sync branch to main

### Can AI agents merge automatically?

Not recommended! Merging to `main` is a deliberate action that should be human-reviewed, especially with protected branches. Agents should create issues and update them; humans should merge to `main`.

However, if you want fully automated sync:

```bash
# WARNING: This bypasses branch protection!
git merge beads-sync  # Run periodically (e.g., via cron)
```

### What if I forget to merge for a long time?

No problem! The sync branch accumulates all changes. When you eventually merge:

```bash
git checkout main
git merge beads-sync --no-ff
git push
```

All accumulated changes will be merged at once. Git history will show the full timeline.

### Can I use this with GitHub Actions or CI/CD?

Yes! Example GitHub Actions workflow:

```yaml
name: Sync Beads Metadata

on:
  schedule:
    - cron: '0 0 * * *'  # Daily at midnight
  workflow_dispatch:     # Manual trigger

jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
        with:
          fetch-depth: 0  # Full history

      - name: Install bd
        run: |
          curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

      - name: Pull changes
        run: |
          git fetch origin beads-sync
          bd sync --no-push

      - name: Merge to main (if changes)
        run: |
          if git log main..beads-sync --oneline | grep -q '.'; then
            git merge beads-sync --no-ff -m "Merge beads-sync metadata"
            git push origin main
          fi
```

**Note:** Make sure the GitHub Action has write permissions to push to `main`.

## Platform-Specific Notes

### GitHub

Protected branch settings:
1. Go to Settings → Branches → Add rule
2. Branch name pattern: `main`
3. Check "Require pull request before merging"
4. Save

Create sync branch PR:
```bash
git push origin beads-sync
gh pr create --base main --head beads-sync --title "Update beads metadata"
```

### GitLab

Protected branch settings:
1. Settings → Repository → Protected Branches
2. Branch: `main`
3. Allowed to merge: Maintainers
4. Allowed to push: No one

Create sync branch MR:
```bash
git push origin beads-sync
glab mr create --source-branch beads-sync --target-branch main
```

### Bitbucket

Protected branch settings:
1. Repository settings → Branch permissions
2. Branch: `main`
3. Check "Prevent direct pushes"

Create sync branch PR:
```bash
git push origin beads-sync
# Create PR via Bitbucket web UI
```

## Advanced Topics

### Multiple Sync Branches

You can use different sync branches for different purposes:

```bash
# Development branch
bd config set sync.branch beads-dev

# Production branch
bd config set sync.branch beads-prod
```

Switch between them as needed.

### Syncing with Upstream

If you're working on a fork:

```bash
# Add upstream
git remote add upstream https://github.com/original/repo.git

# Fetch upstream changes
git fetch upstream

# Merge upstream beads-sync to yours
git checkout beads-sync
git merge upstream/beads-sync
bd import  # Import merged changes
```

### Custom Worktree Location

By default, worktrees are in `.git/beads-worktrees/`. This is hidden and automatic. If you need a custom location, you'll need to manage worktrees manually (not recommended).

## Migration Guide

### From Direct Commits to Sync Branch

If you have an existing beads setup committing to `main`:

1. **Set sync branch:**
   ```bash
   bd config set sync.branch beads-sync
   ```

2. **Restart Dolt server:**
   ```bash
   bd dolt stop && bd dolt start
   ```

3. **Verify:**
   ```bash
   bd config get sync.branch  # Should show: beads-sync
   ```

Future commits will go to `beads-sync`. Historical commits on `main` are preserved.

### From Sync Branch to Direct Commits

If you want to stop using a sync branch:

1. **Unset sync branch:**
   ```bash
   bd config set sync.branch ""
   ```

2. **Restart Dolt server:**
   ```bash
   bd dolt stop && bd dolt start
   ```

Future commits will go to your current branch (e.g., `main`).

---

**Need help?** Open an issue at https://github.com/steveyegge/beads/issues
