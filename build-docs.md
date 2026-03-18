# Noridoc: Build and Version Infrastructure

Path: @/ (root level - Makefile, .goreleaser.yml)

### Overview

The beads project uses a coordinated build and version reporting system that keeps the supported installation methods (`make install`, user-facing `go install ...@latest`, GitHub releases, Homebrew, npm, and the install scripts) reporting enough version/build information to identify what was installed.

This infrastructure is critical for debugging, auditing, and user support - it allows anyone to identify exactly what code their binary was built from.

### How it fits into the larger codebase

- **Build Entry Points**: The Makefile and .goreleaser.yml are the authoritative build configurations that users and CI/CD systems interact with. They control how version information flows into binaries.

- **Version Pipeline**: These files work with `@/cmd/bd/version.go` to establish the complete version reporting chain:
  - Build time: Extract git info via shell commands (Makefile) or goreleaser templates
  - Compilation: Pass info to Go compiler via `-X` ldflags
  - Runtime: Resolve functions in version.go retrieve and display the info

- **Installation Methods**: The build configuration enables multiple installation paths while maintaining version consistency:
  - `make install` - Used by developers building from a local checkout; it builds first, then copies the result into `~/.local/bin`
  - `go install github.com/steveyegge/beads/cmd/bd@latest` - End-user direct Go installation path
  - GitHub releases - Goreleaser-built binaries for all platforms
  - Homebrew - Pre-built binaries installed via `brew install beads`
  - npm - Node.js package that downloads pre-built binaries via postinstall hook
  - `./scripts/install.sh` - User-friendly installer that prefers the end-user install/build path rather than clone-local maintainer workflow

- **Release Automation**: Goreleaser configuration integrates with GitHub Actions and the release process documented in `@/RELEASING.md`, ensuring released binaries have full version info.

### Core Implementation

**Makefile** (`@/Makefile`, `build` / `install` targets):

The current maintainer path is `make install`, which works in two stages:

1. `make build` compiles `./cmd/bd` with `go build`, sets `main.Build` from the short git hash, and relies on Go's embedded VCS metadata for commit and branch details.
2. `make install` copies that built binary into `~/.local/bin`, creates the `beads` alias, and runs the up-to-date guard unless `make install-force` is used.

That means clone-local maintainers should use `make install`, not raw `go build -o bd ./cmd/bd`, when they want their checkout to become the active CLI on the machine.

**Goreleaser Configuration** (`@/.goreleaser.yml`, lines 11-95):

Goreleaser builds binaries for all platforms and sets version information via ldflags. Each of the 5 build configurations uses identical ldflag patterns:

```yaml
ldflags:
  - -s -w
  - -X main.Version={{.Version}}
  - -X main.Build={{.ShortCommit}}
  - -X main.Commit={{.Commit}}
  - -X main.Branch={{.Branch}}
```

Platform configurations:
1. **bd-linux-amd64** (lines 12-26): Linux 64-bit Intel
2. **bd-linux-arm64** (lines 28-44): Linux 64-bit ARM (Apple Silicon support)
3. **bd-darwin-amd64** (lines 46-60): macOS 64-bit Intel
4. **bd-darwin-arm64** (lines 62-76): macOS 64-bit ARM (M1/M2/M3)
5. **bd-windows-amd64** (lines 78-95): Windows 64-bit Intel with additional `-buildmode=exe` flag

The ldflags explained:
- `-s -w`: Strip debug symbols to reduce binary size
- `-X main.Version`: Semantic version (e.g., "0.29.0") from git tag
- `-X main.Build`: Short commit hash for quick reference
- `-X main.Commit`: Full commit hash for precise identification
- `-X main.Branch`: Branch name for build context

Goreleaser template variables:
- `{{.Version}}`: The release version from git tag
- `{{.ShortCommit}}`: First 7 characters of commit (used for Build variable)
- `{{.Commit}}`: Full commit hash
- `{{.Branch}}`: Current git branch

**Installation Script** (`@/scripts/install.sh`):

Provides a user-friendly way to install beads outside a maintainer checkout:
- Prefers `go install github.com/steveyegge/beads/cmd/bd@latest` when Go is available
- Falls back to source builds when necessary
- Immediately verifies installation by running `bd version`

**Version Resolution Chain** (`@/cmd/bd/version.go`, lines 116-163):

The version.go file implements functions that retrieve the injected information:

1. **resolveCommitHash()** (lines 116-130):
   - First checks if `Commit` package variable was set via ldflag (most reliable)
   - Falls back to `runtime/debug.ReadBuildInfo()` to extract VCS info automatically embedded by `go build`
   - Returns empty string if neither source has data

2. **resolveBranch()** (lines 139-163):
   - First checks if `Branch` package variable was set via ldflag (most reliable)
   - Falls back to `runtime/debug.ReadBuildInfo()` to extract VCS branch info
   - Falls back to `git symbolic-ref --short HEAD` for runtime detection
   - Returns empty string if none available

3. **Output Formatting** (lines 52-58):
   - Displays commit and branch in human-readable format: `bd version 0.29.0 (dev: main@7e70940)`

### Things to Know

**Critical Design Decision - Why Explicit Ldflags**:

The Go toolchain (as of 1.18+) can automatically embed VCS information when compiling with `go build`, but this does NOT happen with `go install`. This creates an asymmetry:
- `go build ./cmd/bd` → automatically embeds vcs.revision and vcs.branch
- `go install ./cmd/bd` → does NOT embed VCS info automatically

The solution is to ensure each installation path injects or preserves the right metadata for its mechanism. In practice this means:
- Users who run `make install` get the repo's `go build` VCS metadata plus the short-build marker from the Makefile
- Users who run plain `go install ./cmd/bd` from a checkout still need explicit ldflags if they want local branch/commit detail in the binary
- Released binaries from goreleaser have full version info (handled by goreleaser templates)
- The version command is consistent regardless of installation method

**Issue #503 Root Cause**:

The original system relied on Go's automatic VCS embedding which only works with `go build`. When released binaries (built via goreleaser) or installed binaries (via `go install`) came without explicit ldflags, the `bd version` command couldn't report commit and branch information.

The fix adds explicit ldflag injection at all build points, creating a reliable pipeline independent of Go's automatic VCS embedding feature.

**Ldflag Variable Names**:

The variables in `@/cmd/bd/version.go` (lines 15-23) must match the ldflag paths in build configurations:
- `main.Version` → Version variable
- `main.Build` → Build variable
- `main.Commit` → Commit variable
- `main.Branch` → Branch variable

These are fully qualified with the package name (`main`) because the ldflag syntax is: `-X package.Variable=value`

**Build-Time vs Runtime Information**:

- **Build-Time** (injected via ldflags): Git commit and branch at the moment of compilation
  - Most reliable and consistent
  - Does not change after binary is created
  - Reflects the exact code in the binary

- **Runtime** (fallback via git commands): Current branch of the source directory
  - Used only if build-time info is not available
  - Can differ from build-time info (e.g., if working directory changes)
  - Useful for development but less reliable

**Release Process Integration**:

The build configuration integrates with `@/RELEASING.md`:
1. Version tag is pushed to GitHub (e.g., `v0.29.0`)
2. GitHub Actions/goreleaser automatically builds binaries for all platforms
3. Goreleaser uses git metadata to populate `{{.Commit}}` and `{{.Branch}}` template variables
4. Released binaries include full version info
5. Users installing via any channel get consistent version reporting

**Testing Version Information**:

The test file `@/cmd/bd/version_test.go` includes:
- `TestResolveCommitHash`: Verifies ldflag values are prioritized
- `TestResolveBranch`: Verifies ldflag values are prioritized  
- `TestVersionOutputWithCommitAndBranch`: Verifies output formatting with real values

These tests simulate build-time injection by directly setting the package variables, ensuring the resolution chain works correctly.

**Multi-Platform Consistency**:

All 5 goreleaser build configurations use identical ldflag patterns. This ensures:
- macOS (Intel and ARM) binaries have full version info
- Linux (Intel and ARM) binaries have full version info
- Windows binaries have full version info
- Users on any platform have equal access to version information

**Dependency Requirements**:

- **Makefile**: Requires `git` and `bash` to be available at install time
- **Goreleaser**: Requires git tags and repository metadata (automatic in CI/CD)
- **Scripts**: Require `git` and `go` in PATH

All are standard tools in development and CI/CD environments.

Created and maintained by Nori.
