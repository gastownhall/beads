# Uninstalling Beads

This guide explains how to completely remove Beads from a repository.

## Quick Uninstall

Run these commands from your repository root:

```bash
# 1. Stop any running daemon
bd daemons killall

# 2. Remove git hooks installed by Beads
rm -f .git/hooks/pre-commit .git/hooks/post-merge .git/hooks/pre-push .git/hooks/post-checkout

# 3. Remove merge driver config
git config --unset merge.beads.driver
git config --unset merge.beads.name

# 4. Remove .gitattributes entry (if only contains beads config)
# Or manually edit to remove the beads line
rm -f .gitattributes

# 5. Remove .beads directory
rm -rf .beads

# 6. Remove sync worktree (if exists)
rm -rf .git/beads-worktrees
```

## Detailed Steps

### 1. Stop the Daemon

If a Beads daemon is running for this repository, stop it first:

```bash
bd daemons list     # Check if daemon is running
bd daemons killall  # Stop all daemons
```

### 2. Remove Git Hooks

Beads installs these hooks in `.git/hooks/`:

| Hook | Purpose |
|------|---------|
| `pre-commit` | Syncs JSONL before commits |
| `post-merge` | Imports changes after merges |
| `pre-push` | Syncs before pushing |
| `post-checkout` | Imports after branch switches |

To remove them:

```bash
rm -f .git/hooks/pre-commit
rm -f .git/hooks/post-merge
rm -f .git/hooks/pre-push
rm -f .git/hooks/post-checkout
```

**Note:** If you had custom hooks before installing Beads, check for `.backup` files:
```bash
ls .git/hooks/*.backup
```

Restore any backups if needed:
```bash
mv .git/hooks/pre-commit.backup .git/hooks/pre-commit
```

### 3. Remove Merge Driver Configuration

Beads configures a custom merge driver in your git config:

```bash
git config --unset merge.beads.driver
git config --unset merge.beads.name
```

### 4. Remove .gitattributes Entry

Beads adds a line to `.gitattributes` for JSONL merge handling:

```
.beads/issues.jsonl merge=beads
```

Either remove the entire file (if it only contains this line):
```bash
rm -f .gitattributes
```

Or edit it to remove just the beads line:
```bash
# Edit .gitattributes and remove the line containing "merge=beads"
```

### 5. Remove .beads Directory

The `.beads/` directory contains:

| File/Dir | Description |
|----------|-------------|
| `beads.db` | SQLite database with issues |
| `issues.jsonl` | Git-tracked issue data |
| `daemon.pid` | Running daemon PID |
| `daemon.log` | Daemon logs |
| `daemon.lock` | Lock file for daemon |
| `bd.sock` | Unix socket for daemon IPC |
| `config.yaml` | Project configuration |
| `metadata.json` | Version tracking |
| `deletions.jsonl` | Soft-deleted issues |
| `README.md` | Human-readable overview |

Remove everything:
```bash
rm -rf .beads
```

**Warning:** This permanently deletes all issue data. Consider backing up first:
```bash
cp .beads/issues.jsonl ~/beads-backup-$(date +%Y%m%d).jsonl
```

### 6. Remove Sync Worktree

If you used branch sync features, clean up the worktree:

```bash
rm -rf .git/beads-worktrees
```

### 7. Commit the Removal (Optional)

If `.beads/` was tracked in git, commit its removal:

```bash
git add -A
git commit -m "Remove beads issue tracking"
git push
```

## Uninstalling the `bd` Binary

The `bd` command itself is a standalone binary. Remove it based on how you installed:

**If installed via go install:**
```bash
rm $(which bd)
# Or: rm ~/go/bin/bd
```

**If installed manually:**
```bash
# Remove from wherever you placed it
rm /usr/local/bin/bd
```

## Verify Complete Removal

Run these checks to confirm Beads is fully removed:

```bash
# Should show "command not found" or be a different bd
which bd

# Should not exist
ls .beads/

# Should not contain beads hooks
ls .git/hooks/

# Should not have merge driver
git config --get merge.beads.driver

# No .gitattributes or no beads line
cat .gitattributes
```

## Re-installing Later

To set up Beads again in the future:

```bash
bd init
```

This will recreate the `.beads/` directory, install hooks, and configure the merge driver.
