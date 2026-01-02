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

## Why `bd worktree` over `git worktree`

**Always use `bd worktree`** instead of raw `git worktree` commands.

```bash
bd worktree create .worktrees/{name} --branch feature/{name}
bd worktree remove .worktrees/{name}
```

**Why?** `bd worktree` auto-configures:
- Beads database redirect files
- Proper gitignore entries
- Daemon bypass for worktree operations

## Architecture

All worktrees share one `.beads/` database via redirect files:

```
main-repo/
├── .beads/              ← Single source of truth
└── .worktrees/
    ├── feature-a/
    │   └── .beads       ← Redirect file (not directory)
    └── feature-b/
        └── .beads       ← Redirect file
```

**Key insight**: Daemon auto-bypasses for wisp operations in worktrees.

## Commands

```bash
# Create worktree with beads support
bd worktree create .worktrees/my-feature --branch feature/my-feature

# List worktrees
bd worktree list

# Show worktree info
bd worktree info .worktrees/my-feature

# Remove worktree cleanly
bd worktree remove .worktrees/my-feature
```

## Debugging

When beads commands behave unexpectedly in a worktree:

```bash
bd where              # Shows actual .beads location (follows redirects)
bd doctor --deep      # Validates graph integrity across all refs
```

## Protected Branch Workflows

For repos with protected `main` branch:

```bash
bd init --branch beads-metadata    # Use separate branch for beads data
bd init --contributor              # Auto-configure sync.remote=upstream for forks
```

This creates `.git/beads-worktrees/` for internal management.

## Multi-Clone Support

Multi-clone, multi-branch workflows:

- Hash-based IDs (`bd-abc`) eliminate collision across clones
- Each clone syncs independently via git
- See [WORKTREES.md](https://github.com/steveyegge/beads/blob/main/docs/WORKTREES.md) for comprehensive guide

## External References

- **Official Docs**: [github.com/steveyegge/beads/docs](https://github.com/steveyegge/beads/tree/main/docs)
- **Sync Branch**: [PROTECTED_BRANCHES.md](https://github.com/steveyegge/beads/blob/main/docs/PROTECTED_BRANCHES.md)
- **Worktrees**: [WORKTREES.md](https://github.com/steveyegge/beads/blob/main/docs/WORKTREES.md)
