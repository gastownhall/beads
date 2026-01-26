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
$ bd sync
# GitCmd() runs in /project (correct)
```

### BEADS_DIR Redirect

User is in one repository but managing beads in another:

```
$ cd /repo-a          # Has uncommitted changes
$ export BEADS_DIR=/repo-b/.beads
$ bd sync
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
$ bd sync
# GitCmd() runs in /project (main repo, where .beads lives)
```

### Combined: Worktree + Redirect

Both worktree and BEADS_DIR can be active simultaneously:

```
$ cd /repo-a/.worktrees/branch-x
$ export BEADS_DIR=/repo-b/.beads
$ bd sync
# GitCmd() runs in /repo-b (BEADS_DIR takes precedence)
```

## RepoContext Fields

| Field | Description |
|-------|-------------|
| `BeadsDir` | Actual `.beads/` directory (after following redirects) |
| `RepoRoot` | Repository root containing `BeadsDir` |
| `CWDRepoRoot` | Repository root containing user's CWD (may differ) |
| `IsRedirected` | True if BEADS_DIR points to different repo than CWD |
| `IsWorktree` | True if CWD is in a git worktree |

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

### Path Boundary Validation

`GetRepoContext()` validates that `BEADS_DIR` does not point to sensitive
system directories:

- `/etc`, `/usr`, `/var`, `/root` (Unix system directories)
- `/System`, `/Library` (macOS system directories)
- Other users' home directories

Temporary directories (e.g., `/var/folders` on macOS) are explicitly allowed
for test environments.

## Daemon Handling

### CLI vs Daemon Context

For CLI commands, `GetRepoContext()` caches the result via `sync.Once` because:
- CWD doesn't change during command execution
- BEADS_DIR doesn't change during command execution
- Repeated filesystem access would be wasteful

For the daemon (long-running process), this caching is inappropriate:
- User may create new worktrees
- BEADS_DIR may change via direnv
- Multiple workspaces may be active simultaneously

### Workspace-Specific API

The daemon uses `GetRepoContextForWorkspace()` for fresh resolution:

```go
// For daemon: fresh resolution per-operation (no caching)
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

- [WORKTREES.md](WORKTREES.md) - Git worktree integration
- [ROUTING.md](ROUTING.md) - Multi-repository routing
- [CONFIG.md](CONFIG.md) - BEADS_DIR and environment variables
- [DAEMON.md](DAEMON.md) - Daemon architecture and workspace handling

## Implementation Notes

- Result is cached via `sync.Once` for CLI efficiency
- CWD and BEADS_DIR don't change during command execution
- Uses `cmd.Dir` pattern (not `-C` flag) for Go-idiomatic execution
- Security mitigations implemented for git hooks and path traversal
