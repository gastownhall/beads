---
description: Synchronize issues with git remote
argument-hint: [--dry-run] [--message] [--status] [--merge]
---

Synchronize issues with git remote in a single operation.

## Sync Steps

1. Export pending changes to JSONL
2. Commit changes to git
3. Pull from remote (with conflict resolution)
4. Import updated JSONL
5. Push local commits to remote

Wraps the entire git-based sync workflow for multi-device use.

## Usage

- **Basic sync**: `bd sync`
- **Preview**: `bd sync --dry-run`
- **Custom message**: `bd sync --message "Closed sprint issues"`
- **Pull only**: `bd sync --no-push`
- **Push only**: `bd sync --no-pull`
- **Flush only**: `bd sync --flush-only` (export to JSONL without git operations)
- **Import only**: `bd sync --import-only` (import from JSONL without git operations)

## Separate Branch Workflow

When using a separate sync branch (configured via `sync.branch`), additional commands are available:

- **Check status**: `bd sync --status` - Show diff between sync branch and main
- **Merge to main**: `bd sync --merge` - Merge sync branch back to main branch
- **Preview merge**: `bd sync --merge --dry-run` - Preview what would be merged

### Merge Workflow

When working with a protected main branch and separate sync branch:

1. Beads commits go to the sync branch (e.g., `beads-metadata`)
2. Use `bd sync --status` to review pending changes
3. When ready, use `bd sync --merge` to merge back to main
4. After merge, run `bd import` to update the database
5. Run `bd sync` to push changes to remote

The merge command includes safety checks:
- Verifies you're not on the sync branch
- Checks for uncommitted changes in working tree
- Detects and reports merge conflicts with resolution steps
- Uses `--no-ff` to create a merge commit for clear history

## Note

Most users should rely on the daemon's automatic sync (`bd daemon --auto-commit --auto-push`) instead of running manual sync. This command is useful for one-off syncs or when not using the daemon.
