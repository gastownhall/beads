---
title: Uninstalling
description: Remove beads from a repository with bd admin reset, uninstall git hooks, and delete the bd binary after backing up issue data
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

Use `bd admin reset` from the repository root. It previews what will be
removed by default:

```bash
bd admin reset
```

If the preview is correct, run:

```bash
bd admin reset --force
```

This removes beads-managed repository data such as:

- the `.beads/` directory
- git hooks that beads installed in full
- legacy beads sync worktrees under `.git/beads-worktrees/`

Reset works on whole hook files, not on sections. A hook of your own that
beads injected a section into is left in place and reported, because deleting
the file would take your content with it. Remove the section from those with
`bd hooks uninstall`.

## Remove Hooks Only

To keep issue data but remove git hooks:

```bash
bd hooks uninstall
```

This is preferable to manually deleting hook files because beads preserves
unrelated user hook content outside its managed hook markers.

## Manual Cleanup

Use manual cleanup only if `bd admin reset` is unavailable or cannot run in
the repository.

Start by stopping a local Dolt server, if one is running:

```bash
bd dolt stop 2>/dev/null || true
```

### Hooks: look before you delete

There is no batch command for this step, deliberately. `pre-commit`,
`prepare-commit-msg`, `post-merge`, `pre-push` and `post-checkout` are the
standard git hook names, not names beads reserves, so any of them may be a hook
you wrote — and if you are reading this section, `bd hooks uninstall` was not
available to tell the difference for you.

List which of them exist and what beads left in them:

```bash
grep -l -e 'bd-hooks-version:' -e 'bd-shim' -e 'bd (beads)' -e 'BEGIN BEADS INTEGRATION' \
  .git/hooks/pre-commit .git/hooks/prepare-commit-msg .git/hooks/post-merge \
  .git/hooks/pre-push .git/hooks/post-checkout 2>/dev/null
```

Open each file that matched and decide from what is in it:

- A file whose **whole content** beads generated carries a
  `# bd-hooks-version:`, `# bd-shim`, or `# bd (beads)` line and has nothing
  else in it but the shebang. Delete that one: `rm -f .git/hooks/<name>`.
- A file of yours with a `# --- BEGIN BEADS INTEGRATION ... ---` block in it is
  **your** file. Edit it: remove the lines from the `BEGIN` marker to the `END`
  marker, keep the rest, and leave the file in place. Comments of yours around
  the block count — a hook that is a header comment plus beads' block is still
  yours to edit rather than delete.

Hooks the command did not list are yours regardless of what they mention.
Naming beads in a comment, or calling `bd` from a hook you composed, does not
make the file beads'.

### The rest

```bash
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
git config --unset core.hooksPath 2>/dev/null || true
git config --unset merge.beads.driver 2>/dev/null || true
git config --unset merge.beads.name 2>/dev/null || true
```

Do not skip `core.hooksPath`: if it is left set, git keeps looking for a
hooks directory that no longer exists, and beads' post-checkout import can
recreate a `.beads/` workspace under the old prefix.

Check the value first, though — `core.hooksPath` is not beads-only. If
`git config --get core.hooksPath` reports a directory that belongs to another
hook manager (husky's `.husky/_`, for example) rather than `.beads/hooks` or
`.beads-hooks`, leave it alone; unsetting it would disable that tool's hooks
too. `bd doctor` applies the same rule and will not touch a hooks path it did
not set.

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
bd hooks list 2>/dev/null || true
git config --get merge.beads.driver
```

## Reinstall Later

To initialize beads again:

```bash
bd init
```
