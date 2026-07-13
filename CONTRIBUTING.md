# Contributing to bd

Thank you for your interest in contributing to bd! This document provides guidelines and instructions for contributing.

## Development Setup

### Prerequisites

- Go (see `go.mod` for the required version; currently 1.26+)
- Git
- A C compiler (CGO is required for the embedded Dolt database)
- (Optional) golangci-lint for local linting
- ICU headers are **not required** for building -- see [engdocs/ICU-POLICY.md](engdocs/ICU-POLICY.md)

### Getting Started

```bash
# Clone the repository
git clone https://github.com/gastownhall/beads
cd beads

# Build the project (uses gms_pure_go tag via Makefile)
make build

# Run tests (uses correct build tags automatically)
make test

# Build and install locally to ~/.local/bin
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
# Run all tests (recommended — uses correct build tags)
make test

# Run tests with coverage
go test -tags gms_pure_go -v -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run specific package tests
go test -tags gms_pure_go ./internal/storage/dolt/ -v

# Run tests with race detection
go test -tags gms_pure_go -race ./...
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

**Note**: The linter currently reports ~100 warnings. These are documented false positives and idiomatic Go patterns (deferred cleanup, Cobra interface requirements, etc.). See [engdocs/LINTING.md](engdocs/LINTING.md) for details. When contributing, focus on avoiding *new* issues rather than the baseline warnings.

CI will automatically run linting on all pull requests.

## Making Changes

### Project Scope

Before adding new feature surface area, read
[engdocs/PROJECT_CHARTER.md](engdocs/PROJECT_CHARTER.md). Beads owns issue tracking
primitives. It should not encode orchestration-layer policy, become a storage
engine, or expand the database schema when issue metadata is sufficient.

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
- Lead the PR with a brief plain-language `What` and `Why` so reviewers can grasp the goal without reading the diff. `.github/PULL_REQUEST_TEMPLATE.md` is a starting scaffold — replace, expand, or delete sections to fit your change.

### ZFC (Zero Framework Cognition)

If you are contributing code that involves AI decision-making or orchestration, understand and follow the [ZFC principles](https://steve-yegge.medium.com/zero-framework-cognition-a-way-to-build-resilient-ai-applications-56b090ed3e69). In short: keep the smarts in the AI models, keep the code as dumb orchestration. Do not add heuristics, keyword matching, ranking logic, or semantic analysis in application code — delegate cognitive decisions to AI.

## Testing Guidelines

For how to run tests, see [engdocs/TESTING.md](engdocs/TESTING.md). For what to
test and why (the test pyramid and tiering we follow), see
[engdocs/TESTING_PHILOSOPHY.md](engdocs/TESTING_PHILOSOPHY.md).

### Before Opening a PR

- Run `make test` (or `./scripts/test.sh`) locally and make sure it passes.
- Add tests for new functionality; extend existing tests when fixing bugs.
- Write table-driven tests for multiple scenarios, use descriptive test
  names, use `t.Run()` for subtests, and clean up resources (database
  files, etc.) in test teardown.
- If you hit a test failure unrelated to your change, don't silently skip
  it -- check `.test-skip` and file an issue if it's not already tracked
  (see [engdocs/TESTING.md](engdocs/TESTING.md#known-broken-tests)).
- Ensure CI passes (`make ci-pr-core`, `make ci-pr-policy`, `make
  ci-pr-lint`) before requesting review.
- If your change touches ICU or build tags, see
  [engdocs/ICU-POLICY.md](engdocs/ICU-POLICY.md) for the policy and rationale.

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

Maintainers and agents follow [PR_MAINTAINER_GUIDELINES.md](PR_MAINTAINER_GUIDELINES.md) when triaging, landing, transforming, or closing PRs.

### Refactoring Campaign PR Intake Checklist

Before starting a rewrite, cleanup, or large refactoring pass, maintainers and agents must review open contributor PRs that touch the same area. Use this checklist to decide whether to merge, rebase, incorporate, or close each PR.

1. Identify overlap:
   - Read the PR description, changed files, linked issues, and latest review comments.
   - Compare the PR scope with the planned refactor and note any shared files, commands, migrations, tests, docs, or release paths.
   - If the PR is unrelated, leave it alone unless the refactor would still create a merge conflict.

2. Prefer clean merges:
   - If the PR is focused, passing CI, and aligned with current design, review it as the first option.
   - Merge it before the refactor when that reduces conflict risk.
   - Preserve the contributor's commits and attribution unless the contributor agrees to a squash or rework.

3. Request a rebase when needed:
   - Ask for a rebase if the PR is still valid but conflicts with main or depends on code that has moved.
   - Give concrete instructions about the new target files or APIs.
   - Do not rewrite the same work in parallel while waiting unless there is a release blocker or security issue.

4. Preserve tests and intent:
   - Treat contributor tests as part of the contribution, not optional scaffolding.
   - If a refactor supersedes implementation code, port the tests or explain why they are invalid.
   - Keep user-facing behavior, docs examples, and regression coverage intact unless the PR is explicitly changing the contract.

5. Close superseded PRs with explicit rationale:
   - Close only after commenting with the replacement commit, PR, or issue.
   - Explain what was preserved, what changed, and why the original branch will not be merged.
   - Thank the contributor and invite follow-up if their use case was not fully covered.

6. Leave an audit trail:
   - Link the intake decision from the refactor PR or Beads issue.
   - Record any follow-up work as Beads issues instead of hidden notes.
   - Call out contributor-owned tests or behavior in the refactor PR summary.

## Code Review Process

All contributions go through code review:

1. Automated checks (tests, linting) must pass
2. At least one maintainer approval required
3. Address review feedback
4. Maintainer will merge when ready

## Development Tips

### Testing Locally

```bash
# Build and install your changes
make install

# Test specific functionality
bd init --prefix test
bd create "Test issue" -p 1 -t bug
bd dep add test-2 test-1
bd ready
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

If the build fails with a `vendorHash` mismatch, run `./scripts/update-nix-vendorhash.sh` to recompute and update `default.nix`, or update it manually with the `got:` hash from the error message and rebuild.

The `nix build` CI job (`.github/workflows/nix-build.yml`) runs on any PR that touches `go.mod`, `go.sum`, `default.nix`, `flake.nix`, or `flake.lock`, so dependabot bumps that invalidate `vendorHash` fail loudly instead of silently breaking Nix users on main. For dependabot Go-module bumps specifically, `.github/workflows/update-vendor-hash.yml` runs the same `update-nix-vendorhash.sh` script and pushes the hash bump back to the dependabot branch automatically (note: GitHub does not retrigger `pull_request` workflows for `GITHUB_TOKEN`-authored commits, so a maintainer may need to re-run `nix build .#default` once after the auto-fix push to mark the gate green).

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

- Check existing [issues](https://github.com/gastownhall/beads/issues)
- Open a new issue for questions
- Review [README.md](README.md) and other documentation

## License

By contributing, you agree that your contributions will be licensed under the MIT License.

## Code of Conduct

Be respectful and professional in all interactions. We're here to build something great together.

---

Thank you for contributing to bd! 🎉
