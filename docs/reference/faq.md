---
title: FAQ
description: Common questions about beads and how to use it effectively
---

## General

### What is beads?

Beads (`bd`) is a lightweight, Dolt-backed issue tracker designed for AI coding agents. It provides dependency-aware task management with built-in sync across machines, so agents and humans can collaborate from the same task graph. See [Core Concepts](/core-concepts/index) for the full model.

### Why beads instead of GitHub Issues or Jira?

GitHub Issues plus the `gh` CLI can approximate some features, but hosted trackers fundamentally cannot replicate what AI agents need:

| Capability | beads | GitHub Issues |
|---|---|---|
| Typed dependencies | Core types (`blocks`, `related`, `parent-child`, `discovered-from`) plus workflow and knowledge-graph edges | Only "blocks/blocked by" links; no semantic enforcement, no `discovered-from` for agent work discovery |
| Ready-work detection | `bd ready` computes transitive blocking offline in milliseconds | No built-in "ready" concept; requires custom GraphQL plus a sync service |
| Offline-first task memory | Works offline; issues live in a local, version-controlled database; hash IDs prevent collisions on merge | Cloud-first; requires network and auth; no branch-scoped task state |
| Conflicts and duplicates | Automatic collision resolution; duplicate merge with dependency consolidation and reference rewriting | Manual close-as-duplicate; no safe bulk merge, no cross-reference updates |
| Local SQL database | Full SQL queries against a local Dolt database with native version control | No local database; data must be mirrored externally |
| Agent-native APIs | Consistent `--json` on all commands; dedicated MCP server with workspace detection | Mixed JSON/text output; no agent-focused MCP layer |

When to use each: GitHub Issues and Jira excel for human teams working in a web UI with cross-repo dashboards and integrations. Beads excels for AI agents that need offline, version-controlled task memory with graph semantics and deterministic queries. The two can also coexist — beads syncs bidirectionally with GitHub, Jira, and Linear (see [Migration](#migration)).

### How is beads different from Taskwarrior?

Taskwarrior is excellent for personal task management, but beads is built for AI agents:

- **Agent semantics**: the `discovered-from` dependency type, `bd ready` for queue management
- **JSON-first design**: every command has `--json` output
- **Built-in sync**: version-controlled storage with native push/pull, no separate sync server to run
- **Cell-level merge**: concurrent changes merge automatically at the field level
- **SQL database**: full SQL queries against the Dolt database

### Can I use beads without AI agents?

Absolutely. Beads is a good CLI issue tracker for humans too — `bd ready` is useful for anyone managing dependencies. Think of it as "Taskwarrior meets git."

### What does "beads" stand for?

Nothing specific — it's a metaphor for linked work items, like beads on a string.

### Is beads production-ready?

Beads is a 1.x product used in production for AI-assisted development. The core functionality — create, update, dependencies, ready work, Dolt-backed sync — is stable, and releases follow semantic versioning. Data stays portable: `bd export` produces human-readable JSONL, and `bd backup` pushes Dolt-native backups. As with any tracker holding work you care about, keep normal backup hygiene (a Dolt remote or a `bd backup` destination).

## Architecture

### Why Dolt instead of plain SQLite or flat files?

Dolt is a version-controlled SQL database — git semantics at the database level:

- **Version-controlled SQL**: full SQL queries with branch, diff, and merge built in
- **Cell-level merge**: concurrent changes merge automatically at the field level
- **No separate sync format**: `bd dolt push` / `bd dolt pull` move history natively
- **Multi-writer**: server mode supports concurrent agents
- **Portable**: `bd export` produces JSONL for migration and interoperability

See [Dolt architecture](/architecture/dolt) for the detailed analysis.

### Why hash-based IDs instead of sequential?

Sequential IDs (`#1`, `#2`) collide the moment two agents or two branches create issues concurrently — both mint the same next number, and the merge produces two different issues with one ID. Hash IDs like `bd-a1b2` are globally unique without coordination:

```bash
# Branch A
bd create "Add OAuth"   # bd-a1b2

# Branch B
bd create "Add Stripe"  # bd-f14c — no collision

git merge feature-auth  # clean merge, distinct IDs
```

IDs start at 3 characters and grow automatically (up to 8) as the database grows, keeping the collision probability under a fixed threshold. See [Hash IDs](/core-concepts/hash-ids) and [Adaptive IDs](/core-concepts/adaptive-ids), or the [collision math](https://github.com/gastownhall/beads/blob/main/engdocs/COLLISION_MATH.md) if you want the numbers.

### What are hierarchical child IDs?

Hierarchical IDs (`bd-a3f8e9.1`, `bd-a3f8e9.2`) give epics and their subtasks human-readable structure:

```bash
bd create "Auth System" -t epic          # bd-a3f8e9
bd create "Login UI" --parent bd-a3f8e9  # bd-a3f8e9.1
bd create "Validation" --parent bd-a3f8e9 # bd-a3f8e9.2
```

The parent hash keeps the namespace unique across epics, the child numbers stay human-friendly, and up to 3 levels of nesting are supported. Use them for work breakdown structures; for cross-cutting relationships, use `bd dep add` instead. See [Hash IDs](/core-concepts/hash-ids).

### Embedded mode or server mode — which am I running?

The default `bd init` uses **embedded mode**: Dolt runs in-process inside `bd`, data lives at `.beads/embeddeddolt/`, and there is no server, port, or PID file to manage. This is the right mode for solo work, CI/CD, and single-agent setups.

**Server mode** (`bd init --server`) connects to a running `dolt sql-server` and stores data at `.beads/dolt/`. Switch to it when multiple processes need concurrent write access to the same database — for example several agents on one machine. See [Dolt architecture](/architecture/dolt) for setup and migration between modes.

## Usage

### Should I run bd init myself or have my agent do it?

Either works — use the right flag:

```bash
bd init          # Humans: interactive, prompts for git hooks
bd init --quiet  # Agents: non-interactive, auto-installs hooks
```

For an existing project, clone and run `bd init`; it creates the Dolt database and pulls from the configured remote. For a new project, run `bd init`, then commit the `.beads/` directory.

### How do I sync issues across machines?

```bash
bd dolt push    # Push changes to the Dolt remote
bd dolt pull    # Pull changes from the Dolt remote
```

`bd init` auto-configures your git origin as the Dolt remote when present; use `bd init --remote <url>` for an explicit remote. See [Sync Setup](/getting-started/sync-setup).

### Do I need to run export/import manually?

No. All writes go directly to the Dolt database and are committed to Dolt history automatically; `bd dolt push` / `bd dolt pull` handle sync. `bd export` exists for portability and interchange — `.beads/issues.jsonl` is a passive export, never the database. For backups, use `bd backup init <path>` / `bd backup sync` / `bd backup restore`. See [Sync Concepts](/core-concepts/sync-concepts) for the full model and the sync patterns to avoid.

### What if my database feels stale after a colleague pushes changes?

```bash
bd dolt pull    # Fetch and merge updates from the Dolt remote
bd ready        # Shows fresh data
```

For federation setups, `bd federation sync` syncs with all configured peers. See [Federation](/multi-agent/federation).

### How do I handle merge conflicts?

Dolt merges at the cell level, so most concurrent changes resolve automatically. Importing an issue with the same ID but different fields is an update, not a collision — hash IDs are stable, so same ID means same issue. If `bd dolt pull` does report conflicts:

```bash
bd doctor --fix
bd dolt push
```

See the [merge conflicts runbook](/recovery/merge-conflicts).

### Can I track issues for multiple projects?

Yes — each project is completely isolated:

```bash
cd ~/project1 && bd init --prefix proj1
cd ~/project2 && bd init --prefix proj2
```

Each project gets its own `.beads/` directory and database, and `bd` auto-discovers the right one by walking up from your current directory (like git). To link work across projects, hydrate the other repo into your database (`bd repo add`, then `bd repo sync`) and add normal dependencies, or depend on another project's capability with an `external:<project>:<capability>` target — see [cross-repo routing](/multi-agent/routing).

In server mode, each project runs its own Dolt server by default. On machines with many projects you can opt into a single shared server (`bd init --shared-server`, or `export BEADS_DOLT_SHARED_SERVER=1`) that serves every project from `~/.beads/shared-server/`. See [Dolt architecture](/architecture/dolt).

### Can multiple agents work on the same repo?

Yes — that's what beads was designed for. Hash IDs prevent collisions, and assignment tracks who's working on what:

```bash
bd ready --assignee agent-name       # Query ready work for an agent
bd update bd-a1b2 --claim            # Atomically claim an issue (assignee + in_progress)
bd create "Found issue" --deps discovered-from:bd-a1b2   # Track discovered work
```

In orchestrated workflows an orchestrator usually assigns work (`bd assign`); agents picking work directly should use the atomic `--claim`. For multiple concurrent processes on one machine, use server mode; for distributed setups, Dolt's cell-level merge and [federation](/multi-agent/federation) let agents work independently and merge like developers do. See [Agent Coordination](/multi-agent/coordination).

### Does beads work offline?

Yes — beads is offline-first. All queries run against the local Dolt database, no command needs the network, and sync happens via `bd dolt push` / `bd dolt pull` when you're online. That makes it suitable for planes, unstable connections, air-gapped environments, and privacy-sensitive projects.

### How do I use beads in CI/CD?

Just run commands — embedded mode is the default, so no server is required:

```bash
bd list --json
bd ready --json
```

## Workflows

### What are formulas?

Declarative workflow templates in TOML or JSON. `bd cook` compiles a formula into a proto (a template epic), and `bd mol pour` instantiates the proto as a molecule of real, tracked beads. See [Formulas](/workflows/formulas).

### What are gates?

Async coordination primitives that block a workflow step until a condition clears:

- **Human gates** wait for approval
- **Timer gates** wait for a duration
- **GitHub gates** wait for CI runs or PR events

See [Gates](/workflows/gates).

### What's the difference between molecules and wisps?

Both are instantiated workflows made of real beads. **Molecules** (`bd mol pour`) are persistent — part of history, synced like any bead. **Wisps** (`bd mol wisp`) are ephemeral — flagged so they stay out of federation push and can be deleted wholesale (`bd purge`, `bd mol wisp gc`) once closed. Use molecules for work worth referencing later, wisps for operational loops like release checklists and health patrols. See [Molecules](/workflows/molecules) and [Wisps](/workflows/wisps).

## Integrations

### Should I use the CLI or MCP?

**Use CLI + hooks** when a shell is available (Claude Code, Cursor, and similar):

- Lower context overhead (on the order of a couple thousand tokens, versus tens of thousands for a full set of MCP tool schemas)
- Faster execution
- Universal across editors

**Use MCP** when the CLI is unavailable (for example Claude Desktop). See [MCP Server](/integrations/mcp-server).

### How do I integrate with my editor?

```bash
bd setup claude   # Claude Code
bd setup cursor   # Cursor
bd setup aider    # Aider
```

`bd setup` also supports copilot, gemini, factory, codex, mux, opencode, junie, windsurf, cody, and kilocode. See [IDE Setup](/getting-started/ide-setup) and the [integrations index](/integrations/index).

### Can beads import from GitHub Issues?

Yes — `bd github sync --pull-only` imports issues in bulk (`bd github pull <refs>` cherry-picks specific ones), and `bd github sync` keeps beads and GitHub in sync bidirectionally. See the [bd github reference](/cli-reference/github).

## Migration

### How do I migrate from GitHub Issues, Jira, or Linear?

Beads has built-in bidirectional sync for all three — `bd github`, `bd jira`, and `bd linear` each provide `sync` for bulk moves, plus `pull`/`push` for specific issues by ID (GitLab, Azure DevOps, and Notion are covered by `bd gitlab`, `bd ado`, and `bd notion`). Configure credentials with `bd config set` per the [CLI reference](/cli-reference/index), then run the sync in the pull direction: `bd github sync --pull-only`, `bd jira sync --pull`, or `bd linear sync --pull`.

For any other tracker: export from it (usually CSV or JSON), convert to beads' JSONL format, and run `bd import <file>`. See [examples](https://github.com/gastownhall/beads/tree/main/examples) for scripting patterns.

### Can I export back out of beads?

For GitHub, Jira, and Linear, use the same integrations in the push direction — `bd github sync --push-only`, `bd jira sync --push`, or `bd linear sync --push` for everything, or `bd <tracker> push <ids>` for specific beads. For anything else, `bd export -o issues.jsonl` produces JSONL you can convert with a script and feed to the target system's API.

## Performance

### How does beads handle scale?

Dolt is a SQL database and comfortably handles far more issues than a typical project accumulates. Commands stay fast at the thousands-of-issues scale; for extremely large projects (100k+ issues), consider splitting into multiple databases per component.

### What if my database gets too large?

`bd gc` runs the full lifecycle: deletes old closed issues, squashes old Dolt commits, and runs Dolt garbage collection to reclaim disk space.

```bash
bd gc --dry-run          # Preview all phases
bd gc                    # Delete issues closed 90+ days ago, compact, GC
bd gc --older-than 30    # More aggressive decay window
```

For semantic summarization of old closed issues instead of deletion, see `bd admin compact`. Or split the project:

```bash
cd ~/project/frontend && bd init --prefix fe
cd ~/project/backend && bd init --prefix be
```

## Use Cases

### Can I use beads for non-code projects?

Sure — beads is just an issue tracker. Writing projects (chapters as issues, outlines as dependencies), research (papers, experiments), home projects (renovations with blocking tasks) — any workflow with dependencies works, and the agent-friendly design fits any AI-assisted workflow.

## Technical

### What dependencies does beads have?

Beads is a single static binary with no runtime dependencies — the Dolt engine is embedded in-process. No PostgreSQL, no Redis, no Docker, no node_modules. The standalone `dolt` CLI is only needed if you run server mode, and git is only needed to version your project code. See [Installation](/getting-started/installation).

### Can I query or extend the database directly?

Yes, three ways: `bd query` for the built-in query language (compound filters, boolean operators, date expressions), `bd sql` for raw SQL against the underlying database, and `--json` output on every command for building integrations.

### Does beads support Windows?

Yes — native Windows support, no MSYS or MinGW required. A PowerShell script installs prebuilt releases, and everything works with Windows paths. See [Installation](/getting-started/installation#windows-11).

### Can I use beads with git worktrees?

Yes — beads works from normal git worktrees with no special setup. All worktrees in a repository share the same `.beads` workspace; `bd` discovers it from linked worktrees automatically.

If an old beads version left hidden worktrees under `.git/` (from a since-removed sync-branch feature), clean them up:

```bash
rm -rf .git/beads-worktrees
rm -rf .git/worktrees/beads-*
git worktree prune
```

See [Worktrees](/reference/worktrees) for details and legacy cleanup.

## Troubleshooting

### Why is the Dolt server not starting?

This applies to server mode only (embedded mode has no server):

```bash
bd doctor                        # Check health
cat .beads/dolt-server.log       # Check server logs (server mode)
bd dolt stop && bd dolt start    # Restart the server
```

See [Troubleshooting](/reference/troubleshooting).

### Why aren't my changes syncing?

```bash
bd dolt push     # Push to the Dolt remote
bd hooks list    # Verify git hooks are installed
bd doctor        # Check for deeper issues
```

See the [sync failures runbook](/recovery/sync-failures).

### What's the difference between database corruption and ID collisions?

These are two distinct integrity issues:

**Logical consistency** — same ID assigned to different issues, wrong-prefix bugs, branch divergence. Hash-based IDs eliminate collisions by design, and `bd doctor --fix` repairs logical inconsistencies.

**Physical corruption** — disk failures, power loss, or multiple processes writing to an embedded database simultaneously. Start with the [database corruption runbook](/recovery/database-corruption) (`bd doctor --fix` after backing up `.beads/`). If the database is unrecoverable and you have a Dolt remote, back up `.beads/`, delete the data directory for your mode — `.beads/embeddeddolt/` in embedded mode, `.beads/dolt/` in server mode — then re-init and pull:

```bash
cp -r .beads .beads.backup
rm -rf .beads/embeddeddolt   # or .beads/dolt in server mode
bd init
bd dolt pull
```

For multi-writer scenarios, use server mode so concurrent access goes through the server instead of racing on files.

### How do I report a bug?

1. Check existing issues: https://github.com/gastownhall/beads/issues
2. Include `bd version`, `bd info --json`, and reproduction steps
3. File at: https://github.com/gastownhall/beads/issues/new

## Getting Help

### Where can I get more help?

- **Documentation**: [Quickstart](/getting-started/quickstart), [Advanced Features](/reference/advanced), [README](https://github.com/gastownhall/beads/blob/main/README.md)
- **Troubleshooting**: [Troubleshooting guide](/reference/troubleshooting) and the [recovery runbooks](/recovery/index)
- **Examples**: [examples/](https://github.com/gastownhall/beads/tree/main/examples)
- **GitHub Issues**: [Report bugs or request features](https://github.com/gastownhall/beads/issues)
- **GitHub Discussions**: [Ask questions](https://github.com/gastownhall/beads/discussions)

### How can I contribute?

Contributions are welcome. See [CONTRIBUTING.md](https://github.com/gastownhall/beads/blob/main/CONTRIBUTING.md) for guidelines, test instructions, and the development workflow.

### Where's the roadmap?

The roadmap lives in beads itself:

```bash
bd list --priority-max 1 --json   # All P0 and P1 issues
```

Or check GitHub Issues for feature requests and planned improvements.
