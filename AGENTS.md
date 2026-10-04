<!-- AIHUB-INVIOLABLE-LAW-PRELUDE v1 -->
# AIHUB Inviolable Law — Strict Prelude

1. Truth: never claim done/green/resolved without command, exit code, decisive output.
2. Root cause: exterminate bypass, fallback, shim, suppression, stub, hardcode, catch-based normalization, retry, compatibility, partial execution, keyring, or old+new coexistence.
3. Tracker boundary: without repository `.beads/`, invoke neither `bd` nor `gc` and create no substitute. Otherwise use only the selected, available canonical tracker. If explicitly suspended, preserve authorized Git/PR/CI evidence and never declare DONE.
4. Research first: inspect code, docs, canonical sources before acting; never invent APIs, flags, facts, or behavior.
5. Owner first: use the project's declared facades/primitives; do not reimplement them locally.
6. Gate persistence: a failure stops only that invocation. Correct its owner,
   republish, and rerun until green; never switch phase or repository because a
   check, review, approval, or merge is pending. Escalate only after every
   authorized technical action is exhausted and the remaining condition is
   genuinely external or requires new authority.
7. Landing: native gates, commit, fast-forward push, bead evidence.
8. Divergence: FF push rejected → integrate by cooperation: `git merge --no-ff` the integration base into your lane, resolve conflicts, revalidate, land. Never rebase or force-push an authorized change or integration branch; adopt all current worktree state and fix it forward.
9. Escalation: impossible rule → exact error. Rule conflict → present both with numbers. Unclear → one targeted question. Never guess.
10. Precedence: NEWEST > OLDEST. USER REQUEST > BEADS > ADRs > SKILLs > DOCS > default. Adjust lower/older to higher/newer. Doubt → ASK USER FIRST.
11. Workspaces: follow `rules/coordination/gascity.md`. Every manual task uses a dedicated Git worktree, branch, and physical `.venv`, never the primary checkout. Gas City suspension keeps orchestration inactive. Worktrees and staging stay on the destination filesystem, never `/tmp`; no borrowed environment, backup, or archive. Retire worktrees after verified integration.
12. Phase closure: keep the phase active through check repair, review resolution,
    independent approval, merge into the configured integration branch, and
    post-merge proof. Only then, with its Bead closed with evidence, is it DONE.
    When the operator states that no independent reviewer exists and authorizes
    an administrative merge, that authorization replaces the approval row alone;
    every other row stays mandatory and closure records the approval as
    operator-authorized, never as satisfied.
13. Root Make only: diagnostics, validation, generation, tests, Waza,
    publication, and deployment run only through selector-free verbs in the
    repository root Makefile; bare verbs perform their declared operation. Test
    verbs: `rules/workflow/canonical-commands.md`.
14. Red means red: a warning, skip, empty output, missing tool, missing report,
    zero collection, caught exception, retry, or normalized failure is RED. The
    only acceptable zero-execution test result is a typed `make test` cache hit
    with an integrity-checked database and complete deselection
    accounting; it is never reported as tests passed. The first exception and
    raw traceback escape unchanged.
<!-- /AIHUB-INVIOLABLE-LAW-PRELUDE -->

# AGENTS.md — beads

> Packaged governance `agents-governance` `0.6.3` owns the capability indexes: 71 agents, 111 rules, 141 skills. Consume them through `GovernanceBundle`; do not copy their bodies here.

<!-- AIHUB-AGENTS-SCOPE-LOCAL-BEGIN -->
# Agent Instructions

<!-- bd-doctor-divergence: ok -->

See [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md) for full instructions.

This file exists for compatibility with tools that look for AGENTS.md.

The marker above tells `bd doctor` that the intentional divergence between
this file and `CLAUDE.md` (different audiences, different reading orders) is
expected and should not be flagged.

## Key Sections

- **Issue Tracking** - How to use bd for work management
- **Development Guidelines** - Code standards and testing
- **Project Scope** - Read [docs/PROJECT_CHARTER.md](docs/PROJECT_CHARTER.md) before adding new feature surface area
- **Visual Design System** - Status icons, colors, and semantic styling for CLI output
- **Contributor Protection** - Read [CONTRIBUTING.md](CONTRIBUTING.md) before handling external PRs
- **Maintainer PR Guidelines** - Read [PR_MAINTAINER_GUIDELINES.md](PR_MAINTAINER_GUIDELINES.md) before triaging, landing, or closing PRs

## Project Scope

Before adding new feature surface area, read
[docs/PROJECT_CHARTER.md](docs/PROJECT_CHARTER.md). Beads owns issue tracking
primitives and should not encode orchestration-layer policy, become a storage
engine, or casually expand the database schema when metadata would work.

## PR Safety for Agents

Before triaging, reviewing, landing, closing, or otherwise maintaining PRs, read
[PR_MAINTAINER_GUIDELINES.md](PR_MAINTAINER_GUIDELINES.md). The maintainer
policy is to maximize community throughput: find useful contributor value,
absorb or transform it locally when practical, preserve attribution, and use
request-changes only as a last resort.

Before implementing work, opening a PR, or merging/closing a PR, run the PR
preflight:
```bash
scripts/pr-preflight.sh --search "<topic keywords>" --repo gastownhall/beads
scripts/pr-preflight.sh <pr-number> --repo gastownhall/beads
```

External contributor PRs have priority. Review and build on their branch when
possible, preserve their tests and attribution, and never close or supersede
their PR silently. If a rewrite is unavoidable, explain why on the original PR
and credit their design/tests.

## Visual Design Anti-Patterns

**NEVER use emoji-style icons** (🔴🟠🟡🔵⚪) in CLI output. They cause cognitive overload.

**ALWAYS use small Unicode symbols** with semantic colors:
- Status: `○ ◐ ● ✓ ❄`
- Priority: `● P0` (filled circle with color)

See [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md) for full development guidelines.

## Storage Boundary

The canonical storage boundary is in
[docs/PROJECT_CHARTER.md](docs/PROJECT_CHARTER.md#storage-boundary). In short:
Beads talks to storage through a driver interface (`dolthub/driver` for Dolt).
Do not add beads-side flocks, engine introspection, storage-specific retry or
crash-recovery logic, or public SDK return types that leak driver internals.
If the boundary is too narrow, widen the interface or route the issue to the
driver instead of patching around it in beads.

## Agent Warning: Interactive Commands

**DO NOT use `bd edit`** - it opens an interactive editor ($EDITOR) which AI agents cannot use.

Use `bd update` with flags instead:
```bash
bd update <id> --description "new description"
bd update <id> --title "new title"
bd update <id> --design "design notes"
bd update <id> --notes "additional notes"
bd update <id> --acceptance "acceptance criteria"

# Use stdin for descriptions with special characters (backticks, !, nested quotes)
echo 'Description with `backticks` and "quotes"' | bd create "Title" --description=-
echo 'Updated text' | bd update <id> --description=-
```

## Testing Commands (No Ambiguity)

- Default local test command: `make test` (or `./scripts/test.sh`).
- Opt-in ICU regex path: `make test-icu-path` (or `./scripts/test-icu-path.sh ./...`).
- This ICU path is maintainer-only and not part of normal validation; `make test-full-cgo` and `./scripts/test-cgo.sh` are deprecated aliases.
- For package- or test-scoped shipped-config CGO runs, prefer:
```bash
CGO_ENABLED=1 go test -tags gms_pure_go ./cmd/bd/...
CGO_ENABLED=1 go test -tags gms_pure_go -run '^TestName$' ./cmd/bd/...
```

## Non-Interactive Shell Commands

**ALWAYS use non-interactive flags** with file operations to avoid hanging on confirmation prompts.

Shell commands like `cp`, `mv`, and `rm` may be aliased to include `-i` (interactive) mode on some systems, causing the agent to hang indefinitely waiting for y/n input.

**Use these forms instead:**
```bash
# Force overwrite without prompting
cp -f source dest           # NOT: cp source dest
mv -f source dest           # NOT: mv source dest
rm -f file                  # NOT: rm file

# For recursive operations
rm -rf directory            # NOT: rm -r directory
cp -rf source dest          # NOT: cp -r source dest
```

**Other commands that may prompt:**
- `scp` - use `-o BatchMode=yes` for non-interactive
- `ssh` - use `-o BatchMode=yes` to fail instead of prompting
- `apt-get` - use `-y` flag
- `brew` - use `HOMEBREW_NO_AUTO_UPDATE=1` env var

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

<!-- migrated from .github/copilot-instructions.md -->
# GitHub Copilot Instructions for Beads

## Project Overview

**beads** (command: `bd`) is a Dolt-powered issue tracker designed for AI-supervised coding workflows. Git integration is optional. We dogfood our own tool for all task tracking.

**Key Features:**
- Dependency-aware issue tracking
- Auto-sync via Dolt-native replication
- AI-optimized CLI with JSON output
- Dolt server mode for background operations
- MCP server integration for Claude and other AI assistants

## Tech Stack

- **Language**: Go 1.21+
- **Storage**: Dolt (version-controlled SQL database)
- **CLI Framework**: Cobra
- **Testing**: Go standard testing + table-driven tests
- **CI/CD**: GitHub Actions
- **MCP Server**: Python (integrations/beads-mcp/)

## Coding Guidelines

### Testing
- Always write tests for new features
- Use `t.TempDir() in Go tests` to avoid polluting production database
- Run `go test -short ./...` before committing
- Never create test issues in production DB (use temporary DB)

### Code Style
- Run `golangci-lint run ./...` before committing
- Follow existing patterns in `cmd/bd/` for new commands
- Add `--json` flag to all commands for programmatic use
- Update docs when changing behavior

### Git Workflow
- Install git hooks: `bd hooks install`
- Use `bd dolt push` / `bd dolt pull` for remote sync
- Before implementing related work, opening a PR, or merging/closing a PR, run:
  `scripts/pr-preflight.sh --search "<topic>" --repo gastownhall/beads` or
  `scripts/pr-preflight.sh <pr-number> --repo gastownhall/beads`
- External contributor PRs have priority: build on them when possible, preserve
  tests and attribution, and never close or replace them silently.

## Issue Tracking with bd

**CRITICAL**: This project uses **bd** for ALL task tracking. Do NOT create markdown TODO lists.

### Essential Commands

```bash
# Find work
bd ready --json                    # Unblocked issues
bd stale --days 30 --json          # Forgotten issues

# Create and manage (ALWAYS include --description)
bd create "Title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd update <id> --claim --json
bd close <id> --reason "Done" --json

# Search
bd list --status open --priority 1 --json
bd show <id> --json

# Sync (if remote configured)
bd dolt push                   # Push to Dolt remote
bd dolt pull                   # Pull from Dolt remote
```

### Workflow

1. **Check ready work**: `bd ready --json`
2. **Claim task**: `bd update <id> --claim`
3. **Work on it**: Implement, test, document
4. **Discover new work?** `bd create "Found bug" --description="What was found and why" -p 1 --deps discovered-from:<parent-id> --json`
5. **Complete**: `bd close <id> --reason "Done" --json`
6. **Sync**: `bd dolt push` (push to Dolt remote if configured)

**IMPORTANT**: Always include `--description` when creating issues. Issues without descriptions lack context for future work.

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

## Project Structure

```
beads/
├── cmd/bd/              # CLI commands (add new commands here)
├── internal/
│   ├── types/           # Core data types
│   └── storage/         # Storage layer
│       └── dolt/        # Dolt implementation
├── integrations/
│   └── beads-mcp/       # MCP server (Python)
├── examples/            # Integration examples
├── docs/                # Documentation
└── .beads/
    └── dolt/            # Dolt database (source of truth)
```

## Available Resources

### MCP Server (Recommended)
Use the beads MCP server for native function calls instead of shell commands:
- Install: `pip install beads-mcp`
- Functions: `mcp__beads__ready()`, `mcp__beads__create()`, etc.
- See `integrations/beads-mcp/README.md`

### Scripts
- `./scripts/bump-version.sh <version> --commit` - Update all version files atomically
- `./scripts/release.sh <version>` - Complete release workflow
- `./scripts/update-homebrew.sh <version>` - Update Homebrew formula

### Key Documentation
- **AGENTS.md** - Comprehensive AI agent guide (detailed workflows, advanced features)
- **AGENT_INSTRUCTIONS.md** - Development procedures, testing, releases
- **README.md** - User-facing documentation
- **docs/CLI_REFERENCE.md** - Complete command reference

## Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Use `bd dolt push` / `bd dolt pull` for remote sync
- ✅ Test with `t.TempDir() in Go tests`
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT create test issues in production DB
- ❌ Do NOT manually modify `.beads/dolt/`

---

**For detailed workflows and advanced features, see [AGENTS.md](../AGENTS.md)**

<!-- migrated from CLAUDE.md -->
# Claude Code Entry Point for Beads

This file is intentionally short. Do not copy workflow, build, storage, or UI
rules here; those details drift quickly when repeated across agent entrypoints.

## Read First

- **Workflow and safety**: [AGENTS.md](AGENTS.md)
- **Detailed agent operations**: [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md)
- **Architecture orientation**: [docs/CLAUDE.md](docs/CLAUDE.md)
- **PR maintenance policy**: [PR_MAINTAINER_GUIDELINES.md](PR_MAINTAINER_GUIDELINES.md)

## Current Ground Rules

- Run `bd prime` before doing tracked work.
- Follow `go.mod` and [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md) for build
  and test commands; do not hard-code toolchain versions here.
- Beads uses Dolt as the issue database. Use `bd dolt push` / `bd dolt pull`
  for issue data sync; do not use export/import as a routine git workflow.
- The CLI Visual Design System lives in
  [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md#visual-design-system).
- If this file conflicts with a linked source, trust the linked source and fix
  this file by removing the duplicate.
<!-- AIHUB-AGENTS-SCOPE-LOCAL-END -->
