# Contributing to bd

Thank you for your interest in contributing to bd! This document provides guidelines and instructions for contributing.

## Issues and pull requests

GitHub Issues is the public tracker. Use an issue when it adds context
reviewers need: a user-visible bug, a behavior or design change worth
discussing first, or work that spans several pull requests. The issue holds
why the change is needed, what it affects, and how we will know it works; file
it with the bug or feature form before or alongside your pull request, and it
does not need maintainer approval first. Small, self-explanatory changes
(typos, flaky tests, refactors, CI or docs tweaks) can go straight to a pull
request whose description explains the why.

### Triage labels

| Label | Meaning | Applied by |
|---|---|---|
| `status/needs-triage` | Awaiting initial triage. | Automation, on every new issue |
| `status/needs-info` | Waiting on essential information from the reporter. | Maintainers |
| `status/needs-repro` | Needs a reproducible bug report. | Maintainers |
| `status/needs-design` | The direction needs a design decision before work starts. | Maintainers |
| `status/accepted` | Confirmed and on our radar. | Maintainers |

Issues left in `status/needs-info` or `status/needs-repro` are closed after 14
days without a reply to the request; reply with the details and a
maintainer will reopen them.
Priority (`priority/p0`–`priority/p3`) and kind (`kind/bug`, `kind/feature`,
`kind/docs`, `kind/chore`) labels are set during triage.

### Pull request pipeline labels

These labels drive the maintainers' automated review and merge workflow. They
are applied by maintainers and automation; contributors don't need to set
them.

| Label | Meaning |
|---|---|
| `status/needs-review` | Request the PR review workflow. |
| `status/needs-review-auto` | Request the automated PR review workflow. |
| `status/reviewing` | The PR review workflow is running. |
| `status/review-failed` | The PR review workflow failed before merge-ready. |
| `status/merge-ready` | The PR is ready for the merge workflow. |
| `status/merge-queued` | Queued for deterministic PR-review merge. |
| `status/merge-failed` | The merge queue needs operator attention. |
| `status/needs-bugflow` | Request the bugflow investigation workflow. |

The current priorities are on the [roadmap](ROADMAP.md); work in a priority
area is reviewed first.

## Development Setup

### Prerequisites

- [Bazelisk](https://github.com/bazelbuild/bazelisk), installed as `bazel`
  (it reads `.bazelversion`). **Required:** Bazel is the build and test system
  CI gates on, and the pre-commit (nogo lint) and pre-push (test suite) hooks
  run it.
- Go (see `go.mod` for the required version; currently 1.26+), for `make
  install` and the `go test` inner loop
- Git
- A C compiler (CGO is required for the embedded Dolt database)
- ICU headers are **not required** for building -- see [engdocs/ICU-POLICY.md](engdocs/ICU-POLICY.md)

### Getting Started

```bash
# Clone the repository
git clone https://github.com/gastownhall/beads
cd beads

# Read rbe-west's anonymous, read-only cache: everything CI already built and
# tested is a cache hit, and nothing you build is uploaded.
echo 'build --config=fork-cache' >> .bazelrc.local

# Run the test suite CI gates on (bazel test //... --config=ci)
make test

# Build and install bd to ~/.local/bin (also enables the git hooks)
make install
```

### Building and Testing

Bazel is the gate: `.github/workflows/bazel.yml` runs every pull request's
tests as `bazel test` lanes, and nogo (lint + vet), gofmt and the repository
guards exist only as Bazel targets. The make targets run the same commands:

| Command | Runs |
|---|---|
| `make test` | `bazel test //... --config=ci`: the test lane (unit tests, nogo, gofmt, repository guards) |
| `make check` | the testing.Short policy, `make ci-pr-lint` and `make test` |
| `make ci-pr-lint` | nogo natively plus the windows/amd64 and darwin/arm64 passes |
| `make check-docs` | the Bazel docsync and doc-freshness tests, then the CLI flag check |
| `bazel test //... --config=integration` | the integration lane; [engdocs/TESTING.md](engdocs/TESTING.md) lists every other lane's command |

Where actions run is your choice, set in the gitignored `.bazelrc.local` (or
per command with `make test BAZEL_FLAGS=--config=...`):

- `--config=fork-cache` (contributors): reads the anonymous cache, runs misses
  on your machine, uploads nothing.
- `--config=remote-exec` (maintainers with an rbe-west client certificate):
  executes remotely; the executor and TLS lines stay in your `user.bazelrc` or
  `.bazelrc.local`.

`go test` (`./scripts/test.sh`, or `make test-go` / `make check-go` /
`make check-docs-go`) still works as an inner-loop convenience, but CI does not
enforce it and it skips nogo, gofmt and the guards. Finish with `make test`.

## Project Structure

```
beads/
├── cmd/bd/              # CLI entry point and commands
├── internal/
│   ├── types/           # Core data types (Issue, Dependency, etc.)
│   └── storage/         # Storage interface and implementations
│       └── dolt/        # Dolt database backend
├── .golangci.yml        # Linter configuration (applied by nogo, tools/nogo)
└── .github/workflows/   # CI/CD pipelines
```

## Running Tests

Use the canonical [testing guide](engdocs/TESTING.md) to choose focused tests,
the proportional validation budget, and any applicable CI wrapper. The setup
and safety notes in this file supplement that guide; they do not define a
second test policy.

## Code Style

We follow standard Go conventions:

- Use `gofmt` to format your code (runs automatically in most editors)
- Follow the [Effective Go](https://golang.org/doc/effective_go) guidelines
- Keep functions small and focused
- Write clear, descriptive variable names
- Add comments for exported functions and types

### Linting

Lint and vet run as nogo under Bazel: go test's vet checks plus the
golangci-lint linters `.golangci.yml` enables, the same analyzers CI gates on.

```bash
# Run the required lint and vet gate (native, windows and darwin)
make ci-pr-lint

# Faster: only the Bazel packages of your changed Go files
make lint-changed
```

`make ci-pr-lint` must pass with zero issues. It analyzes the repository's
normal `gms_pure_go` build and cross-checks the Windows- and macOS-only
non-cgo code. Accepted intentional patterns are encoded narrowly in
`.golangci.yml`; do not ignore a failing baseline. See
[engdocs/LINTING.md](engdocs/LINTING.md) for the full policy.

CI runs the same analyzers on all pull requests, in the Bazel test lane.

## Making Changes

### Project Scope

Before adding new feature surface area, read
[engdocs/PROJECT_CHARTER.md](engdocs/PROJECT_CHARTER.md). Beads owns issue tracking
primitives. It should not encode orchestration-layer policy, become a storage
engine, or expand the database schema when issue metadata is sufficient.

### Workflow

1. If the change warrants one, find or file an issue (see [Issues and pull requests](#issues-and-pull-requests))
2. Fork the repository and create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Add tests for new functionality
5. Run tests and linter locally
6. Commit your changes with clear messages
7. Push to your fork
8. Open a pull request that explains the change and says `Closes #<issue>` when there is one

### Commit Messages

Use [Conventional Commits](https://www.conventionalcommits.org/):
`type(scope): summary`, where type is one of `fix`, `feat`, `docs`, `test`,
`refactor`, `chore`, `ci`, or `perf`.

```
feat(dep): add cycle detection for dependency graphs

- Implement recursive CTE-based cycle detection
- Add tests for simple and complex cycles
- Update documentation with examples
```

### Pull Request Hygiene

**One issue per PR, and one PR per issue.** No piggybacking or riders — each PR should address exactly one thing.

Read [CONTRIBUTING_PR_GUIDELINES.md](CONTRIBUTING_PR_GUIDELINES.md) for the
layering rules (schema → storage/issueops → cmd/bd, one layer per PR) and the
repro and benchmark evidence reviewers expect.

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

For test commands, test design, and PR-readiness gates, see the canonical
[engdocs/TESTING.md](engdocs/TESTING.md).

### Before Opening a PR

- Follow the proportional validation budget in
  [engdocs/TESTING.md](engdocs/TESTING.md): docs-only changes use docs checks;
  Go changes use focused and affected-package tests plus one final `make test`
  (the Bazel test lane).
- If you hit a test failure unrelated to your change, don't silently skip
  it -- check `.test-skip` and file an issue if it's not already tracked
  (see [engdocs/TESTING.md](engdocs/TESTING.md#failures-skips-and-review)).
- Run a named CI wrapper only when its risk or surface is affected, or when
  reproducing that CI check.
- If your change touches ICU or build tags, see
  [engdocs/ICU-POLICY.md](engdocs/ICU-POLICY.md) for the policy and rationale.

## Documentation

- Update README.md for user-facing changes
- Update relevant .md files in the project root
- Add inline code comments for complex logic
- Include examples in documentation

## Storage filter conventions

### `IssueFilter.MaxRows` opt-out rule (be-x42v)

`types.IssueFilter` carries a defensive row cap (`MaxRows int`,
`MaxRowsSource string`) that the storage layer enforces via
`*issueops.ErrTooManyRows`. The cap is wired from `--max-rows` /
`BEADS_MAX_ROWS` on user-facing commands listed in designer §4 of be-x42v
(`bd list`, `bd ready`, `bd dep tree`, `bd find-duplicates`, `bd graph`,
plus env-only on the doctor family).

**Rule for new code that builds an `IssueFilter`:** if your call site is
NOT on the designer's wired-up list, you **MUST** explicitly initialize
`filter.MaxRows = 0` and `filter.MaxRowsSource = ""`. This makes the
opt-out intentional in code review and survives future refactors that
might otherwise let the env var leak into a sweep path that must not
abort (export, gc, jira sync, migrate-issues, etc.).

The opt-out test gates (be-x42v.4) enforce this for the
export / migrate / jira / cleanup / gc paths today. New write-side or
round-trip paths should pattern-match on those tests.

## Feature Requests and Bug Reports

Use the issue forms: [bug report](https://github.com/gastownhall/beads/issues/new?template=bug_report.yml)
or [feature request](https://github.com/gastownhall/beads/issues/new?template=feature_request.yml).
The feature form asks for the motivation, impact, risk and compatibility,
and verification plan up front, so a maintainer can accept the issue without
a round trip. Questions go to
[Discussions](https://github.com/gastownhall/beads/discussions).

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

On a PR, this is covered by PR Risk's required `test-nix` job (`.github/workflows/pr-risk.yml`), which runs `nix run .#default -- --help` plus `nix flake check -L` (an evaluation of every flake output; the flake has no test checks) on every PR touching `go.mod`, `go.sum`, `default.nix`, `flake.nix`, or `flake.lock` -- a superset of plain `nix build .#default`. `.github/workflows/nix-build.yml` dropped its own `pull_request` trigger as redundant (F7c, spec-f7.md §2.4) and now only runs `nix build .#default` on push to `main` and on `workflow_dispatch`, so dependabot bumps that invalidate `vendorHash` still fail loudly post-merge instead of silently breaking Nix users on main. For dependabot Go-module bumps specifically, `.github/workflows/update-vendor-hash.yml` runs the same `update-nix-vendorhash.sh` script and pushes the hash bump back to the dependabot branch automatically (note: GitHub does not retrigger `pull_request` workflows for `GITHUB_TOKEN`-authored commits, so a maintainer may need to re-run PR Risk's `test-nix` once after the auto-fix push to mark the gate green).

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

Follow [RELEASING.md](RELEASING.md); it is the canonical release process.

The pre-push version gate requires Go and validates each `v*` release tag
against the checkout's canonical version. A batch containing different release
versions is refused; push only the tag matching this checkout.

`bd preflight` finds the nearest Beads source module by walking ancestor
directories, checking both source markers and the module identity. Its version
check runs directly in Go; `scripts/check-versions.sh` remains a Bash entrypoint.

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
