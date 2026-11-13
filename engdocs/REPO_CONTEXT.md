# Repository Context

This document explains how beads resolves repository context when commands run from
different directories than where `.beads/` lives.

## Problem

Git commands must run in the correct repository, but users may invoke `bd` from:

- A different repository (using `BEADS_DIR` environment variable)
- A git worktree (separate working directory, shared `.beads/`)
- A subdirectory within the repository

Without centralized handling, each command must implement its own path resolution,
leading to bugs when assumptions don't match reality.

## Solution: RepoContext API

The `RepoContext` API provides a single source of truth for repository resolution:

```go
import "github.com/steveyegge/beads/internal/beads"

rc, err := beads.GetRepoContext()
if err != nil {
    return err
}

// Run git in beads repository (not CWD)
cmd := rc.GitCmd(ctx, "status")
output, err := cmd.Output()
```

## When to Use Each Method

| Method | Use Case | Example |
|--------|----------|---------|
| `GitCmd()` | Git commands for beads operations | `git add .beads/`, `git push` |
| `GitCmdCWD()` | Git commands for user's working repo | `git status` (show user's changes) |
| `RelPath()` | Convert absolute path to repo-relative | Display paths in output |

### GitCmd() vs GitCmdCWD()

The distinction matters when `BEADS_DIR` points to a different repository:

```go
rc, _ := beads.GetRepoContext()

// GitCmd: runs in the beads repository
// Use for: committing .beads/, pushing/pulling beads data
cmd := rc.GitCmd(ctx, "add", ".beads/issues.jsonl")

// GitCmdCWD: runs in user's current repository
// Use for: checking user's uncommitted changes, status display
cmd := rc.GitCmdCWD(ctx, "status", "--porcelain")
```

## Scenarios

### Normal Repository

CWD is inside the repository containing `.beads/`:

```
/project/
├── .beads/
├── src/
└── README.md

$ cd /project/src
$ bd dolt push
# GitCmd() runs in /project (correct)
```

### BEADS_DIR Redirect

User is in one repository but managing beads in another:

```
$ cd /repo-a          # Has uncommitted changes
$ export BEADS_DIR=/repo-b/.beads
$ bd dolt push
# GitCmd() runs in /repo-b (correct, not /repo-a)
```

This pattern is common for:
- Fork contribution tracking (your tracker in separate repo)
- Shared team databases
- Monorepo setups

### Git Worktree

User is in a worktree but `.beads/` lives in main repository:

```
/project/                    # Main repo
├── .beads/
├── .worktrees/
│   └── feature-branch/      # Worktree (CWD)
└── src/

$ cd /project/.worktrees/feature-branch
$ bd dolt push
# GitCmd() runs in /project (main repo, where .beads lives)
```

### Combined: Worktree + Redirect

Both worktree and BEADS_DIR can be active simultaneously:

```
$ cd /repo-a/.worktrees/branch-x
$ export BEADS_DIR=/repo-b/.beads
$ bd dolt push
# GitCmd() runs in /repo-b (BEADS_DIR takes precedence)
```

## RepoContext Fields

| Field | Description |
|-------|-------------|
| `BeadsDir` | Actual `.beads/` directory (after following redirects) |
| `RepoRoot` | Repository root containing `BeadsDir` |
| `CWDRepoRoot` | Repository root containing user's CWD (may differ) |
| `IsRedirected` | True if BeadsDir is in a different repository than the CWD, or in none and outside the CWD's work tree while the CWD is in one (see [Resolving Without Git](#resolving-without-git)) |
| `IsWorktree` | True if CWD is in a git worktree |

## Resolving Without Git

`GetRepoContext()` requires a git repository for the CWD: it finds `.beads/`,
validates the path boundary, and only then asks git for a repository root. A workspace that
is located and boundary-checked but has no resolvable git root is therefore a
*known* workspace, not an unknown one — which is why it is the one failure that
is safe to recover from.

`GetRepoContextAllowingNoGit()` is that recovery, for callers that only read
config files and are documented to answer in degraded states: `bd context` and
the `domain/fs` context provider behind the proxied route (which `bd serve`
also resolves through). Anything that actually runs git commands must keep
using `GetRepoContext()`, so a missing repository stays an error where it
matters.

The `.beads` itself need not be in a repository. Named from a CWD that is in
one (`bd -C <dir>`, `BEADS_DIR`, `--db`), a `.beads` git cannot place resolves
as external — `IsRedirected` true, `RepoRoot` its parent, as in the no-git
table below — unless it belongs to the CWD repository's work tree, in which
case it stays local. It belongs when it lies below the CWD's top level, or when
it is reached through a `.beads` symlinked out of the tree where walk-up
discovery finds one: in the CWD or an ancestor up to the top level, or, from a
linked worktree, the main checkout's. Git-backed operations against such a
`RepoRoot` fail at the point of use with git's own "not a git repository"
error rather than running against the CWD's repository (GH#5700).

One shape is not recognized: a symlinked `.beads` named from elsewhere in the
same work tree, such as `bd -C sub` run from the top level when `sub/.beads`
is the symlink. bd resolves `BEADS_DIR` to the symlink's target before this
check, and nothing records which symlink led there, so it resolves as external.

The selection is typed, deliberately. `buildRepoContext()` can fail three ways
— no `.beads/`, the SEC-003 unsafe-location rejection, and no git root — and
the unsafe-location message embeds the offending path verbatim. A substring
test over the message would therefore be a *path-controlled* discriminator: a
workspace under a path containing the probe phrase would take the fallback and
have its security rejection silently cleared. Only the third failure returns
`*NoRepoRootError`, and `errors.As` selects it.

The synthesized context differs from a normal one in what it can know:

| Field | Value without git | Why |
|-------|-------------------|-----|
| `RepoRoot` | the repository containing `.beads`, else the `.beads` parent | the missing git root is the **CWD's**, not the `.beads`'s, so git is still asked from the `.beads` side (`repoRootForBeadsDir`); only when that cannot answer either does it fall back to the parent |
| `CWDRepoRoot` | `""`, or the CWD's root for a [refused repository](#repositories-git-refuses) | `GetMainRepoRoot`, `GetRepoRoot` and `IsWorktree` share one cached git context, so when the CWD's lookup is what failed all three fail together; a refused repository leaves that lookup intact |
| `IsRedirected` | true iff discovery standing in the CWD would not have found the resolved `.beads` | `isExternalBeadsDir` compares git *common dirs* and the CWD side needs a repository, so externality is decided **positionally** instead. This covers all four caller-directed channels (`BEADS_DIR`, `--db`, `BEADS_DB`/`BD_DB`, `-C`) uniformly, where an environment inventory could not: bd rewrites `BEADS_DIR` for itself before resolving, so the variable cannot distinguish a workspace the caller named from one bd just discovered. A store reachable only by being named is a redirect — including one in a *sibling* directory of the CWD — which is what `Role()` means by "external repo mode". A refused repository stays a redirect, as `buildRepoContext()` had already found it external |
| `IsWorktree` | false, or the CWD's answer for a refused repository | as for `CWDRepoRoot` |

`*NoRepoRootError` means "git could not tell us a root", which is broader than
"there is no repo": a dangling gitfile, a dubious-ownership refusal, or git
missing from `PATH` all arrive the same way. Recovery is equally safe in all of
them, but a caller reporting on the workspace should not claim to know which it
was — `bd context` prints `cwd repo: git: unavailable` rather than inventing a
reason.

## Security

### Git Hooks Disabled

`GitCmd()` disables git hooks and templates to prevent code execution in
potentially malicious repositories:

```go
cmd.Env = append(os.Environ(),
    "GIT_HOOKS_PATH=",   // Disable hooks
    "GIT_TEMPLATE_DIR=", // Disable templates
)
```

This protects against scenarios where `BEADS_DIR` points to an untrusted
repository that contains malicious `.git/hooks/` scripts.

### Repositories Git Refuses

`GitCmd()` pins `GIT_DIR` to `<RepoRoot>/.git`, and git skips its
`safe.directory` ownership check for an explicit `GIT_DIR`. An external
`RepoRoot` therefore comes from git itself (`git -C <BeadsDir> rev-parse
--show-toplevel`, which applies the check), or is the `.beads` parent only when
that parent holds no `.git`. When the parent's `.git` is one git did not open
from the `.beads` (another user's repository, or one a
`GIT_CEILING_DIRECTORIES` entry hides), `GetRepoContext()` returns
`*NoRepoRootError` instead, whether the `.beads` was named (`bd -C`,
`BEADS_DIR`) or reached through a `.beads/redirect`. bd never runs that
repository's config (`core.fsmonitor`, filters) as the caller;
`GetRepoContextAllowingNoGit()` still locates the workspace, and `Role()`
answers Contributor there without running git.

### Path Boundary Validation

`GetRepoContext()` validates that `BEADS_DIR` does not point to sensitive
system directories:

- `/etc`, `/usr`, `/var`, `/root` (Unix system directories)
- `/System`, `/Library` (macOS system directories)
- Other users' home directories

Temporary directories (e.g., `/var/folders` on macOS) are explicitly allowed
for test environments.

## Server Mode Handling

### CLI vs Server Context

For CLI commands, `GetRepoContext()` caches the result via `sync.Once` because:
- CWD doesn't change during command execution
- BEADS_DIR doesn't change during command execution
- Repeated filesystem access would be wasteful

For the Dolt server (long-running process), this caching is inappropriate:
- User may create new worktrees
- BEADS_DIR may change via direnv
- Multiple workspaces may be active simultaneously

### Workspace-Specific API

The server uses `GetRepoContextForWorkspace()` for fresh resolution:

```go
// For server mode: fresh resolution per-operation (no caching)
rc, err := beads.GetRepoContextForWorkspace(workspacePath)

// Validation hook for detecting stale contexts
if err := rc.Validate(); err != nil {
    // Context is stale, need fresh resolution
}
```

This function:
- Does NOT cache results
- Does NOT respect BEADS_DIR (workspace path is explicit)
- Resolves worktree relationships correctly
- Validates that paths still exist

## Migration Guide

### Before (scattered resolution)

```go
func doGitOperation(ctx context.Context) error {
    // Each function resolved paths differently
    beadsDir := beads.FindBeadsDir()
    redirectInfo := beads.GetRedirectInfo()
    var repoRoot string
    if redirectInfo.IsRedirected {
        repoRoot = filepath.Dir(beadsDir)
    } else {
        repoRoot = getRepoRootForWorktree(ctx)
    }
    cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "status")
    // ...
}
```

### After (centralized)

```go
func doGitOperation(ctx context.Context) error {
    rc, err := beads.GetRepoContext()
    if err != nil {
        return err
    }
    cmd := rc.GitCmd(ctx, "status")
    // ...
}
```

### Key Changes for Contributors

1. **Replace direct exec.Command**: Use `rc.GitCmd()` or `rc.GitCmdCWD()`
2. **Remove manual path resolution**: RepoContext handles all scenarios
3. **Clear caches in tests**: Call `beads.ResetCaches()` in test cleanup

## Testing

Tests use `beads.ResetCaches()` to clear cached context between test cases:

```go
func TestSomething(t *testing.T) {
    t.Cleanup(func() {
        beads.ResetCaches()
        git.ResetCaches()
    })
    // Test code...
}
```

## Related Documentation

- [WORKTREES.md](../docs/reference/worktrees.md) - Git worktree integration
- [ROUTING.md](../docs/multi-agent/routing.md) - Multi-repository routing
- [CONFIG.md](../docs/reference/configuration.md) - BEADS_DIR and environment variables

## Implementation Notes

- Result is cached via `sync.Once` for CLI efficiency
- CWD and BEADS_DIR don't change during command execution
- Uses `cmd.Dir` pattern (not `-C` flag) for Go-idiomatic execution
- Security mitigations implemented for git hooks and path traversal
