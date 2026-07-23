---
title: Git Integration
description: How bd uses git for hosting and hooks, including hook installation, external hook managers, worktrees, and branch workflows.
---

How beads integrates with git.

## Overview

Beads uses git for:
- **Project hosting** - Your code repository also hosts beads configuration
- **Hooks** - Auto-sync on git operations

Data storage and sync are handled by Dolt (a version-controlled SQL database) —
see [Sync Concepts](/core-concepts/sync-concepts) for how issue data moves
between machines.

## File Structure

```
.beads/
├── config.yaml        # Project config (git-tracked)
├── metadata.json      # Backend metadata (git-tracked)
├── .gitignore         # Written by bd init (git-tracked)
├── embeddeddolt/      # Dolt database — embedded mode, the default (gitignored)
└── dolt/              # Dolt database — server mode (gitignored)
```

`bd init` writes `.beads/.gitignore` to keep the database directory and
runtime files out of git — no manual gitignore rules are needed. Never track
the database directory (`.beads/embeddeddolt/` or `.beads/dolt/`) in git or
via Git LFS.

## Git Hooks

### Installation

`bd init` installs hooks by default (skip with `bd init --skip-hooks`). To
install or refresh them manually:

```bash
bd hooks install
```

Installed hooks are thin shims that call `bd hooks run <hook-name>`, so
upgrading `bd` automatically updates hook behavior:

| Hook | What it does |
|------|--------------|
| `pre-commit` | Runs chained hooks; when `export.auto` is enabled, exports `.beads/issues.jsonl` so it lands in the same commit |
| `post-merge` | Runs chained hooks; imports JSONL only as a legacy fallback when no Dolt remote is configured — with `sync.remote` set, `bd dolt pull` is the canonical sync |
| `pre-push` | Runs chained hooks before push |
| `post-checkout` | Runs chained hooks after branch checkout |
| `prepare-commit-msg` | Adds an `Executed-By:` agent identity trailer when an agent (`BD_ACTOR`) makes the commit |

The shims use section markers to coexist with existing hooks — content
outside the markers is preserved across installs and upgrades. Install
variants:

```bash
bd hooks install --beads    # Install to .beads/hooks/ (recommended for the Dolt backend)
bd hooks install --shared   # Install to .beads-hooks/ (versioned, shareable with the team)
bd hooks install --chain    # Run existing hooks before bd hooks
```

Hook installation is worktree-aware: `bd` resolves the shared git directory,
so installing from a linked worktree works.

### Status

```bash
bd hooks list
```

### Uninstall

```bash
bd hooks uninstall
```

### External Hook Managers

bd detects these external git hook managers and checks whether their config
calls `bd hooks run`:

- [lefthook](https://lefthook.dev/) — YAML/TOML/JSON config
- [husky](https://typicode.github.io/husky/) — `.husky/` directory scripts
- [pre-commit](https://pre-commit.com/) — `.pre-commit-config.yaml`
- [prek](https://prek.j178.dev/) — Rust-based pre-commit alternative (same config)
- [hk](https://hk.jdx.dev/) — fast hook manager using Pkl config
- [overcommit](https://github.com/sds/overcommit) — Ruby-based (detection only)
- yorkie — detection only
- [simple-git-hooks](https://github.com/toplenboren/simple-git-hooks) — lightweight JS (detection only)

`bd doctor` reports whether a detected manager is integrated with bd, and
`bd doctor --fix` reinstalls the hooks with `--chain` so the manager's
existing hooks keep running.

For config-driven managers, add bd steps directly. Example `hk.pkl`:

```pkl
hooks {
    ["pre-commit"] {
        steps {
            ["bd-pre-commit"] {
                check = "bd hooks run pre-commit"
            }
        }
    }
    ["post-merge"] {
        steps {
            ["bd-post-merge"] {
                check = "bd hooks run post-merge"
            }
        }
    }
    ["pre-push"] {
        steps {
            ["bd-pre-push"] {
                check = "bd hooks run pre-push \"$@\""
            }
        }
    }
}
```

### Hook Timeout

The hook shim wraps `bd hooks run` with an OS-level `timeout` so hooks cannot
hang git operations indefinitely. The default is **300 seconds** (5 minutes),
which accommodates chained pre-commit pipelines (eslint, prettier, TypeScript
compilation). Override it with the `BEADS_HOOK_TIMEOUT` environment variable:

```bash
# Set a longer timeout (in seconds)
export BEADS_HOOK_TIMEOUT=600  # 10 minutes

# Or set it per-invocation
BEADS_HOOK_TIMEOUT=600 git commit -m "..."
```

When the timeout is reached, beads prints a warning and lets the git
operation proceed — the commit or push is not blocked.

## Conflict Resolution

Dolt handles merge conflicts at the database level using its built-in
merge capabilities. When conflicts arise during sync, Dolt identifies
conflicting rows and allows resolution through SQL.

```bash
# Check for and fix conflicts
bd doctor --fix
```

## Protected Branches

Dolt stores data under `refs/dolt/data`, separate from Git refs. This means
beads data does not conflict with protected Git branches, and no separate
`beads-sync` branch or protected-branch exception is needed. On new projects
with a Git `origin`, `bd init` configures that origin as the Dolt remote
automatically.

See [Protected Branches](/reference/protected-branches) for the full
workflow, including legacy `beads-sync` cleanup.

## Git Worktrees

Beads works in Git worktrees without extra setup. Linked worktrees discover the
repository's `.beads` workspace and sync issue data through Dolt:

```bash
# In a linked worktree
bd create "Task"
bd list
bd dolt pull
bd dolt push
```

All worktrees share the repository's `.beads` workspace: discovery follows
`BEADS_DIR` if set, then the main repository's `.beads`, preventing database
duplication across worktrees. Use `bd where` as the authoritative check for
which workspace is active — a local `./.beads` may legitimately be absent in
a worktree. Embedded mode (the default) serves one writer at a time; for
concurrent writers across worktrees, use server mode. See
[Git Worktrees](/reference/worktrees) for the full guide.

Older beads versions documented a `sync.branch` workflow that created hidden
Git worktrees. That workflow has been removed; current sync uses Dolt remotes.

## Branch Workflows

### Feature Branch

```bash
git checkout -b feature-x
bd create "Feature X" -t feature
# Work...
bd dolt push
git push
```

### Fork Workflow

```bash
# In fork
bd init --contributor   # Interactive wizard
# Work in separate planning repo...
bd dolt push
```

The contributor wizard keeps issue data in a separate planning repository,
leaving the upstream repo without any `.beads/`. Best for open source
contributors, solo developers, and private task tracking on public repos.

`bd init` auto-detects forks and offers to configure `.git/info/exclude`
(`--setup-exclude`) so beads files stay local. Set the role without prompting
via `--role contributor` or `--role maintainer` (the default in
non-interactive mode).

### Team Workflow

```bash
bd init --team
# All team members share the Dolt database
bd dolt pull   # Pull latest changes from Dolt remote
bd dolt push   # Push your changes to Dolt remote
```

Best for teams on protected branches and review-before-merge policies. See
[Multi-Repo Migration](/multi-agent/multi-repo-migration) for multi-repo
patterns.

### Duplicate Detection

After merging branches:

```bash
bd duplicates --auto-merge
```

## Branchless Workflows (Jujutsu / jj)

Beads works with branchless VCS tools like
[Jujutsu (jj)](https://martinvonz.github.io/jj/). Since beads data is stored
in Dolt (not git branches), there is no dependency on the "current branch"
concept.

### What Works Without Hooks

All core beads functionality works without git hooks:

| Feature | Hooks Required? | Notes |
|---------|----------------|-------|
| `bd create`, `bd update`, `bd close` | No | Core CRUD uses Dolt directly |
| `bd ready`, `bd list`, `bd show` | No | Read-only queries |
| `bd dolt push` / `bd dolt pull` | No | Dolt-native sync, independent of git |
| `bd onboard`, `bd doctor` | No | Diagnostics and onboarding |
| Agent identity trailers | Yes | `prepare-commit-msg` hook adds `Executed-By:` to commits |
| Hook chaining | Yes | Preserves existing pre-commit, post-merge hooks |

To skip hooks entirely during init:

```bash
bd init --skip-hooks
```

### What Works Without AGENTS.md

The AGENTS.md file generated by `bd init` provides AI agent instructions. If
you manage your own agent instructions or don't want beads to modify tracked
files:

```bash
bd init --skip-agents    # Skip AGENTS.md and Claude/Codex setup generation
bd init --stealth        # Full invisible mode (also skips hooks + agents)
```

### Jujutsu Setup

**Colocated repos** (`jj git init --colocate`): Git hooks work normally.
Beads installs simplified hooks (`pre-commit` and `post-merge` only, no
staging logic).

**Pure jj repos** (no git): Since jj doesn't have native hooks yet, set up
push aliases:

```toml
# ~/.config/jj/config.toml
[aliases]
push = ["util", "exec", "--", "sh", "-c", "bd dolt commit && bd dolt push && jj git push \"$@\"", ""]
```

Then use `jj push` instead of `jj git push`.

## Best Practices

1. **Install hooks** - `bd hooks install`
2. **Push regularly** - `bd dolt push` at session end
3. **Pull before work** - `bd dolt pull` to get latest issues
4. **Use normal Git worktrees** - no sync branch is required
