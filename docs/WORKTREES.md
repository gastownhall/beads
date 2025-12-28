# Git Worktrees Guide

**Enhanced Git worktree compatibility for Beads issue tracking**

## Overview

Beads now provides **enhanced Git worktree support** with a shared database architecture. All worktrees in a repository share the same `.beads` database located in the main repository, enabling seamless issue tracking across multiple working directories.

**Note:** While comprehensively implemented and tested internally, this feature may benefit from real-world usage feedback to identify any remaining edge cases.

---

## Beads-Created Worktrees (Sync Branch)

**Important:** Beads automatically creates git worktrees internally for its sync-branch feature. This is different from user-created worktrees for parallel development.

### Why Beads Creates Worktrees

When you configure a **sync branch** (via `bd init --branch <name>` or `bd config set sync.branch <name>`), beads needs to commit issue updates to that branch without switching your working directory away from your current branch.

**Solution:** Beads creates a lightweight worktree that:
- Contains only the `.beads/` directory (sparse checkout)
- Lives in `.git/beads-worktrees/<sync-branch>/`
- Commits issue changes to the sync branch automatically
- Leaves your main working directory untouched

### Where to Find These Worktrees

```
your-project/
├── .git/
│   ├── beads-worktrees/          # Beads-created worktrees live here
│   │   └── beads-sync/           # Default sync branch worktree
│   │       └── .beads/
│   │           └── issues.jsonl  # Issue data committed here
│   └── worktrees/                # Standard git worktrees directory
├── .beads/                       # Your working copy
│   ├── beads.db                  # Local SQLite database
│   └── issues.jsonl              # Local JSONL (may differ from sync branch)
└── src/                          # Your code (untouched by sync)
```

### Common Confusion: "Beads took over main!"

If you see worktrees pointing to `main` and can't switch branches normally, this is likely because:

1. Your sync branch was created from `main`
2. Beads created a worktree for that branch
3. Git worktrees lock branches they're checked out to

**Symptoms:**
```bash
$ git checkout main
fatal: 'main' is already checked out at '/path/to/.git/beads-worktrees/beads-sync'
```

**Quick Fix:**
```bash
# Remove the beads worktree
rm -rf .git/beads-worktrees

# Prune stale worktree references
git worktree prune

# Also remove any stray worktrees in .git/worktrees (older versions)
rm -rf .git/worktrees/beads-*
git worktree prune
```

### Disabling Sync Branch (Remove Worktrees)

If you don't want beads to use a separate sync branch:

```bash
# Unset the sync branch configuration
bd config set sync.branch ""

# Stop and restart daemon
bd daemon stop
bd daemon --start

# Clean up existing worktrees
rm -rf .git/beads-worktrees
git worktree prune
```

### Checking Your Sync Branch Configuration

```bash
# See current sync branch setting
bd config get sync.branch

# Check if worktrees exist
ls -la .git/beads-worktrees/ 2>/dev/null || echo "No beads worktrees"
ls -la .git/worktrees/ 2>/dev/null || echo "No standard worktrees"

# List all git worktrees
git worktree list
```

### See Also

For complete sync-branch documentation, see [PROTECTED_BRANCHES.md](PROTECTED_BRANCHES.md).

---

## How It Works

### Shared Database Architecture

```
Main Repository
├── .git/                    # Shared git directory
├── .beads/                  # Shared database (main repo)
│   ├── beads.db            # SQLite database
│   ├── issues.jsonl        # Issue data (git-tracked)
│   └── config.yaml         # Configuration
├── feature-branch/         # Worktree 1
│   └── (code files only)
└── bugfix-branch/          # Worktree 2
    └── (code files only)
```

**Key points:**
- ✅ **One database** - All worktrees share the same `.beads` directory in main repo
- ✅ **Automatic discovery** - Database found regardless of which worktree you're in
- ✅ **Concurrent access** - SQLite locking prevents corruption
- ✅ **Git integration** - Issues sync via JSONL in main repo

### Worktree Detection & Daemon Safety

bd automatically detects when you're in a git worktree and handles daemon mode safely:

**Default behavior (no sync-branch configured):**
- Daemon is **automatically disabled** in worktrees
- Uses direct mode for safety (no warning needed)
- All commands work correctly without configuration

**With sync-branch configured:**
- Daemon is **enabled** in worktrees
- Commits go to dedicated sync branch (e.g., `beads-sync`)
- Full daemon functionality available across all worktrees

## Usage Patterns

### Recommended: Configure Sync-Branch for Full Daemon Support

```bash
# Configure sync-branch once (in main repo or any worktree)
bd config set sync-branch beads-sync

# Now daemon works safely in all worktrees
cd feature-worktree
bd create "Implement feature X" -t feature -p 1
bd update bd-a1b2 --status in_progress
bd ready  # Daemon auto-syncs to beads-sync branch
```

### Alternative: Direct Mode (No Configuration Needed)

```bash
# Without sync-branch, daemon is auto-disabled in worktrees
cd feature-worktree
bd create "Implement feature X" -t feature -p 1
bd ready  # Uses direct mode automatically
bd sync   # Manual sync when needed
```

### Legacy: Explicit Daemon Disable

```bash
# Still works if you prefer explicit control
export BEADS_NO_DAEMON=1
# or
bd --no-daemon ready
```

## Worktree-Aware Features

### Database Discovery

bd intelligently finds the correct database:

1. **Priority search**: Main repository `.beads` directory first
2. **Fallback logic**: Searches worktree if main repo doesn't have database
3. **Path resolution**: Handles symlinks and relative paths correctly
4. **Validation**: Ensures `.beads` contains actual project files

### Git Hooks Integration

Pre-commit hooks adapt to worktree context:

```bash
# In main repo: Stages JSONL normally
git add .beads/issues.jsonl

# In worktree: Safely skips staging (files outside working tree)
# Hook detects context and handles appropriately
```

### Sync Operations

Worktree-aware sync operations:

- **Repository root detection**: Uses `git rev-parse --show-toplevel` for main repo
- **Git directory handling**: Distinguishes between `.git` (file) and `.git/` (directory)
- **Path resolution**: Converts between worktree and main repo paths
- **Concurrent safety**: SQLite locking prevents corruption

## Setup Examples

### Basic Worktree Setup

```bash
# Create main worktree
git worktree add main-repo

# Create feature worktree
git worktree add feature-worktree

# Initialize beads in main repo
cd main-repo
bd init

# Worktrees automatically share the database
cd ../feature-worktree
bd ready  # Works immediately - sees same issues
```

### Multi-Feature Development

```bash
# Main development
cd main-repo
bd create "Epic: User authentication" -t epic -p 1
# Returns: bd-a3f8e9

# Feature branch worktree
git worktree add auth-feature
cd auth-feature
bd create "Design login UI" -p 1
# Auto-assigned: bd-a3f8e9.1 (child of epic)

# Bugfix worktree
git worktree add auth-bugfix
cd auth-bugfix
bd create "Fix password validation" -t bug -p 0
# Auto-assigned: bd-f14c3
```

## Troubleshooting

### Issue: "Branch already checked out" error

**Symptoms:**
```bash
$ git checkout main
fatal: 'main' is already checked out at '/path/to/.git/beads-worktrees/beads-sync'
```

**Cause:** Beads created a worktree for its sync branch feature, and that worktree has your target branch checked out. Git doesn't allow the same branch to be checked out in multiple worktrees.

**Solution:**
```bash
# Remove beads worktrees
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*

# Clean up git's worktree registry
git worktree prune

# Now you can checkout the branch
git checkout main
```

**Prevention:** If you use trunk-based development and don't need a separate sync branch, disable it:
```bash
bd config set sync.branch ""
```

### Issue: Unexpected worktree directories appeared

**Symptoms:** You notice `.git/beads-worktrees/` or entries in `.git/worktrees/` that you didn't create.

**Cause:** Beads automatically creates worktrees when using the sync-branch feature (configured via `bd init --branch` or `bd config set sync.branch`).

**Solution:** See [Beads-Created Worktrees](#beads-created-worktrees-sync-branch) section above for details on what these are and how to remove them if unwanted.

### Issue: Daemon commits to wrong branch

**Symptoms:** Changes appear on unexpected branch in git history

**Note:** This issue should no longer occur with the new worktree safety feature. Daemon is automatically disabled in worktrees unless sync-branch is configured.

**Solution (if still occurring):**
```bash
# Option 1: Configure sync-branch (recommended)
bd config set sync-branch beads-sync

# Option 2: Explicitly disable daemon
export BEADS_NO_DAEMON=1
# Or use --no-daemon flag for individual commands
bd --no-daemon sync
```

### Issue: Database not found in worktree

**Symptoms:** `bd: database not found` error

**Solutions:**
```bash
# Ensure main repo has .beads directory
cd main-repo
ls -la .beads/

# Re-run bd init if needed
bd init

# Check worktree can access main repo
cd ../worktree-name
bd info  # Should show database path in main repo
```

### Issue: Multiple databases detected

**Symptoms:** Warning about multiple `.beads` directories

**Solution:**
```bash
# bd shows warning with database locations
# Typically, the closest database (in main repo) is correct
# Remove extra .beads directories if they're not needed
```

### Issue: Git hooks fail in worktrees

**Symptoms:** Pre-commit hook errors about staging files outside working tree

**Solution:** This is now automatically handled. The hook detects worktree context and adapts its behavior. No manual intervention needed.

## Advanced Configuration

### Environment Variables

```bash
# Disable daemon globally for worktree usage
export BEADS_NO_DAEMON=1

# Disable auto-start (still warns if manually started)
export BEADS_AUTO_START_DAEMON=false

# Force specific database location
export BEADS_DB=/path/to/specific/.beads/beads.db
```

### Configuration Options

```bash
# Configure sync behavior
bd config set sync.branch beads-sync  # Use separate sync branch
bd config set sync.auto_commit true       # Auto-commit changes
bd config set sync.auto_push true         # Auto-push changes
```

## Performance Considerations

### Database Sharing Benefits

- **Reduced overhead**: One database instead of per-worktree copies
- **Instant sync**: Changes visible across all worktrees immediately
- **Memory efficient**: Single SQLite instance vs multiple
- **Git efficient**: One JSONL file to track vs multiple

### Concurrent Access

- **SQLite locking**: Prevents corruption during simultaneous access
- **Git operations**: Safe concurrent commits from different worktrees
- **Sync coordination**: JSONL-based sync prevents conflicts

## Migration from Limited Support

### Before (Limited Worktree Support)

- ❌ Daemon mode broken in worktrees
- ❌ Manual workarounds required
- ❌ Complex setup procedures
- ❌ Limited documentation

### After (Enhanced Worktree Support)

- ✅ Shared database architecture
- ✅ Automatic worktree detection
- ✅ Clear user guidance and warnings
- ✅ Comprehensive documentation
- ✅ Git hooks work correctly
- ✅ All bd commands function properly

**Note:** Based on comprehensive internal testing. Real-world usage may reveal additional refinements needed.

## Examples in the Wild

### Monorepo Development

```bash
# Monorepo with multiple service worktrees
git worktree add services/auth
git worktree add services/api
git worktree add services/web

# Each service team works in their worktree
cd services/auth
export BEADS_NO_DAEMON=1
bd create "Add OAuth support" -t feature -p 1

cd ../api
bd create "Implement auth endpoints" -p 1
# Issues automatically linked and visible across worktrees
```

### Feature Branch Workflow

```bash
# Create feature worktree
git worktree add feature/user-profiles
cd feature/user-profiles

# Work on feature with full issue tracking
bd create "Design user profile schema" -t task -p 1
bd create "Implement profile API" -t task -p 1
bd create "Add profile UI components" -t task -p 2

# Issues tracked in shared database
# Code changes isolated to worktree
# Clean merge back to main when ready
```

## Fully Separate Beads Repository

For users who want complete separation between code history and issue tracking, beads supports storing issues in a completely separate git repository.

### Why Use a Separate Repo?

- **Clean code history** - No beads commits polluting your project's git log
- **Shared across worktrees** - All worktrees can use the same BEADS_DIR
- **Platform agnostic** - Works even if your main project isn't git-based
- **Monorepo friendly** - Single beads repo for multiple projects

### Setup

```bash
# 1. Create a dedicated beads repository (one-time)
mkdir ~/my-project-beads
cd ~/my-project-beads
git init
bd init --prefix myproj

# 2. Add a remote for cross-machine sync (optional)
git remote add origin git@github.com:you/my-project-beads.git
git push -u origin main
```

### Usage

Set `BEADS_DIR` to point at your separate beads repository:

```bash
cd ~/my-project
export BEADS_DIR=~/my-project-beads/.beads

# All bd commands now use the separate repo
bd create "My task" -t task
bd list
bd sync  # commits to ~/my-project-beads, pushes there
```

### Making It Permanent

**Option 1: Shell profile**
```bash
# Add to ~/.bashrc or ~/.zshrc
export BEADS_DIR=~/my-project-beads/.beads
```

**Option 2: direnv (per-project)**
```bash
# In ~/my-project/.envrc
export BEADS_DIR=~/my-project-beads/.beads
```

**Option 3: Wrapper script**
```bash
# ~/bin/bd-myproj
#!/bin/bash
BEADS_DIR=~/my-project-beads/.beads exec bd "$@"
```

### How It Works

When `BEADS_DIR` points to a different git repository than your current directory:

1. `bd sync` detects "External BEADS_DIR"
2. Git operations (add, commit, push, pull) target the beads repo
3. Your code repository is never touched

This was contributed by @dand-oss in [PR #533](https://github.com/steveyegge/beads/pull/533).

### Combining with Worktrees

This approach elegantly solves the worktree isolation problem:

```bash
# All worktrees share the same external beads repo
export BEADS_DIR=~/project-beads/.beads

cd ~/project/main       && bd list  # Same issues
cd ~/project/feature-1  && bd list  # Same issues
cd ~/project/feature-2  && bd list  # Same issues
```

No daemon conflicts, no branch confusion - all worktrees see the same issues because they all use the same external repository.

## See Also

- [GIT_INTEGRATION.md](GIT_INTEGRATION.md) - General git integration guide
- [AGENTS.md](../AGENTS.md) - Agent usage instructions
- [README.md](../README.md) - Main project documentation
- [MULTI_REPO_MIGRATION.md](MULTI_REPO_MIGRATION.md) - Multi-workspace patterns