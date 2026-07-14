---
title: Uninstalling
description: Remove beads from a repository with bd reset, uninstall git hooks, and delete the bd binary after backing up issue data
---

This guide explains how to remove beads from a repository or remove the `bd`
binary from a machine.

## Before You Remove Data

Removing `.beads/` permanently deletes the local Dolt database. If the issue
history matters, make a Dolt-native backup first:

```bash
bd backup init /path/to/beads-backup
bd backup sync
```

For review, migration, or interoperability, you can also write an issue-table
export:

```bash
bd export -o ~/beads-issues-$(date +%Y%m%d).jsonl
```

`bd export` is not a complete restorable database backup. It does not preserve
Dolt branches, commit history, working-set state, or non-issue tables.

## Repository Reset

Use `bd reset` from the repository root. It previews what will be removed by
default:

```bash
bd reset
```

If the preview is correct, run:

```bash
bd reset --force
```

This removes beads-managed repository data such as:

- the `.beads/` directory
- beads-managed git hook sections
- legacy beads sync worktrees under `.git/beads-worktrees/`

## Remove Hooks Only

To keep issue data but remove git hooks:

```bash
bd hooks uninstall
```

This is preferable to manually deleting hook files because beads preserves
unrelated user hook content outside its managed hook markers.

## Manual Cleanup

Use manual cleanup only if `bd reset` is unavailable or cannot run in the
repository.

```bash
# Stop a local Dolt server if one is running.
bd dolt stop 2>/dev/null || true

# Remove beads-managed hooks when bd hooks uninstall is unavailable.
rm -f .git/hooks/pre-commit
rm -f .git/hooks/prepare-commit-msg
rm -f .git/hooks/post-merge
rm -f .git/hooks/pre-push
rm -f .git/hooks/post-checkout

# Remove the local beads database and config.
rm -rf .beads

# Remove legacy sync-branch worktrees from older beads versions.
rm -rf .git/beads-worktrees
git worktree prune
```

If `.gitattributes` contains only beads merge-driver configuration, remove it.
If it contains other project entries, edit out only the beads line.

If beads-specific git config remains, remove it:

```bash
git config --unset beads.role 2>/dev/null || true
git config --unset merge.beads.driver 2>/dev/null || true
git config --unset merge.beads.name 2>/dev/null || true
```

## Remove the `bd` Binary

The CLI is a standalone binary. Remove it according to how it was installed:

```bash
# Homebrew
brew uninstall beads

# Go install
rm -f "$(which bd)"

# Manual install location
rm -f /usr/local/bin/bd
```

If you installed the MCP package separately, remove that package with the tool
you used to install it.

## Verify Removal

```bash
which bd
test ! -e .beads
bd hooks status 2>/dev/null || true
git config --get merge.beads.driver
```

## Reinstall Later

To initialize beads again:

```bash
bd init
```
