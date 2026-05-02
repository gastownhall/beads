# Detailed Agent Instructions for Beads Development

**For project overview and quick start, see [AGENTS.md](AGENTS.md)**

This document contains detailed operational instructions for AI agents working on beads development, testing, and releases.

## Development Guidelines

### Code Standards

- **Go version**: see `go.mod` for the required version (currently 1.26+)
- **Linting**: `golangci-lint run ./...` (baseline warnings documented in [docs/LINTING.md](docs/LINTING.md))
- **Testing**: All new features need tests (`make test` for the normal local/CI path, `make test-icu-path` only when intentionally exercising the opt-in ICU regex path)
- **Documentation**: Update relevant .md files

### File Organization

```
beads/
├── cmd/bd/              # CLI commands
├── internal/
│   ├── types/           # Core data types
│   └── storage/         # Storage layer
│       └── dolt/        # Dolt implementation
├── examples/            # Integration examples
└── *.md                 # Documentation
```

### Testing Workflow

**IMPORTANT:** Never pollute the production database with test issues!

**For manual testing**, use the `BEADS_DB` environment variable to point to a temporary database:

```bash
# Create test issues in isolated database
BEADS_DB=/tmp/test.db bd init --quiet --prefix test
BEADS_DB=/tmp/test.db bd create "Test issue" -p 1

# Or for quick testing
BEADS_DB=/tmp/test.db bd create "Test feature" -p 1
```

**For automated tests**, use `t.TempDir()` in Go tests:

```go
func TestMyFeature(t *testing.T) {
    tmpDir := t.TempDir()
    testDB := filepath.Join(tmpDir, ".beads", "beads.db")
    s := newTestStore(t, testDB)
    // ... test code
}
```

**Git test isolation:** For tests that create temporary git repos, force repo-local hooks:

```bash
git config core.hooksPath .git/hooks
```

Do not rely on the developer's global git config. Global `core.hooksPath` can leak
into temp repos and produce flaky test behavior.

**Warning:** bd will warn you when creating issues with "Test" prefix in the production database. Always use `BEADS_DB` for manual testing.

### Before Committing

1. **Run tests**: `make test` (or `./scripts/test.sh`)
   - Only if intentionally exercising the ICU regex path: `make test-icu-path`
2. **Run linter**: `golangci-lint run ./...` (ignore baseline warnings)
3. **Update docs**: If you changed behavior, update README.md or other docs
4. **Commit**: With git hooks installed (`bd hooks install`), Dolt changes are auto-committed

### Commit Message Convention

When committing work for an issue, include the issue ID in parentheses at the end:

```bash
git commit -m "Fix auth validation bug (bd-abc)"
git commit -m "Add retry logic for database locks (bd-xyz)"
```

This enables `bd doctor` to detect **orphaned issues** - work that was committed but the issue wasn't closed. The doctor check cross-references open issues against git history to find these orphans.

### Git Workflow

bd uses **Dolt** as its primary database. Changes are committed to Dolt history automatically (one Dolt commit per write command).

**Install git hooks** for automatic sync:
```bash
bd hooks install
```

### Git Integration

**Dolt sync**: Dolt handles sync natively via `bd dolt push` / `bd dolt pull`. No JSONL export/import needed.

**Protected branches**: Dolt stores data under `refs/dolt/data`, separate from standard Git refs. See [docs/PROTECTED_BRANCHES.md](docs/PROTECTED_BRANCHES.md).

**Git worktrees**: Work directly with Dolt — no special flags needed. See [docs/ADVANCED.md](docs/ADVANCED.md).

**Merge conflicts**: Rare with hash IDs. Dolt uses cell-level 3-way merge for conflict resolution.

## Git Workflow: PR by Default

Crew workers use a PR-based workflow. Beads is a dependency of Gas City, so we
defer to the standard PR flow to keep changes reviewable.

- Work on a feature branch, push the branch, open a PR against `main`
- `gh pr create` is the normal path to land work
- Direct push to main is reserved for releases (tag + release commit) and
  narrow operational fixes; prefer a PR when unsure
- When handling external contributor PRs, use fix-merge: checkout the PR
  branch locally, fix/rebase onto main, merge via PR, then close the PR

### Maintainer PR Guidelines

Before triaging, reviewing, landing, closing, or otherwise maintaining PRs,
read [PR_MAINTAINER_GUIDELINES.md](PR_MAINTAINER_GUIDELINES.md). The
maintainer policy is to maximize community throughput: find useful contributor
value, absorb or transform it locally when practical, preserve attribution, and
use request-changes only as a last resort.

### External Contributor PRs: Check Before You Build

**Read [CONTRIBUTING.md](CONTRIBUTING.md)** — it contains promises we've made to contributors. Violating them damages trust and community.

Run the read-only preflight before implementing related work, opening a PR, or
merging/closing a PR:

```bash
scripts/pr-preflight.sh --search "<topic keywords>" --repo gastownhall/beads
scripts/pr-preflight.sh <pr-number> --repo gastownhall/beads
```

**Before implementing any feature or fix, check for existing open PRs on the same topic:**

```bash
gh pr list --repo gastownhall/beads --state open --search "<topic keywords>" --json number,title,author,headRefName
```

**Contributor work gets priority.** If an external PR already exists:
1. **Review it first** — read the diff, understand the approach
2. **Build on their work, don't rewrite it** — checkout their branch, fix/adapt as needed
3. **Preserve their tests** — contributor tests are signal; keep them unless they're wrong
4. **Attribute properly** — use `Co-authored-by:` in commits, reference their PR number
5. **Never auto-close a contributor PR** by merging a rewrite — that discards their contribution silently

If you must rewrite (e.g., fundamentally different approach needed), explain why on the original PR and credit the contributor's design/tests in your commits.

Do not rely on auto-discovery of CONTRIBUTING.md; the preflight is the agent
gate for PR handling.

## Landing the Plane

**When the user says "let's land the plane"**, you MUST complete ALL steps below. The plane is NOT landed until `git push` succeeds. NEVER stop before pushing. NEVER say "ready to push when you are!" - that is a FAILURE.

**MANDATORY WORKFLOW - COMPLETE ALL STEPS:**

1. **File beads issues for any remaining work** that needs follow-up
2. **Ensure all quality gates pass** (only if code changes were made):
   - Run `golangci-lint run ./...` (if pre-commit installed: `pre-commit run --all-files`)
   - Run `make test` (and `make test-icu-path` only if you intentionally need the ICU regex path)
   - File P0 issues if quality gates are broken
3. **Update beads issues** - close finished work, update status
4. **PUSH TO REMOTE - NON-NEGOTIABLE** - This step is MANDATORY. Execute ALL commands below:
   ```bash
   # Pull first to catch any remote changes
   git pull --rebase

   # MANDATORY: Push everything to remote
   # DO NOT STOP BEFORE THIS COMMAND COMPLETES
   git push

   # MANDATORY: Verify push succeeded
   git status  # MUST show "up to date with origin/main"
   ```

   **CRITICAL RULES:**
   - The plane has NOT landed until `git push` completes successfully
   - NEVER stop before `git push` - that leaves work stranded locally
   - NEVER say "ready to push when you are!" - YOU must push, not the user
   - If `git push` fails, resolve the issue and retry until it succeeds
   - The user is managing multiple agents - unpushed work breaks their coordination workflow

5. **Clean up git state** - Clear old stashes and prune dead remote branches:
   ```bash
   git stash clear                    # Remove old stashes
   git remote prune origin            # Clean up deleted remote branches
   ```
6. **Verify clean state** - Ensure all changes are committed AND PUSHED, no untracked files remain
7. **Choose a follow-up issue for next session**
   - Provide a prompt for the user to give to you in the next session
   - Format: "Continue work on bd-X: [issue title]. [Brief context about what's been done and what's next]"

**REMEMBER: Landing the plane means EVERYTHING is pushed to remote. No exceptions. No "ready when you are". PUSH IT.**

**Example "land the plane" session:**

```bash
# 1. File remaining work
bd create "Add integration tests for sync" -t task -p 2 --json

# 2. Run quality gates (only if code changes were made)
make test
golangci-lint run ./...

# 3. Close finished issues
bd close bd-42 bd-43 --reason "Completed" --json

# 4. PUSH TO REMOTE - MANDATORY, NO STOPPING BEFORE THIS IS DONE
git pull --rebase
git push       # MANDATORY - THE PLANE IS STILL IN THE AIR UNTIL THIS SUCCEEDS
git status     # MUST verify "up to date with origin/main"

# 5. Clean up git state
git stash clear
git remote prune origin

# 6. Verify everything is clean and pushed
git status

# 7. Choose next work
bd ready --json
bd show bd-44 --json
```

**Then provide the user with:**

- Summary of what was completed this session
- What issues were filed for follow-up
- Status of quality gates (all passing / issues filed)
- Confirmation that ALL changes have been pushed to remote
- Recommended prompt for next session

**CRITICAL: Never end a "land the plane" session without successfully pushing. The user is coordinating multiple agents and unpushed work causes severe rebase conflicts.**

## Agent Session Workflow

**WARNING: DO NOT use `bd edit`** - it opens an interactive editor ($EDITOR) which AI agents cannot use. Use `bd update` with flags instead:
```bash
bd update <id> --description "new description"
bd update <id> --title "new title"
bd update <id> --design "design notes"
bd update <id> --notes "additional notes"
bd update <id> --acceptance "acceptance criteria"
```

**Read execution metadata before prose.** When enacting a bd issue, inspect the
structured metadata before using description or notes to choose execution mode,
delegation, model, reasoning level, or parallel group:

```bash
bd show <id> --json | jq '.[0] | {id,title,metadata,description,notes}'
```

The execution metadata keys are:

- `execution_agent_type`
- `execution_suggested_model`
- `execution_reasoning_effort`
- `execution_mode`
- `execution_parallel_group`

When these keys are present, treat them as the authoritative execution hints.
Use `description` for the work scope and `notes` for rationale or fallback
context. Parent/orchestrator agents must read these fields before spawning
subagents because a running subagent cannot change its model or reasoning effort
after launch.

**Use stdin for descriptions with special characters** (backticks, `!`, nested quotes):
```bash
# Pipe via stdin to avoid shell escaping issues
echo 'Description with `backticks` and "quotes"' | bd create "Title" --stdin
echo 'Updated description with $variables' | bd update <id> --description=-

# Or use --body-file for longer content
bd create "Title" --body-file=description.md
```

**Example agent session:**

```bash
# Make changes (each write auto-commits to Dolt)
bd create "Fix bug" -p 1
bd create "Add tests" -p 1
bd update bd-42 --claim
bd close bd-40 --reason "Completed"

# Push Dolt data to remote if configured
bd dolt push

# Now safe to end session
```

This installs:

- **pre-commit** — Commits pending Dolt changes
- **post-merge** — Pulls remote Dolt changes after git merge

**Note:** Hooks are embedded in the bd binary and work for all bd users (not just source repo users).

## Common Development Tasks

### CLI Design Principles

**Minimize cognitive overload.** Every new command, flag, or option adds cognitive burden for users. Before adding anything:

1. **Recovery/fix operations → `bd doctor --fix`**: Don't create separate commands like `bd recover` or `bd repair`. Doctor already detects problems - let `--fix` handle remediation. This keeps all health-related operations in one discoverable place.
   For git hook marker migration specifically: use `bd migrate hooks --dry-run` to preview operations, and `bd doctor --fix` for the standard apply path.

2. **Prefer flags on existing commands**: Before creating a new command, ask: "Can this be a flag on an existing command?" Example: `bd list --stale` instead of `bd stale`.

3. **Consolidate related operations**: Related operations should live together. Version control uses `bd vc {log,diff,commit}`, not separate top-level commands.

4. **Count the commands**: Run `bd --help` and count. If we're approaching 30+ commands, we have a discoverability problem. Consider subcommand grouping.

5. **New commands need strong justification**: A new command should represent a fundamentally different operation, not just a convenience wrapper.

### Adding a New Command

1. Create file in `cmd/bd/`
2. Add to root command in `cmd/bd/main.go`
3. Implement with Cobra framework
4. Add `--json` flag for agent use
5. Add tests in `cmd/bd/*_test.go`
6. Document in README.md

### Adding Storage Features

1. Add Dolt SQL schema changes in `internal/storage/dolt/`
2. Add migration if needed
3. Update `internal/types/types.go` if new types
4. Implement in `internal/storage/dolt/` (queries, issues, etc.)
5. Add tests
6. Update export/import in `cmd/bd/export.go` and `cmd/bd/import.go`

### Adding Examples

1. Create directory in `examples/`
2. Add README.md explaining the example
3. Include working code
4. Link from `examples/README.md`
5. Mention in main README.md

## Building and Testing

```bash
# Build and install bd to ~/.local/bin (the canonical location)
make install

# Test (local baseline)
make test

# Optional ICU regex path smoke (maintainer-only, not normal validation)
make test-icu-path

# Coverage run
go test -tags gms_pure_go -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Verify installed binary
bd init --prefix test
bd create "Test issue" -p 1
bd ready
```

> **WARNING**: Do NOT use `go build -o bd ./cmd/bd`, `go install ./cmd/bd`,
> or raw `go run ./cmd/bd ...`.
> These bypass the canonical build path, can create stale binaries in the
> working directory or `~/go/bin/`, and raw `go run` may miss the required
> `gms_pure_go` build tag. Always use `make install`, `./bd`, or
> `go run -tags gms_pure_go ./cmd/bd ...` when you explicitly need `go run`.

## Version Management

**IMPORTANT**: When the user asks to "bump the version" or mentions a new version number (e.g., "bump to 0.9.3"), use the version bump script:

```bash
# Preview changes (shows diff, doesn't commit)
./scripts/bump-version.sh 0.9.3

# Auto-commit the version bump
./scripts/bump-version.sh 0.9.3 --commit
git push origin main
```

**What it does:**

- Updates ALL version files (CLI, plugin, MCP server, docs) in one command
- Validates semantic versioning format
- Shows diff preview
- Verifies all versions match after update
- Creates standardized commit message

**User will typically say:**

- "Bump to 0.9.3"
- "Update version to 1.0.0"
- "Rev the project to 0.9.4"
- "Increment the version"

**You should:**

1. Run `./scripts/bump-version.sh <version> --commit`
2. Push to GitHub
3. Confirm all versions updated correctly

**Files updated automatically:**

- `cmd/bd/version.go` - CLI version
- `plugins/beads/.claude-plugin/plugin.json` - Claude plugin version
- `plugins/beads/.codex-plugin/plugin.json` - Codex plugin version
- `.claude-plugin/marketplace.json` - Claude marketplace version
- `integrations/beads-mcp/pyproject.toml` - MCP server version
- `README.md` - Documentation version
- `PLUGIN.md` - Version requirements

**Why this matters:** We had version mismatches (bd-66) when only `version.go` was updated. This script prevents that by updating all components atomically.

See `scripts/README.md` for more details.

## Release Process (Maintainers)

**Automated (Recommended):**

```bash
# One command to do everything (version bump, tests, tag, Homebrew update, local install)
./scripts/release.sh 0.9.3
```

This handles the entire release workflow automatically, including waiting ~5 minutes for GitHub Actions to build release artifacts. See [scripts/README.md](scripts/README.md) for details.

**Manual (Step-by-Step):**

1. Bump version: `./scripts/bump-version.sh <version> --commit`
2. Update CHANGELOG.md with release notes
3. Run tests: `make test` (and `make test-icu-path` only if you intentionally need the ICU regex path)
4. Push version bump: `git push origin main`
5. Tag release: `git tag v<version> && git push origin v<version>`
6. Update Homebrew: `./scripts/update-homebrew.sh <version>` (waits for GitHub Actions)
7. Verify: `brew update && brew upgrade beads && bd version`

See [docs/RELEASING.md](docs/RELEASING.md) for complete manual instructions.

## Checking GitHub Issues and PRs

**IMPORTANT**: When asked to check GitHub issues or PRs, use command-line tools like `gh` instead of browser/playwright tools.

**Preferred approach:**

```bash
# List open issues with details
gh issue list --limit 30

# List open PRs
gh pr list --limit 30

# View specific issue
gh issue view 201
```

**Then provide an in-conversation summary** highlighting:

- Urgent/critical issues (regressions, bugs, broken builds)
- Common themes or patterns
- Feature requests with high engagement
- Items that need immediate attention

**Why this matters:**

- Browser tools consume more tokens and are slower
- CLI summaries are easier to scan and discuss
- Keeps the conversation focused and efficient
- Better for quick triage and prioritization

**Do NOT use:** `browser_navigate`, `browser_snapshot`, or other playwright tools for GitHub PR/issue reviews unless specifically requested by the user.

## Questions?

- Check existing issues: `bd list`
- Look at recent commits: `git log --oneline -20`
- Read the docs: README.md, ADVANCED.md, docs/CONFIG.md
- Create an issue if unsure: `bd create "Question: ..." -t task -p 2`

## Important Files

- **README.md** - Main documentation (keep this updated!)
- **ADVANCED.md** - Advanced features (rename, merge, compaction)
- **CONTRIBUTING.md** - Contribution guidelines
- **SECURITY.md** - Security policy
