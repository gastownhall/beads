---
title: Protected Branches
description: Why beads needs no protected-branch workaround since Dolt stores issue data outside your code branches, which refs a git-backed remote holds, plus team workflow and legacy sync-branch cleanup.
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

## Refs on a Git-Backed Remote

When the Dolt remote is a Git repository (`git+ssh://`, `git+https://`, or a
`git+file://` path ending in `.git`), Dolt keeps beads data in that repository
on a Git ref. Dolt's default data ref is `refs/dolt/data`, which a normal branch
list does not show. This section describes a remote that uses the default.

| Ref | What it holds | Safe to delete? |
|---|---|---|
| `refs/dolt/data` | The Dolt store: all issue history on that remote | No |
| `refs/heads/__dolt_remote_info__` | One file, `DOLT_REMOTE.md`, marking the repository as a Dolt remote | Yes, if the remote has another branch |

### `refs/dolt/data`

`git clone` and `git fetch` do not fetch `refs/dolt/*`, so an ordinary clone
has no copy of this ref. `git push --mirror` from such a clone makes the
remote's refs match the clone's, so it deletes `refs/dolt/data` on the remote.
The only sign is a `- [deleted] refs/dolt/data` line in its output. The beads
data at that ref is then gone. Local workspaces keep their own Dolt databases,
which `git push` does not touch.

To move a repository to a new host, push from a mirror clone, which carries
every ref:

```bash
git clone --mirror <old-url> repo.git
git -C repo.git push --mirror <new-url>
```

To check that a remote still holds `refs/dolt/data`:

```bash
git ls-remote <remote-url> refs/dolt/data
```

No output means the ref is missing.

### `__dolt_remote_info__`

After a push, Dolt can also force-push a branch named `__dolt_remote_info__`
that holds one file, `DOLT_REMOTE.md`. Dolt writes this branch best-effort and
never reads it back. It holds no issue data, so deleting it loses nothing, and
Dolt writes it again on its next push unless the branch is turned off. Keep it
if it is the remote's only branch: Dolt refuses a Git remote that has no
branches (`git remote has no branches`).

An empty `DOLT_REMOTE_INFO_BRANCH` turns the branch off. Set the variable in
the environment of the process that pushes: the `bd` process in embedded mode,
or the Dolt sql-server in server mode. A sql-server that bd starts inherits the
variable from the `bd` command that starts it.

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
