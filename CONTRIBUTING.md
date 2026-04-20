# Contributing to bd

Thank you for your interest in contributing to bd! This document provides guidelines and instructions for contributing.

## Development Setup

### Prerequisites

- Go 1.24 or later
- Git
- A C compiler (CGO is required for the embedded Dolt database)
- (Optional) golangci-lint for local linting
- ICU headers are **not required** for building -- see [docs/ICU-POLICY.md](docs/ICU-POLICY.md)

### Getting Started

```bash
# Clone the repository
git clone https://github.com/steveyegge/beads
cd beads

# Build the project (uses gms_pure_go tag via Makefile)
make build

# Run tests
go test ./...

# Run with race detection
go test -race ./...

# Build and install locally
make install
```

## Project Structure

```
beads/
├── cmd/bd/              # CLI entry point and commands
├── internal/
│   ├── types/           # Core data types (Issue, Dependency, etc.)
│   └── storage/         # Storage interface and implementations
│       └── dolt/        # Dolt database backend
├── .golangci.yml        # Linter configuration
└── .github/workflows/   # CI/CD pipelines
```

## Running Tests

```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -v -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run specific package tests
go test ./internal/storage/dolt/ -v

# Run tests with race detection
go test -race ./...
```

## Code Style

We follow standard Go conventions:

- Use `gofmt` to format your code (runs automatically in most editors)
- Follow the [Effective Go](https://golang.org/doc/effective_go) guidelines
- Keep functions small and focused
- Write clear, descriptive variable names
- Add comments for exported functions and types

### Linting

We use golangci-lint for code quality checks:

```bash
# Install golangci-lint
brew install golangci-lint  # macOS
# or
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Run linter
golangci-lint run ./...
```

**Note**: The linter currently reports ~100 warnings. These are documented false positives and idiomatic Go patterns (deferred cleanup, Cobra interface requirements, etc.). See [docs/LINTING.md](docs/LINTING.md) for details. When contributing, focus on avoiding *new* issues rather than the baseline warnings.

CI will automatically run linting on all pull requests.

## Making Changes

### Workflow

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Add tests for new functionality
5. Run tests and linter locally
6. Commit your changes with clear messages
7. Push to your fork
8. Open a pull request

### Commit Messages

Write clear, concise commit messages:

```
Add cycle detection for dependency graphs

- Implement recursive CTE-based cycle detection
- Add tests for simple and complex cycles
- Update documentation with examples
```

### Pull Request Hygiene

**One issue per PR, and one PR per issue.** No piggybacking or riders — each PR should address exactly one thing.

- Keep PRs focused on a single feature or fix
- Do not include unrelated changes, cleanup, or "while I'm here" improvements
- Do not include `.beads/` data (database, JSONL) in your PR
- Make sure there are no extra generated or garbage files in your diff
- Include tests for new functionality
- Update documentation as needed
- Ensure CI passes before requesting review
- Respond to review feedback promptly

### ZFC (Zero Framework Cognition)

If you are contributing code that involves AI decision-making or orchestration, understand and follow the [ZFC principles](https://steve-yegge.medium.com/zero-framework-cognition-a-way-to-build-resilient-ai-applications-56b090ed3e69). In short: keep the smarts in the AI models, keep the code as dumb orchestration. Do not add heuristics, keyword matching, ranking logic, or semantic analysis in application code — delegate cognitive decisions to AI.

## Testing Guidelines

### Test Strategy

We use a two-tier testing approach:

- **Fast tests** (unit tests): Run on every PR via CI with `-short` flag (~2s)
- **Slow tests** (integration tests): Run nightly with full git operations (~14s)

Slow tests use `testing.Short()` to skip when `-short` flag is present.

### Running Tests

```bash
# Fast tests (recommended for development - skips slow tests)
# Use this for rapid iteration during development
go test -short ./...

# Full test suite (before committing - includes all tests)
# Run this before pushing to ensure nothing breaks
go test ./...

# With race detection and coverage
go test -race -coverprofile=coverage.out ./...
```

**When to use `-short`:**
- During active development for fast feedback loops
- When making small changes that don't affect integration points
- When you want to quickly verify unit tests pass

**When to use full test suite:**
- Before committing and pushing changes
- After modifying git operations or multi-clone scenarios
- When preparing a pull request

### Writing Tests

- Write table-driven tests when testing multiple scenarios
- Use descriptive test names that explain what is being tested
- Clean up resources (database files, etc.) in test teardown
- Use `t.Run()` for subtests to organize related test cases
- Mark slow tests with `if testing.Short() { t.Skip("slow test") }`

### CGO vs Non-CGO Tests

Tests are split into two categories based on whether they need the embedded Dolt database (which requires CGO):

- **Non-CGO tests** (no build tag): Unit tests for CLI parsing, helpers, and pure logic. These run everywhere.
- **CGO tests** (`//go:build cgo`): Integration tests that create a real Dolt database. Files often use the `_embedded_test.go` suffix.

```bash
# Fast non-CGO tests (recommended for development)
make test                     # or: go test -short ./...

# Full CGO-enabled suite (before committing)
make test-full-cgo            # or: ./scripts/test-cgo.sh ./...

# Run a specific CGO test
./scripts/test-cgo.sh -run '^TestMyFeature$' ./cmd/bd/...
```

On macOS, always use the script or Make target for CGO tests -- they configure the required ICU linker flags automatically.

### ICU and Build Tags

All production builds use `-tags gms_pure_go` to avoid ICU runtime dependencies.
**Do not add ICU linker flags to the Makefile or `.buildflags`.**
See [docs/ICU-POLICY.md](docs/ICU-POLICY.md) for the full policy and rationale.

### Test Isolation with `t.TempDir()`

Database tests use `t.TempDir()` for isolation so each test gets a clean environment and nothing touches the production database:

```go
func TestMyFeature(t *testing.T) {
    tmpDir := t.TempDir()
    dbPath := filepath.Join(tmpDir, "test.db")
    store := newTestStoreWithPrefix(t, dbPath, "bd")

    ctx := context.Background()
    issue := &types.Issue{
        ID:     "bd-1",
        Title:  "Test issue",
        Status: types.StatusOpen,
    }
    if err := store.CreateIssue(ctx, issue, "test"); err != nil {
        t.Fatalf("CreateIssue failed: %v", err)
    }
    // ... assertions ...
}
```

Test helpers in `cmd/bd/test_helpers_test.go` provide database setup functions like `newTestStore`, `newTestStoreWithPrefix`, and `newTestStoreSharedBranch` (which uses branch-per-test isolation to avoid expensive CREATE/DROP DATABASE overhead).

### Table-Driven Test Example

```go
func TestIssueValidation(t *testing.T) {
    tests := []struct {
        name    string
        issue   *types.Issue
        wantErr bool
    }{
        {
            name:    "valid issue",
            issue:   &types.Issue{Title: "Test", Status: types.StatusOpen, Priority: 2},
            wantErr: false,
        },
        {
            name:    "missing title",
            issue:   &types.Issue{Status: types.StatusOpen, Priority: 2},
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.issue.Validate()
            if (err != nil) != tt.wantErr {
                t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

## Documentation

- Update README.md for user-facing changes
- Update relevant .md files in the project root
- Add inline code comments for complex logic
- Include examples in documentation

## Feature Requests and Bug Reports

### Reporting Bugs

Include in your bug report:
- Steps to reproduce
- Expected behavior
- Actual behavior
- Version of bd (`bd version` if implemented)
- Operating system and Go version

### Feature Requests

When proposing new features:
- Explain the use case
- Describe the proposed solution
- Consider backwards compatibility
- Discuss alternatives you've considered

## Your PR Will Not Be Overwritten

This project uses AI agents for maintenance. We've established strict rules to protect contributor work:

- **Your PR has priority.** If you've submitted a PR, agents must review and build on your work — not rewrite it from scratch.
- **Your tests matter.** Agents must preserve contributor tests unless they're actually wrong.
- **You'll get attribution.** Your commits and `Co-authored-by:` will be preserved.
- **No silent closes.** Your PR will never be auto-closed by a parallel rewrite. If changes are needed, they'll be discussed on your PR.

If any of this goes wrong, please open an issue — we take contributor experience seriously.

## Code Review Process

All contributions go through code review:

1. Automated checks (tests, linting) must pass
2. At least one maintainer approval required
3. Address review feedback
4. Maintainer will merge when ready

## Development Tips

### Testing Locally

```bash
# Build and test your changes quickly
go build -o bd ./cmd/bd && ./bd init --prefix test

# Test specific functionality
./bd create "Test issue" -p 1 -t bug
./bd dep add test-2 test-1
./bd ready
```

### Database Inspection

```bash
# Inspect the Dolt database directly
bd query "SELECT * FROM issues"
bd query "SELECT * FROM dependencies"
bd query "SELECT * FROM events WHERE issue_id = 'test-1'"
```

### Updating Nix flake.lock (without nix installed)

The `flake.lock` file pins a specific nixpkgs revision. When `go.mod` bumps the Go version beyond what's in the pinned nixpkgs, the Nix CI job will fail. To update `flake.lock` without installing nix locally, use Docker:

```bash
# Update flake.lock
docker run --rm -v $(pwd):/workspace -w /workspace nixos/nix \
  sh -c 'echo "experimental-features = nix-command flakes" >> /etc/nix/nix.conf && nix flake update'

# Verify the build works
docker run --rm -v $(pwd):/workspace -w /workspace nixos/nix \
  sh -c 'echo "experimental-features = nix-command flakes" >> /etc/nix/nix.conf && nix build .#default && ./result/bin/bd version'
```

If the build fails with a `vendorHash` mismatch, update `default.nix` with the `got:` hash from the error message and rebuild.

The `nix build` CI job (`.github/workflows/nix-build.yml`) runs on any PR that touches `go.mod`, `go.sum`, `default.nix`, `flake.nix`, or `flake.lock`, so dependabot bumps that invalidate `vendorHash` fail loudly instead of silently breaking Nix users on main.

### Debugging

Use Go's built-in debugging tools:

```bash
# Run with verbose logging
go run ./cmd/bd -v create "Test"

# Use delve for debugging
dlv debug ./cmd/bd -- create "Test issue"
```

## Release Process

(For maintainers)

1. Update version in code
2. Update CHANGELOG.md
3. Tag release: `git tag v0.x.0`
4. Push tag: `git push origin v0.x.0`
5. GitHub Actions will build and publish

## Questions?

- Check existing [issues](https://github.com/steveyegge/beads/issues)
- Open a new issue for questions
- Review [README.md](README.md) and other documentation

## License

By contributing, you agree that your contributions will be licensed under the MIT License.

## Code of Conduct

Be respectful and professional in all interactions. We're here to build something great together.

---

Thank you for contributing to bd! 🎉
