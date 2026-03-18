# Contributing to bd

Thank you for your interest in contributing to bd! This document provides guidelines and instructions for contributing.

## Development Setup

### Prerequisites

- Go 1.24 or later
- Git
- (Optional) golangci-lint for local linting

### Getting Started

```bash
# Clone the repository
git clone https://github.com/steveyegge/beads
cd beads

# Build and install locally
make install

# Fast local / PR-like test lane
make test-short

# Broader local lane
make test
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
# Fast local / PR-like lane
make test-short

# Broader local lane (non-short; `make test` also enables coverage)
make test

# Full CGO-enabled lane for Dolt-sensitive changes
make test-full-cgo

# Coverage run that keeps the repo wrapper / skip behavior
TEST_COVER=1 TEST_COVERPROFILE=coverage.out ./scripts/test.sh ./...
go tool cover -html=coverage.out

# Run specific package tests
./scripts/test.sh -short ./internal/storage/dolt/...

# Run a CI-style race check when you specifically need it
go test -race -short ./...
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

### Pull Requests

- Keep PRs focused on a single feature or fix
- Include tests for new functionality
- Update documentation as needed
- Ensure CI passes before requesting review
- Respond to review feedback promptly

## Testing Guidelines

### Test Strategy

We now use a few practical lanes instead of a simple fast/slow split:

- **Fast local / PR-like lane**: `make test-short`
- **Broader local lane**: `make test`
- **Full CGO lane**: `make test-full-cgo`
- **Heavier runtime-manager and integration coverage**: owned by dedicated CI lanes

See [docs/TESTING.md](docs/TESTING.md) for the current lane definitions and
tradeoffs.

### Running Tests

```bash
# Fast local / PR-like lane (recommended for active development)
make test-short

# Broader local lane before opening a pull request
make test

# Full CGO lane when changing Dolt-sensitive code
make test-full-cgo

# CI-style race check when you specifically need it
go test -race -short ./...
```

**When to use the fast lane (`make test-short`):**
- During active development for fast feedback loops
- When making small changes that don't affect integration points
- When you want the closest local match to the default PR CI path

**When to step up to broader lanes:**
- Use `make test` before opening a pull request or when you want the broader
  non-short local surface
- Use `make test-full-cgo` after changing Dolt-sensitive or CGO-relevant code
- Use `go test -race -short ./...` when you specifically want the CI-style race
  check

### Writing Tests

- Write table-driven tests when testing multiple scenarios
- Use descriptive test names that explain what is being tested
- Clean up resources (database files, etc.) in test teardown
- Use `t.Run()` for subtests to organize related test cases
- Mark slow tests with `if testing.Short() { t.Skip("slow test") }`

### Dual-Mode Testing Pattern

**IMPORTANT**: bd still has product requirements around embedded and server behavior, but the current CGO integration helper only exercises the server-backed path. Do not describe it as embedded+server parity coverage unless the helper actually runs both again.

```go
// cmd/bd/integration_test_helpers_test.go provides the current server-backed harness

func TestMyCommand(t *testing.T) {
    // This helper currently runs the shared server-backed integration path.
    RunServerModeTest(t, "my_test", func(t *testing.T, env *DualModeTestEnv) {
        // Create test data using mode-agnostic helpers
        issue := &types.Issue{
            Title:     "Test issue",
            IssueType: types.TypeTask,
            Status:    types.StatusOpen,
            Priority:  2,
        }
        if err := env.CreateIssue(issue); err != nil {
            t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
        }

        // Verify behavior - works in both modes
        got, err := env.GetIssue(issue.ID)
        if err != nil {
            t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
        }
        if got.Title != "Test issue" {
            t.Errorf("[%s] wrong title: got %q", env.Mode(), got.Title)
        }
    })
}
```

Available `DualModeTestEnv` helper methods:
- `CreateIssue(issue)` - Create an issue
- `GetIssue(id)` - Retrieve an issue by ID
- `UpdateIssue(id, updates)` - Update issue fields
- `DeleteIssue(id, force)` - Delete (tombstone) an issue
- `AddDependency(from, to, type)` - Add a dependency
- `ListIssues(filter)` - List issues matching filter
- `GetReadyWork()` - Get issues ready for work
- `AddLabel(id, label)` - Add a label to an issue
- `Mode()` - Returns the active harness mode (`"server"` today)

Run the current server-backed helper:
```bash
# Run server-backed integration helper tests (requires integration tag)
go test -v -tags integration -run "TestMyCommand" ./cmd/bd/
```

`RunDualModeTest` still exists as a legacy wrapper for older tests, but it is
currently just an alias for the server-backed helper. If we restore true
embedded+server coverage later, update this section at the same time.

Example:

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
make build && ./bd init --prefix test

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
