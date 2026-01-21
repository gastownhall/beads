# Multi-Repo Auto-Routing

This document describes the auto-routing feature that intelligently directs new issues to the appropriate repository based on user role.

## Overview

Auto-routing solves the OSS contributor problem: contributors want to plan work locally without polluting upstream PRs with planning issues. The routing layer automatically detects whether you're a maintainer or contributor and routes `bd create` to the appropriate repository.

## User Role Detection

### Strategy

The routing system detects user role via:

1. **Explicit git config** (highest priority):
   ```bash
   git config beads.role maintainer
   # or
   git config beads.role contributor
   ```

2. **Push URL inspection** (automatic):
   - SSH URLs (`git@github.com:user/repo.git`) → Maintainer
   - HTTPS with credentials → Maintainer  
   - HTTPS without credentials → Contributor
   - No remote → Contributor (fallback)

### Examples

```bash
# Maintainer (SSH access)
git remote add origin git@github.com:owner/repo.git
bd create "Fix bug" -p 1
# → Creates in current repo (.)

# Contributor (HTTPS fork)
git remote add origin https://github.com/fork/repo.git
git remote add upstream https://github.com/owner/repo.git
bd create "Fix bug" -p 1
# → Creates in planning repo (~/.beads-planning by default)
```

## Configuration

Routing is configured via the database config:

```bash
# Auto-routing is disabled by default (routing.mode="")
# Enable with:
bd init --contributor
# OR manually:
bd config set routing.mode auto

# Set default planning repo
bd config set routing.default "~/.beads-planning"

# Set repo for maintainers (in auto mode)
bd config set routing.maintainer "."

# Set repo for contributors (in auto mode)
bd config set routing.contributor "~/.beads-planning"
```

## CLI Usage

### Auto-Routing

```bash
# Let bd decide based on role
bd create "Fix authentication bug" -p 1

# Maintainer: creates in current repo (.)
# Contributor: creates in ~/.beads-planning
```

### Explicit Override

```bash
# Force creation in specific repo (overrides auto-routing)
bd create "Fix bug" -p 1 --repo /path/to/repo
bd create "Add feature" -p 1 --repo ~/my-planning
```

## Discovered Issue Inheritance

Issues created with `discovered-from` dependencies automatically inherit the parent's `source_repo`:

```bash
# Parent in current repo
bd create "Implement auth" -p 1
# → Created as bd-abc (source_repo = ".")

# Discovered issue inherits parent's repo
bd create "Found bug in auth" -p 1 --deps discovered-from:bd-abc
# → Created with source_repo = "." (same as parent)
```

This ensures discovered work stays in the same repository as the parent task.

## Backward Compatibility

- **Single-repo workflows unchanged**: If no multi-repo config exists, all issues go to current repo
- **Explicit --repo always wins**: `--repo` flag overrides any auto-routing
- **No schema changes**: Routing is pure config-based, no database migrations

## Implementation

**Key Files:**
- `internal/routing/routing.go` - Role detection and routing logic
- `internal/routing/routing_test.go` - Unit tests
- `cmd/bd/create.go` - Integration with create command
- `routing_integration_test.go` - End-to-end tests

**API:**

```go
// Detect user role based on git configuration
func DetectUserRole(repoPath string) (UserRole, error)

// Determine target repo based on config and role
func DetermineTargetRepo(config *RoutingConfig, userRole UserRole, repoPath string) string
```

## Testing

```bash
# Run routing tests
go test -v -run TestRouting

# Tests cover:
# - Maintainer detection (git config)
# - Contributor detection (fork remotes)
# - SSH vs HTTPS remote detection
# - Explicit --repo override
# - End-to-end multi-repo workflow
```

## Future Enhancements

See [bd-k58](https://github.com/steveyegge/beads/issues/k58) for proposal workflow:
- `bd propose <id>` - Move issue from planning to upstream
- `bd withdraw <id>` - Un-propose
- `bd accept <id>` - Maintainer accepts proposal
