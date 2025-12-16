# bd Git Hooks

This directory contains git hooks that integrate bd (beads) with your git workflow, preventing stale JSONL from being pushed to remote.

## The Problem

Two race conditions can occur:

1. **Between operations and commits**: Daemon auto-flush (5s debounce) may fire after commit
   - User closes issue via MCP → daemon schedules flush (5 sec delay)
   - User commits code changes → JSONL appears clean
   - Daemon flush fires → JSONL modified after commit
   - Result: dirty working tree showing JSONL changes

2. **Between commits and pushes**: Changes made after commit but before push (bd-my64)
   - User commits → pre-commit hook flushes JSONL
   - User adds comments or updates issues
   - User pushes → outdated JSONL is pushed
   - Result: remote has stale JSONL

## The Solution

These git hooks ensure bd changes are always synchronized with your commits and pushes:

- **pre-commit** - Flushes pending bd changes to JSONL before commit and stages it
- **pre-push** - Blocks push if JSONL has uncommitted changes (bd-my64)
- **post-merge** - Imports updated JSONL after git pull/merge

## Installation

### Quick Install (Recommended)

Use `bd hooks install` to install hooks automatically:

```bash
bd hooks install
```

Alternatively, use `bd init --quiet` which installs hooks during initialization.

**Hook Chaining (New in v0.23):** If you already have git hooks installed (e.g., pre-commit framework), bd will:
- Detect existing hooks
- Offer to chain with them (recommended)
- Preserve your existing hooks while adding bd functionality
- Back up hooks if you choose to overwrite

This prevents bd from silently overwriting workflows like pre-commit framework, which previously caused test failures to slip through.

The installer will:
- Copy hooks to `.git/hooks/`
- Make them executable
- Detect and preserve existing hooks

### Shared Hooks for Teams (New in v0.24.3)

For teams that need to share hooks across members (especially when using pre-built containers or CI/CD):

```bash
bd hooks install --shared
```

This installs hooks to `.beads-hooks/` (a versioned directory) instead of `.git/hooks/`, and configures git to use them via `git config core.hooksPath .beads-hooks`.

**Benefits:**
- ✅ Hooks are versioned and can be committed to your repository
- ✅ Team members get hooks automatically when they clone/pull
- ✅ Security teams can scan and audit hook contents before deployment
- ✅ Works with pre-built containers (hooks are already in the repo)
- ✅ Hooks stay in sync when you run `bd hooks install --shared` after upgrades

**Use cases:**
- Teams building containers in CI that need hooks pre-installed
- Organizations requiring security scanning of all code (including hooks)
- Projects where consistent tooling across team members is critical
- Devcontainer workflows where bd is installed during container build

After running `bd hooks install --shared`, commit `.beads-hooks/` to your repository:

```bash
git add .beads-hooks/
git commit -m "Add bd git hooks for team"
```

### Manual Install

```bash
cp examples/git-hooks/pre-commit .git/hooks/pre-commit
cp examples/git-hooks/pre-push .git/hooks/pre-push
cp examples/git-hooks/post-merge .git/hooks/post-merge
chmod +x .git/hooks/pre-commit .git/hooks/pre-push .git/hooks/post-merge
```

## How It Works

### pre-commit

Before each commit, the hook runs:

```bash
bd sync --flush-only
```

This:
1. Exports any pending database changes to `.beads/issues.jsonl`
2. Stages the JSONL file if modified
3. Allows the commit to proceed with clean state

The hook is silent on success, fast (no git operations), and safe (fails commit if flush fails).

### pre-push

Before each push, the hook:

```bash
bd sync --flush-only  # Flush pending changes (if bd available)
git status --porcelain .beads/*.jsonl  # Check for uncommitted changes
```

This prevents pushing stale JSONL by:
1. Flushing pending in-memory changes from daemon's 5s debounce
2. Checking for uncommitted changes (staged, unstaged, untracked, deleted)
3. Failing the push with clear error message if changes exist
4. Instructing user to commit JSONL before pushing again

This solves bd-my64: changes made between commit and push (or pending debounced flushes) are caught before reaching remote.

### post-merge

After a git pull or merge, the hook runs:

```bash
bd import -i .beads/issues.jsonl
```

This ensures your local database reflects the merged state. The hook:
- Only runs if `.beads/issues.jsonl` exists (also checks `issues.jsonl` for backward compat)
- Imports any new issues or updates from the merge
- Warns on failure but doesn't block the merge

**Note:** With hash-based IDs (v0.20.1+), ID collisions don't occur - different issues get different hash IDs.

## Compatibility

- **Auto-sync**: Works alongside bd's automatic 5-second debounce
- **Direct mode**: Hooks work in both daemon and `--no-daemon` mode
- **Worktrees**: Safe to use with git worktrees

## Benefits

✅ No more dirty working tree after commits  
✅ Database always in sync with git  
✅ Automatic collision resolution on merge  
✅ Fast and silent operation  
✅ Optional - manual `bd sync` still works  

## Uninstall

Remove the hooks:

```bash
rm .git/hooks/pre-commit .git/hooks/pre-push .git/hooks/post-merge
```

Your backed-up hooks (if any) are in `.git/hooks/*.backup-*`.

## Related

- See [bd-51](../../.beads/bd-51) for the race condition bug report
- See [AGENTS.md](../../AGENTS.md) for the full git workflow
- See [examples/](../) for other integrations
