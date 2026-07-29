# Git Worktree Support

> Adapted from ACF beads skill

**v0.40+**: First-class worktree management via `bd worktree` command.

## When to Use Worktrees

| Scenario | Worktree? | Why |
|----------|-----------|-----|
| Parallel agent work | Yes | Each agent gets isolated working directory |
| Long-running feature | Yes | Avoids stash/switch dance for interruptions |
| Quick branch switch | No | `git switch` is simpler |
| PR review isolation | Yes | Review without disturbing main work |

## Creating Worktrees

Normal Git worktrees work with beads. Use `bd worktree` when its convenience
features are useful:

```bash
# Beads convenience command: creates the Git worktree and adds an in-repo path
# to .gitignore.
bd worktree create .worktrees/{name} --branch feature/{name}
bd worktree remove .worktrees/{name}

# Standard Git commands are also supported.
git worktree add -b feature/{name} .worktrees/{name}
git worktree remove .worktrees/{name}
```

`bd worktree remove` adds safety checks for uncommitted changes and unpushed
commits. By default, both creation paths use the same shared beads workspace.

## Architecture

By default, linked worktrees share the repository's `.beads/` workspace through
Git common directory discovery. They do not need per-worktree redirect files:

```
main-repo/
├── .git/                ← Shared Git directory
├── .beads/              ← Shared beads config and local Dolt data
└── .worktrees/
    ├── feature-a/
    └── feature-b/
```

`bd` uses the workspace's configured storage mode from every linked worktree;
worktree use does not force embedded mode.

Set `BEADS_DIR` to use an external beads workspace instead. A worktree can also
use its own `.beads/` database explicitly; otherwise discovery falls back to
the shared workspace.

## Commands

```bash
# Create worktree with beads support
bd worktree create .worktrees/my-feature --branch feature/my-feature

# List worktrees
bd worktree list

# Show info for the current worktree
cd .worktrees/my-feature
bd worktree info

# Remove worktree cleanly
bd worktree remove .worktrees/my-feature
```

## Debugging

When beads commands behave unexpectedly in a worktree:

```bash
bd where              # Shows the effective .beads workspace location
bd doctor --deep      # Validates full graph integrity
```

## Protected Branch Workflows

Protected Git branches need no special beads branch because issue data is
stored in Dolt under `refs/dolt/data`, separate from code branches:

```bash
# Choose one initialization path:
bd init                            # Standard repository setup
# OR
bd init --contributor              # OSS fork setup with contributor routing

bd dolt pull                       # Pull shared issue data
bd dolt push                       # Push shared issue data
```

No `--branch` flag or `.git/beads-worktrees/` directory is used. Keep using
your normal Git feature branches and worktrees for code changes.

## Multi-Clone Support

Multi-clone, multi-branch workflows:

- Hash-based IDs (`bd-abc`) eliminate collision across clones
- Each clone syncs through the configured Dolt remote with `bd dolt pull` and
  `bd dolt push`
- See [WORKTREES.md](https://github.com/gastownhall/beads/blob/main/docs/reference/worktrees.md) for comprehensive guide

## External References

- **Official Docs**: [github.com/gastownhall/beads/docs](https://github.com/gastownhall/beads/tree/main/docs)
- **Protected Branches**: [PROTECTED_BRANCHES.md](https://github.com/gastownhall/beads/blob/main/docs/reference/protected-branches.md)
- **Worktrees**: [WORKTREES.md](https://github.com/gastownhall/beads/blob/main/docs/reference/worktrees.md)
