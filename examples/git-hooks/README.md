# bd Git Hooks

This directory contains git hooks that integrate bd (beads) with your git workflow, solving the race condition between daemon auto-flush and git commits.

## The Problem

When using bd in daemon mode, operations trigger a 5-second debounced auto-flush to JSONL. This creates a race condition:

1. User closes issue via MCP → daemon schedules flush (5 sec delay)
2. User commits code changes → JSONL appears clean
3. Daemon flush fires → JSONL modified after commit
4. Result: dirty working tree showing JSONL changes

## The Solution

These git hooks ensure bd changes are always synchronized with your commits:

- **pre-commit** - Flushes pending bd changes to JSONL before commit
- **post-merge** - Imports updated JSONL after git pull/merge

## Installation

### Quick Install

From your repository root:

```bash
./examples/git-hooks/install.sh
```

This will:
- Copy hooks to `.git/hooks/`
- Make them executable
- Back up any existing hooks

### Manual Install

```bash
cp examples/git-hooks/pre-commit .git/hooks/pre-commit
cp examples/git-hooks/post-merge .git/hooks/post-merge
chmod +x .git/hooks/pre-commit .git/hooks/post-merge
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

### post-merge

After a git pull or merge, the hook runs:

```bash
bd import -i .beads/issues.jsonl --resolve-collisions
```

This ensures your local database reflects the merged state. The hook:
- Only runs if `.beads/issues.jsonl` exists
- Automatically resolves ID collisions from branch merges
- Warns on failure but doesn't block the merge

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
rm .git/hooks/pre-commit .git/hooks/post-merge
```

Your backed-up hooks (if any) are in `.git/hooks/*.backup-*`.

## Related

- See [bd-51](../../.beads/bd-51) for the race condition bug report
- See [AGENTS.md](../../AGENTS.md) for the full git workflow
- See [examples/](../) for other integrations
