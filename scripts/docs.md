# Noridoc: scripts

Path: @/scripts

### Overview

The `scripts` directory contains build and release automation utilities for the beads project. These scripts handle version management, installation with embedded build info, and release orchestration across multiple distribution channels.

Key scripts include version bumping, installation helpers that inject git information at build time, and release coordination across GitHub, Homebrew, PyPI, and npm.

### How it fits into the larger codebase

- **Build Integration**: The `install.sh` script is referenced in documentation and release processes as the primary user-facing installation mechanism. It integrates directly with the Go build system to ensure full version information is embedded.

- **Version Pipeline**: Works alongside the version infrastructure in `@/cmd/bd/version.go` by extracting and passing git information at build time via ldflags.

- **Release Automation**: The `release.sh` and `bump-version.sh` scripts orchestrate the release process documented in `@/RELEASING.md`, ensuring version consistency across all components (CLI, plugin, MCP server, npm package).

- **CI/CD Integration**: Goreleaser (configured in `@/.goreleaser.yml`) uses the same ldflag patterns established by these scripts, ensuring consistency across all installation methods.

- **Multi-Channel Distribution**: These scripts ensure that whether a user installs via Homebrew, npm, GitHub releases, or direct `go install`, they get consistent version reporting with full git information.

### Core Implementation

**install.sh** (GitHub issue #503 fix):

The script simplifies local installation from source while ensuring full version information is available in the resulting binary:

1. **Usage** (lines 1-6):
   - Can be invoked from source checkout with optional custom install directory
   - `./scripts/install.sh` installs to `$(go env GOPATH)/bin`
   - `./scripts/install.sh /usr/local/bin` installs to custom location

2. **Git Information Extraction** (lines 12-14):
   - Extracts full commit hash via `git rev-parse HEAD`
   - Extracts branch name via `git rev-parse --abbrev-ref HEAD`
   - Gracefully handles missing git info (returns empty strings in non-git environments)

3. **Build Information Display** (lines 16-18):
   - Shows user where installation will occur
   - Displays the 12-character short commit hash
   - Displays the branch name for context

4. **Installation with Ldflags** (line 20):
   - Calls `go install` with explicit `-ldflags` to set `main.Commit` and `main.Branch`
   - These ldflags inject values that are then picked up by `resolveCommitHash()` and `resolveBranch()` in `@/cmd/bd/version.go`

5. **Post-Install Verification** (lines 22-24):
   - Immediately runs `bd version` to show the user that installation succeeded
   - User sees commit and branch info in the output, confirming full version info is present

**Makefile Integration** (`@/Makefile`, lines 37-41):

The Makefile's `install` target uses identical logic:
- Extracts git info at build time
- Passes to `go install` via ldflags
- Ensures that standard `make install` produces binaries with full version info, not just `make build`

**Goreleaser Configuration** (`@/.goreleaser.yml`):

All 5 platform builds (linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64) use goreleaser's built-in git variables:
- `-X main.Commit={{.Commit}}` uses goreleaser's detected commit
- `-X main.Branch={{.Branch}}` uses goreleaser's detected branch
- Ensures released binaries have full version info without requiring manual extraction

**Version Bumping** (`bump-version.sh` and `release.sh`):

- Coordinates version updates across multiple files (CLI version, plugin metadata, MCP server, npm package)
- Ensures all distribution channels report consistent version numbers
- Integrates with release process documented in `@/RELEASING.md`

### Things to Know

**Why Explicit Ldflags Are Necessary**:
- `go install` does not automatically embed VCS information like `go build` does (even though Go supports it)
- Without explicit ldflags, binaries lack commit and branch information regardless of installation method
- The Makefile and goreleaser configurations compensate for this limitation

**Installation Path Resolution**:
- The script uses `$(go env GOPATH)/bin` as the default installation target
- This respects the user's Go configuration and matches standard Go tooling behavior
- Allows overriding for system-wide installations (e.g., `/usr/local/bin`)

**Git Information Fallbacks**:
- The script silently handles missing git info (returns empty strings)
- This allows installation in non-git environments or git-less distributions
- The version command in `@/cmd/bd/version.go` has its own fallback chain

**Testing the Version Pipeline**:
- After running `./scripts/install.sh`, users should immediately see full version info via `bd version`
- The text output shows format like: `bd version 0.29.0 (dev: main@7e70940)`
- JSON output includes both `commit` and `branch` fields

**Release Coordination**:
- The `install.sh` script is independent of release automation
- Users can run it locally to build from any source branch
- Release scripts (`release.sh`, `update-homebrew.sh`) handle orchestration across channels and are documented separately in `@/RELEASING.md`

**Platform Compatibility**:
- All scripts use POSIX shell constructs (bash on all platforms)
- Git operations work identically on macOS, Linux, and Windows (with Git for Windows)
- The go install command behaves consistently across all platforms

**Security Considerations**:
- Scripts use `set -e` to fail fast on any errors
- Git commands are defensive (using `2>/dev/null` to suppress errors)
- No shell injection risks as git values are passed as structured arguments

Created and maintained by Nori.
