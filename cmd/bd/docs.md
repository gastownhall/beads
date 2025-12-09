# Noridoc: cmd/bd

Path: @/cmd/bd

### Overview

The `cmd/bd` directory contains the complete CLI application for the Beads issue tracker. It implements the `bd` command-line tool, which users interact with to create, query, manage, and synchronize issues across distributed systems.

The CLI is built on the Cobra framework and consists of command implementations for core operations (create, list, delete, import, export, sync, etc.), daemon management for background operations, and version reporting that includes git commit and branch information from the build.

### How it fits into the larger codebase

- **Entry Point**: The CLI defined here (`cmd/bd/main.go`) is the user-facing interface to the entire beads system. All user interactions flow through this package.

- **Integration with Core Libraries**: The CLI commands call into libraries at `@/internal/beads` (database discovery, version detection), `@/internal/storage` (database operations), `@/internal/rpc` (daemon communication), and other internal packages.

- **Daemon Communication**: Commands use RPC client logic to communicate with the background daemon (PersistentPreRun hook), allowing the CLI to operate either in daemon mode (delegating to the daemon) or direct mode (local database operations).

- **Version Reporting**: The version command (`@/cmd/bd/version.go`) reports full build information - it resolves git commit and branch from ldflags set at build time via the Makefile (`@/Makefile`) and goreleaser config (`@/.goreleaser.yml`). This enables users to identify exactly what code their binary was built from.

- **Release Pipeline Integration**: The version infrastructure here integrates directly with the release pipeline documented in `@/RELEASING.md`. Version information is injected during builds by build automation, ensuring consistency across all distribution channels (GitHub releases, Homebrew, npm, direct go install).

### Core Implementation

**Version Information Pipeline** (GitHub issue #503):

1. **Version Variables** (lines 15-23 in `@/cmd/bd/version.go`):
   - `Version`: The semantic version (e.g., "0.29.0")
   - `Build`: The build type ("dev" for local builds, typically set to short commit hash by goreleaser)
   - `Commit`: Git commit hash (optional, set via ldflag `-X main.Commit=...`)
   - `Branch`: Git branch name (optional, set via ldflag `-X main.Branch=...`)

2. **Resolution Strategy** (`resolveCommitHash()` and `resolveBranch()` functions):
   - First checks if the ldflag was set at build time (most authoritative)
   - Falls back to `runtime/debug.ReadBuildInfo()` to extract VCS info automatically embedded by Go when using `go build` directly from source
   - For branch, additionally falls back to `git symbolic-ref --short HEAD` as a runtime resolution

3. **Build-Time Injection**:
   - **Makefile** (`@/Makefile`, lines 37-41): The `make install` target extracts git info at build time and passes it to `go install` via ldflags
   - **.goreleaser.yml** (`@/.goreleaser.yml`): All 5 build configurations (linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64) set the same ldflags using goreleaser's template variables `{{.Commit}}` and `{{.Branch}}`
   - **scripts/install.sh** (`@/scripts/install.sh`, lines 13-14): Helper script for users who want to build from source with explicit git info passed to `go install`

4. **Output Formatting**:
   - **Text Output** (lines 52-58 in `version.go`): Shows format like `bd version 0.29.0 (dev: main@7e70940)` when both commit and branch are available
   - **JSON Output** (lines 39-50): Includes optional `commit` and `branch` fields when available

5. **Daemon Version Checking** (lines 63-109): The `--daemon` flag shows daemon/client compatibility by calling health RPC endpoints

**Command Structure**:
- All commands follow the Cobra pattern with `Command` structs and run functions
- Commands register themselves via `init()` functions that add them to `rootCmd`
- The daemon connection state is managed via PersistentPreRun hooks, allowing most commands to transparently work in daemon or direct mode

**Key Data Paths**:
- User input → Cobra command parsing → Internal beads library calls → Storage layer → Git operations
- Responses flow back through storage → RPC (if daemon) or direct return → formatted output

### Things to Know

**Why Both Commit and Branch Are Needed**:
- The `Commit` variable allows tracing the exact code version
- The `Branch` variable provides context for CI/CD systems, build automation, and helps users understand which development line they're running

**The Problem This Solves** (Issue #503):
- `go install` from source doesn't automatically embed VCS info like `go build` does (Go 1.18+ feature)
- Without explicit ldflags, users running `bd version` would only see semantic version and build type, not which commit they had
- This made it impossible to debug issues or understand build provenance
- The fix ensures both `make install` and raw `go install` produce binaries with full version info by setting ldflags explicitly

**Fallback Resolution Chain** (important for development):
- The `resolveCommitHash()` function first checks the ldflag, then checks runtime build info, returning empty if neither is available
- This allows the version command to work even in development environments where ldflags aren't set (useful for testing)

**Testing Coverage** (`@/cmd/bd/version_test.go`):
- `TestResolveCommitHash`: Verifies ldflag values are used when set
- `TestResolveBranch`: Verifies ldflag values are used when set
- `TestVersionOutputWithCommitAndBranch`: Verifies text and JSON output formats correctly include commit and branch information
- Existing `TestVersionCommand` and `TestVersionFlag` tests verify basic version output

**Build System Dependencies**:
- The Makefile must have bash and git available at install time
- Goreleaser relies on git tags being present for version metadata
- Scripts/install.sh is designed for local development from source clones

**Platform-Specific Considerations**:
- The git extraction in Makefile uses POSIX shell constructs compatible with bash on all platforms
- Windows builds via goreleaser set ldflags identically to Unix platforms
- The `symbolic-ref` fallback in resolveBranch works reliably even in fresh repos with no commits

Created and maintained by Nori.
