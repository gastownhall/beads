---
title: Protected Branches
description: Why beads needs no protected-branch workaround since Dolt stores issue data outside Git refs, plus team workflow and legacy sync-branch cleanup.
---

Beads does not need a protected-branch workaround in current releases.

Issue data is stored in Dolt under `refs/dolt/data`, separate from normal Git
branches such as `main`. Beads commands do not commit issue updates to your
current code branch, so GitHub, GitLab, and Bitbucket branch protection rules
continue to apply only to your code history.

## Current Workflow

Initialize beads in the project:

```bash
bd init
```

Commit the small tracked configuration files if your project policy requires
them:

```bash
git add .beads/.gitignore .beads/metadata.json .beads/config.yaml .gitignore
git commit -m "Initialize beads issue tracker"
```

The local Dolt database directory remains gitignored. Sync issue data through a
Dolt remote:

```bash
bd dolt pull
bd dolt push
```

No `beads-sync` Git branch, protected-branch exception, or beads-managed Git
worktree is required.

## Why Protected Branches Are Safe

Protected branches guard Git refs such as `refs/heads/main`. Dolt stores beads
data in its own ref namespace. That means:

- `bd create`, `bd update`, and `bd close` do not create commits on `main`.
- `bd dolt push` pushes Dolt data, not a code branch.
- Normal code changes still go through your existing pull-request workflow.

## Team Usage

For a shared tracker:

```bash
bd init --team
bd dolt pull
bd ready
bd update <id> --claim
bd dolt push
```

Pull before starting work and push before handing off so other clones see the
latest issue state.

## Legacy Sync-Branch Cleanup

Older beads versions documented an experimental `sync.branch` workflow that
committed `.beads` changes to a branch such as `beads-sync` and used hidden Git
worktrees under `.git/beads-worktrees/`. That workflow has been removed.

If an old checkout still has sync-branch config, clear it:

```bash
bd config set sync.branch ""
```

If stale hidden worktrees prevent branch checkout, remove them and prune Git's
worktree registry:

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

If a remote `beads-sync` branch exists only for the removed workflow, archive or
delete it according to your repository policy after confirming all current issue
data has been synced through Dolt.

## Troubleshooting

### `bd dolt push` Has No Remote

Add or inspect the Dolt remote:

```bash
bd dolt remote list
bd dolt remote add origin <remote-url>
bd dolt push
```

### Conflicts During `bd dolt pull`

Dolt reports database-level conflicts separately from Git branch conflicts. Use
the merge strategy or doctor guidance printed by the failed command:

```bash
bd vc merge <branch> --strategy [ours|theirs]
bd doctor --fix
```

### Stale Hooks Mention Legacy Sync Commands

Refresh generated hooks:

```bash
bd hooks install
```

## See Also

- [Git Worktrees Guide](/reference/worktrees) - Git worktree behavior
- [Git Integration](/reference/git-integration) - general Git integration guide
- [Recovery Playbooks](/recovery/init-safety) - recovery playbooks
