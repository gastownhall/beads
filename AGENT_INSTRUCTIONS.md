# Detailed Agent Instructions for Beads Development

**For project overview and quick start, see [AGENTS.md](AGENTS.md)**

This document contains detailed operational instructions for AI agents working on beads development, testing, and releases.

## Development Guidelines

### Code Standards

- **Go version**: see `go.mod` for the required version (currently 1.26+)
- **Linting**: `golangci-lint run ./...` (baseline warnings documented in [engdocs/LINTING.md](engdocs/LINTING.md))
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

**Tmpfs hosts:** the `cmd/bd` test suite creates an isolated `$HOME` and several
test binaries under `$TMPDIR`. They are normally cleaned by the test process,
but a SIGKILLed or OOMed run can leave orphans behind. On hosts where `/tmp`
is tmpfs (e.g. Fedora Atomic / Bluefin), run `make clean-test-tmp` between
test runs if `du -sh /tmp/beads-* /tmp/bd-*` shows accumulation. See bd-3q2u.

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

For agent-prepared commits, also include the
`Agent-Signature:` trailer described in
[engdocs/AGENT_SIGNING.md](engdocs/AGENT_SIGNING.md). Use `unknown-model` or
`unknown-reasoning` when reliable runtime metadata is unavailable.

### Git Workflow

bd uses **Dolt** as its primary database. Changes are committed to Dolt history automatically (one Dolt commit per write command).

**Install git hooks** for commit integration and legacy fallback behavior:
```bash
bd hooks install
```

### Git Integration

**Dolt sync**: Dolt handles sync natively via `bd dolt push` / `bd dolt pull`. No export/import round-trip needed for normal sync.

**Protected branches**: Dolt stores data under `refs/dolt/data`, separate from standard Git refs. See [docs/reference/protected-branches.md](docs/reference/protected-branches.md).

**Git worktrees**: Work directly with Dolt — no special flags needed. See [docs/reference/advanced.md](docs/reference/advanced.md).

**Merge conflicts**: Rare with hash IDs. Dolt uses cell-level 3-way merge for conflict resolution.

## Git Workflow: PR by Default

Crew workers use a PR-based workflow. Beads is a dependency of a downstream consumer, so we
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

Sign agent-written GitHub comments and reviews using
[engdocs/AGENT_SIGNING.md](engdocs/AGENT_SIGNING.md).

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

See [AGENTS.md](AGENTS.md#landing-the-plane-session-completion) for the
canonical session-completion protocol (quality gates, mandatory `git push`,
cleanup, and next-session hand-off).

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

**GitHub body hygiene.** For GitHub PR, issue, comment, and review bodies,
write Markdown to a file and pass it with `gh ... --body-file`. Run
`scripts/gh-body-lint <body-file>` first to catch literal `\n` sequences and
non-linking `GH#123` references.

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
- **post-merge** — Runs chained hooks and a legacy JSONL import fallback only when no Dolt remote is configured

**Note:** Hooks are embedded in the bd binary and work for all bd users (not just source repo users).

## Common Development Tasks

### Visual Design System

When adding CLI output features, follow these design principles for consistent,
cognitively friendly visuals.

#### No Emoji-Style Icons

Do not use large colored emoji icons like red/orange/yellow/blue/white circles
for priorities or status. They cause cognitive overload and break visual
consistency.

Use small Unicode symbols with semantic colors applied via lipgloss:

- Status: `○ ◐ ● ✓ ❄`
- Priority: `●` (filled circle with color)

#### Status Icons

Use these symbols consistently across all commands:

```text
○ open        - Available to work (white/default)
◐ in_progress - Currently being worked (yellow)
● blocked     - Waiting on dependencies (red)
✓ closed      - Completed (muted gray)
❄ deferred    - Scheduled for later (blue/muted)
```

#### Priority Icons and Colors

Format priority as `● P0` (filled circle icon plus label, colored by priority):

- `● P0`: Red + bold (critical)
- `● P1`: Orange (high)
- `● P2-P4`: Default text (normal)

#### Issue Type Colors

- `bug`: Red (problems need attention)
- `epic`: Purple (larger scope)
- Others: Default text

#### Design Principles

1. Small Unicode symbols only; avoid emoji blobs.
2. Semantic colors only for actionable items; do not color everything.
3. Closed items fade using muted gray.
4. Prefer icons over text labels for scanability.
5. Keep icons consistent across list, graph, show, and related commands.
6. Use tree connectors (`├──`, `└──`, `│`) for hierarchies.
7. Reduce cognitive noise; do not show `needs:1` when it is just the parent epic.

#### Semantic Styles

Use exported styles from `internal/ui/styles.go`:

```go
// Status styles
ui.StatusInProgressStyle  // Yellow - active work
ui.StatusBlockedStyle     // Red - needs attention
ui.StatusClosedStyle      // Muted gray - done

// Priority styles
ui.PriorityP0Style        // Red + bold
ui.PriorityP1Style        // Orange

// Type styles
ui.TypeBugStyle           // Red
ui.TypeEpicStyle          // Purple

// General styles
ui.PassStyle, ui.WarnStyle, ui.FailStyle
ui.MutedStyle, ui.AccentStyle
ui.RenderMuted(text), ui.RenderAccent(text)
```

Example:

```go
switch issue.Status {
case types.StatusOpen:
    icon = "○"
case types.StatusInProgress:
    icon = ui.StatusInProgressStyle.Render("◐")
case types.StatusBlocked:
    icon = ui.StatusBlockedStyle.Render("●")
case types.StatusClosed:
    icon = ui.StatusClosedStyle.Render("✓")
}
```

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

See [RELEASING.md](RELEASING.md) for the complete release process, including
the automated `./scripts/release.sh <version>` script and the manual,
step-by-step channel-by-channel instructions.

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

## Telemetry

`bd` collects anonymous command-usage metrics. Each event is a `cli_command`
record carrying only the command name; each batch also carries the bd version
and OS platform, keyed by a machine-derived, HMAC-protected distinct ID. No
email, repo path, remote URL, issue content, or user-supplied strings are
collected. Events are written under `~/.beads/eventsData` and POSTed to
`https://gastownhall-eventsapi.com/mp/collect`.

Metrics are enabled by default (opt-out). The friendliest way to see or change
them is `bd metrics` (`bd metrics on` / `bd metrics off` / `bd metrics example`),
which takes effect on the next command with no restart. `BD_DISABLE_METRICS=1`
still works as a one-off, shell-scoped override. The cross-tool
[`DO_NOT_TRACK`](https://donottrack.sh/) standard is honored as a disable-only
opt-out: `DO_NOT_TRACK=1` opts out, while a falsey or empty value
(`DO_NOT_TRACK=0`, `false`, or unset-but-present) falls through to your saved
`bd metrics` preference instead of forcing metrics back on. `BD_DISABLE_METRICS`
is the bidirectional override and takes precedence when both are set.

## Questions?

- Check existing issues: `bd list`
- Look at recent commits: `git log --oneline -20`
- Read the docs: README.md, ADVANCED.md, docs/reference/configuration.md
- Create an issue if unsure: `bd create "Question: ..." -t task -p 2`

## Important Files

- **README.md** - Main documentation (keep this updated!)
- **ADVANCED.md** - Advanced features (rename, merge, compaction)
- **CONTRIBUTING.md** - Contribution guidelines
- **SECURITY.md** - Security policy
