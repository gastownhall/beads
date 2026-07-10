# Git Worktrees Guide

Beads works from normal Git worktrees without a separate sync branch. Current
beads stores issue data in Dolt under `refs/dolt/data`, so issue sync is
separate from Git branch commits.

## Current Model

All worktrees in the same repository use the same beads workspace unless you
override discovery with `BEADS_DIR`.

```
project/
├── .git/                 # Shared Git directory
├── .beads/               # Shared beads config and local Dolt data
├── main-worktree/
└── feature-worktree/
```

Key points:

- `bd` discovers the repository's `.beads` directory from linked worktrees.
- Issue changes are stored in Dolt, not committed to the current Git branch.
- Cross-clone sync uses `bd dolt pull` and `bd dolt push`.
- No `sync.branch` or beads-managed Git worktree is required.

## Basic Usage

Initialize beads once in the repository:

```bash
cd project
bd init
```

Create linked worktrees normally:

```bash
git worktree add ../project-feature feature-branch
cd ../project-feature
bd ready
bd create "Implement feature X" -t feature -p 1
```

Sync issue data through the configured Dolt remote:

```bash
bd dolt pull
bd dolt push
```

## External Beads Workspace

If you want a separate issue-tracker repository shared by many code worktrees,
point `BEADS_DIR` at that workspace:

```bash
export BEADS_DIR=~/project-beads/.beads

cd ~/project/main       && bd list
cd ~/project/feature-1  && bd list
cd ~/project/feature-2  && bd list
```

With an external `BEADS_DIR`, `bd dolt push` and `bd dolt pull` target the
external beads workspace, not the code repository.

## Hooks

Git hooks installed by beads are worktree-aware. If hooks are stale or mention
removed legacy sync commands, refresh them:

```bash
bd hooks install
```

## Legacy Cleanup

Older beads versions had an experimental `sync.branch` workflow that created
hidden worktrees such as `.git/beads-worktrees/<branch>/`. That workflow has
been removed.

If a legacy checkout cannot switch branches because a beads-created worktree
still holds the branch, remove the stale worktree records:

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

If old config still contains a sync branch, clear it:

```bash
bd config set sync.branch ""
```

## Troubleshooting

### Database Not Found In A Worktree

Check that the main repository has a `.beads` directory and that the worktree
belongs to that repository:

```bash
git worktree list
cd /path/to/main/repo
ls -la .beads
```

If the repository has no beads workspace yet, run `bd init` from the main
repository.

### Multiple `.beads` Directories

If a worktree has its own accidental `.beads` directory, remove or archive the
extra copy after confirming it does not contain unique issue data. By default,
worktrees should share the repository workspace.

### Concurrent Writers

For ordinary single-user worktree use, run commands directly. For true
multi-writer workflows across machines or agents, sync frequently with
`bd dolt pull` and `bd dolt push`, and coordinate through the tracker to avoid
working the same issue concurrently.

## See Also

- [PROTECTED_BRANCHES.md](PROTECTED_BRANCHES.md) - protected branch behavior
- [GIT_INTEGRATION.md](GIT_INTEGRATION.md) - general Git integration guide
- [MULTI_REPO_MIGRATION.md](MULTI_REPO_MIGRATION.md) - multi-workspace patterns
