---
title: Configuration
description: Complete reference for bd configuration across config.yaml and database-stored settings, with precedence, secrets, auto-commit, backup, and integrations.
---

Complete configuration reference for beads.

Last reviewed: 2026-07-10

Freshness source: `cmd/bd/main.go`, `cmd/bd/config.go`, and `internal/configfile/`.

beads has two complementary configuration systems:

1. **Tool-level configuration** (YAML, managed by [Viper](https://github.com/spf13/viper)) — startup flags and tool behavior, stored in `config.yaml` files. These are user preferences: output format, auto-commit behavior, CLI ergonomics.
2. **Project-level configuration** (managed by `bd config`) — integration credentials, status maps, and project-specific settings, stored in the Dolt database. Some keys are routed to `config.yaml` instead (see [YAML-only keys](#yaml-only-keys-startup-settings) below).

The split is deliberate: tool settings are user-specific; project config is team-shared and travels with the database when you run `bd dolt push`. That is also why secrets are refused in the database — see [Security](#security-where-secrets-live).

Dolt is the only storage backend. Embedded mode (the default) stores data at `.beads/embeddeddolt/`; server mode (`bd init --server` or `BEADS_DOLT_SERVER_MODE=1`) uses `.beads/dolt/`. See [Dolt architecture](/architecture/dolt).

## Configuration Locations

`config.yaml` is searched in this order, with later files overriding earlier ones:

1. `~/.beads/config.yaml` (legacy user-level, lowest priority)
2. `~/.config/bd/config.yaml` (user-level; this exact path is checked even on platforms whose native user-config directory differs)
3. `<repo>/.beads/config.yaml` (project-level, walked up from the current directory)
4. `$BEADS_DIR/config.yaml` (highest priority, when `BEADS_DIR` points at a different workspace)

A `config.local.yaml` next to the project `config.yaml` is also merged in last for machine-specific overrides that should not be committed.

## Precedence

For Viper-managed (YAML) keys, highest to lowest:

1. **Command-line flags** (e.g. `--json`, `--db`, `--actor`)
2. **Environment variables** (`BD_*`, plus a small set of legacy `BEADS_*` names — see below)
3. **`config.yaml`** files (in the order listed above)
4. **Built-in defaults**

Project-level keys written via `bd config set` (Jira, Linear, GitHub, status maps, etc.) live in the Dolt database. They are read at command time and have no env var override.

When a config.yaml value or environment variable shadows a database key, `bd config list` prints an override warning, and `bd config show` reports the source of every effective key.

## Managing Configuration

```bash
# Set a value (auto-routes to config.yaml or the database)
bd config set jira.url "https://company.atlassian.net"
bd config set validation.on-create warn   # YAML-only key

# Set many values in one go (single auto-commit; validates before writing)
bd config set-many jira.url=https://example.atlassian.net jira.project=PROJ

# Get a value
bd config get jira.url
bd config get --json jira.url
# → {"key":"jira.url","value":"https://company.atlassian.net"}

# List all database-stored config (with override warnings)
bd config list

# Show all effective config with provenance (env / config.yaml / default / database)
bd config show
bd config show --source config.yaml
bd config show --json

# Validate sync-related configuration
bd config validate

# Remove a value
bd config unset jira.url
```

`bd config set` automatically routes the write to the right location: keys in the YAML namespace (see below) are written to the project `config.yaml`; everything else is written to the Dolt database. `beads.role` is stored in git config.

Unrecognized keys produce a warning with a did-you-mean suggestion; use the `custom.*` namespace for user-defined keys.

## YAML-only Keys (Startup Settings)

These keys must live in `config.yaml`, not the database, because they are read before the database is opened. Writing them with `bd config set` automatically updates `config.yaml`.

The full namespaces routed to YAML are:

`routing.*`, `sync.*`, `git.*`, `directory.*`, `repos.*`, `external_projects.*`, `validation.*`, `hierarchy.*`, `ai.*`, `backup.*`, `export.*`, `dolt.*`, `federation.*`, `metrics.*`, `list.*`

Plus these individual keys:

`no-db`, `json`, `db`, `actor`, `identity`, `no-push`, `no-git-ops`, `agent.profile`, `create.require-description`, `import.auto`, `import.path`, `prime.max-memories`, `prime.max-memory-chars`, and the secret keys `github.token`, `gitlab.token`, `jira.api_token`, `ado.pat`, `linear.api_key`, `linear.oauth_client_id`, `linear.oauth_client_secret`.

Any key whose name contains `api_key`, `api-key`, `secret`, `token`, or `password` is treated as a secret: it is refused on git-tracked `config.yaml` files unless you pass `--force-git-tracked`. Prefer exporting the value as an environment variable instead (e.g. `LINEAR_API_KEY`).

## Tool-Level Settings (config.yaml)

| Setting | Flag | Env Var | Default | Description |
|---|---|---|---|---|
| `json` | `--json` | `BD_JSON` | `false` | JSON output for scripting |
| `db` | `--db` | `BD_DB` | (auto-discover) | Database path |
| `actor` | `--actor` | `BEADS_ACTOR` | `git config user.name` | Actor name for audit trail (see [Actor identity](#actor-identity-resolution)) |
| `identity` | `--identity` | `BEADS_IDENTITY` | (git user / hostname) | Sender identity for `bd mail` |
| `no-db` | `--no-db` | `BD_NO_DAEMON` (related) | `false` | Run without opening the database |
| `no-push` | `--no-push` | `BD_NO_PUSH` | `false` | Skip pushing to the remote in `bd dolt push` |
| `no-git-ops` | — | — | `false` | Disable git ops in `bd prime` close protocol |
| `agent.profile` | — | `BD_AGENT_PROFILE` | `conservative` | Policy profile `bd prime` uses for git/commit authority: `conservative`, `minimal`, `team-maintainer`; invalid values fall back to `conservative` |
| `prime.max-memories` | `--max-memories` | `BD_PRIME_MAX_MEMORIES` | `0` | Max persistent memories injected by `bd prime` (0 = unlimited) |
| `prime.max-memory-chars` | `--max-memory-chars` | `BD_PRIME_MAX_MEMORY_CHARS` | `0` | Max total bytes of memory entries injected by `bd prime`, at whole-memory boundaries (0 = unlimited) |
| `dolt.auto-commit` | `--dolt-auto-commit` | `BD_DOLT_AUTO_COMMIT` | `on` | Create a Dolt history commit after each successful write (see [below](#auto-commit-sql-commits-vs-dolt-commits)) |
| `dolt.auto-push` | — | `BD_DOLT_AUTO_PUSH` | `false` | Auto-push to Dolt remote after writes (opt-in; see [below](#auto-push)) |
| `dolt.auto-push-interval` | — | `BD_DOLT_AUTO_PUSH_INTERVAL` | `5m` | Minimum time between auto-pushes |
| `dolt.auto-push-timeout` | — | `BD_DOLT_AUTO_PUSH_TIMEOUT` | `30s` | Timeout for a single auto-push attempt |
| `dolt.shared-server` | `--shared-server` | `BEADS_DOLT_SHARED_SERVER` | `false` | Share one Dolt server at `~/.beads/shared-server/` |
| `dolt.max-conns` | — | `BEADS_DOLT_MAX_CONNS` | `10` | Connection pool size |
| `git.author` | — | `BD_GIT_AUTHOR` | (none) | Override commit author for beads commits |
| `git.no-gpg-sign` | — | `BD_GIT_NO_GPG_SIGN` | `false` | Disable GPG signing for beads commits |
| `create.require-description` | — | `BD_CREATE_REQUIRE_DESCRIPTION` | `false` | Require description on `bd create` |
| `validation.on-create` | — | `BD_VALIDATION_ON_CREATE` | `none` | Template validation: `none`, `warn`, `error` |
| `validation.on-close` | — | `BD_VALIDATION_ON_CLOSE` | `none` | Template validation on close |
| `validation.on-sync` | — | `BD_VALIDATION_ON_SYNC` | `none` | Template validation before sync |
| `validation.metadata.mode` | — | — | `none` | Metadata schema validation |
| `hierarchy.max-depth` | — | — | `3` | Max hierarchical ID nesting depth |
| `backup.enabled` | — | `BD_BACKUP_ENABLED` | `false` | Enable periodic Dolt-native backup to `.beads/backup/` (see [below](#auto-backup)) |
| `backup.interval` | — | `BD_BACKUP_INTERVAL` | `15m` | Minimum time between auto-backups |
| `backup.git-push` | — | — | `false` | Auto-push backup repo |
| `backup.git-repo` | — | `BD_BACKUP_GIT_REPO` | (none) | Backup git repo URL; when set, backups go to a `backup/` directory inside that repo |
| `export.auto` | — | — | `false` | Refresh `.beads/issues.jsonl` export after every write; not cross-machine sync |
| `export.path` | — | — | `issues.jsonl` | Output filename relative to `.beads/` |
| `export.interval` | — | — | `60s` | Minimum time between auto-exports |
| `export.git-add` | — | — | `false` | Run `git add` on the export file |
| `import.auto` | — | `BD_IMPORT_AUTO` | `true` | Master switch for automatic JSONL imports: the git-hook fallback used when no Dolt remote is configured, and the empty-database recovery import when `.beads/issues.jsonl` exists but the database is empty. `false` disables all auto-imports; explicit `bd import` always works |
| `import.path` | — | — | `issues.jsonl` | Input filename relative to `.beads/` for implied JSONL imports (including `bd init --from-jsonl` and empty-DB auto-import); use relative paths for portability |
| `routing.mode` | — | — | (none) | Multi-repo routing: `auto`, `maintainer`, `contributor`, `explicit` |
| `routing.default` | — | — | `.` | Default routing target |
| `routing.maintainer` | — | — | `.` | Maintainer-routed path |
| `routing.contributor` | — | — | `~/.beads-planning` | Contributor-routed path |
| `list.limit` | `--limit` / `-n` | `BD_LIST_LIMIT` | `50` | Default limit for `bd list` results |
| `directory.labels` | — | — | `{}` | Map directory patterns → labels for monorepos |
| `external_projects` | — | — | `{}` | Map project names → paths for cross-project deps |
| `federation.remote` | — | `BD_FEDERATION_REMOTE` | (none) | Dolt remote URL (`dolthub://`, `gs://`, `s3://`, `az://`, `file://`) |
| `federation.sovereignty` | — | `BD_FEDERATION_SOVEREIGNTY` | (none) | Sovereignty tier: `T1`, `T2`, `T3`, `T4` (see [below](#sync-and-federation)) |
| `federation.allowed-remote-patterns` | — | — | `[]` | Glob patterns restricting allowed remote URLs |
| `federation.exclude_types` | — | — | `[wisp]` | Issue types excluded from federation push |
| `sync.require_confirmation_on_mass_delete` | — | — | `false` | Prompt before pushing when a merge deletes most issues |
| `output.title-length` | — | — | `255` | Title display in feedback (`0` hides); see routing note below |
| `ai.model` | — | `BD_AI_MODEL` | `claude-haiku-4-5-20251001` | Default AI model |
| `agents.file` | — | — | `AGENTS.md` | Agents instruction filename; see routing note below |

<Warning>
**JSONL export is opt-in**

`export.auto` and `export.git-add` are disabled unless configured explicitly.
`.beads/issues.jsonl` is an optional export for viewers, interchange, and
issue-level migration. It is not the canonical source of truth, not
cross-machine sync, and not a full database backup.

Workflows that depend on a fresh, git-staged JSONL file should opt in:

```bash
bd config set export.auto true
bd config set export.git-add true
```

Use `bd dolt push` / `bd dolt pull` for sync and `bd backup` for restorable
database backups.
</Warning>

Routing note: `output.title-length` and `agents.file` are functionally tool-level settings, but `bd config set` writes them to the Dolt database. They are typically read from `config.yaml` when set there directly.

`bd config show` is the source of truth for what's currently effective on your machine, including provenance.

## Dolt History, Backup, and Push

Three post-write behaviors run after each successful write command, in this order: auto-commit, auto-backup, auto-push.

### Auto-commit: SQL Commits vs Dolt Commits

There are two different kinds of "commit":

- **SQL transaction commit** — what happens when a `bd` command updates tables successfully (durable in the Dolt *working set*).
- **Dolt version-control commit** — what records those changes into Dolt *history* (visible in `bd history`, and what push/pull/merge workflows operate on).

By default (`dolt.auto-commit: on`), `bd` creates a Dolt history commit after each successful write command, so changes are never left only in the working set. The cost is more Dolt commits over time — one per write command — which is intentional; use `bd compact` to squash old history.

Disable for a single command:

```bash
bd --dolt-auto-commit off create "No history commit for this one"
```

Or in `config.yaml`:

```yaml
dolt:
  auto-commit: off
```

### Auto-backup

Periodic Dolt-native backup to `.beads/backup/` provides a recovery path independent of the live database. Local Dolt commits (via `dolt.auto-commit`) remain the primary safety net; backup is a secondary layer. Unlike `bd export` or `.beads/issues.jsonl`, this is a full database backup: it preserves tables, branches, commit history, and working-set data.

```yaml
backup:
  enabled: true    # Enable auto-backup after write commands
  interval: 15m    # Minimum time between auto-backups
```

How it works:

- After each write command, `bd` compares the Dolt HEAD commit hash against the last backup state.
- If data changed and the throttle interval has passed, a Dolt-native backup is synced to `.beads/backup/` (or to a `backup/` directory inside `backup.git-repo` when configured).
- State is tracked in `backup_state.json` inside the backup directory.

Manual commands (see [bd backup](/cli-reference/backup)):

```bash
bd backup init <path>     # Register a destination (filesystem or DoltHub URL)
bd backup sync            # Push to the configured destination
bd backup restore [path]  # Restore from a backup (--force to overwrite)
bd backup remove          # Unregister the destination
bd backup status          # Show configuration and last sync time
```

### Auto-push

By default, `bd` does not push automatically after write commands. Auto-push is explicit opt-in because concurrent pushes to git-protocol Dolt remotes can corrupt or strand remote history when multiple writers race.

```yaml
dolt:
  auto-push: true         # Explicit opt-in; safe for single-writer setups
  auto-push-interval: 5m  # Minimum time between auto-pushes
  auto-push-timeout: 30s  # Bound one push attempt when the remote is unreachable
```

How it works:

- After each write command (after auto-commit and auto-backup), `bd` checks whether a push is due.
- Pushes are debounced: skipped if the last push was less than `dolt.auto-push-interval` ago.
- Change detection: skipped if the Dolt HEAD commit hasn't changed since the last push.
- Push failures are warnings only (non-fatal), and failed attempts are throttled too.
- Last push time and commit are tracked in `.beads/push-state.json`, a per-machine file (not in the database, to avoid merge conflicts across machines).

Before pushing, `bd` verifies the local chunk store with `dolt fsck --quiet`, bounded by a 30-second timeout. For large stores, raise it with the runtime-only `BEADS_FSCK_TIMEOUT` environment variable (accepts durations like `2m` or bare seconds like `90`).

## Actor Identity Resolution

The actor name (used for `created_by` and audit trails) is resolved in this order:

1. `--actor` flag (explicit override)
2. `BEADS_ACTOR` environment variable
3. `BD_ACTOR` environment variable (deprecated alias)
4. `git config user.name`
5. `$USER` environment variable
6. `"unknown"` (final fallback)

For most developers no configuration is needed — issue authorship matches commit authorship automatically. To override, set `BEADS_ACTOR` in your shell profile:

```bash
export BEADS_ACTOR="my-github-handle"
```

## Project-Level Settings (Database)

These are written to the Dolt database by `bd config set` and have no env var override. Common namespaces:

| Namespace | Purpose |
|---|---|
| `jira.*` | Jira integration (URL, project(s), status_map, type_map, custom_fields) |
| `linear.*` | Linear integration (team_id(s), priority_map, state_map, label_type_map, relation_map) |
| `github.*` | GitHub integration (org, repo, label_map) |
| `gitlab.*` | GitLab integration |
| `ado.*` | Azure DevOps integration (org, project(s), state_map, type_map) |
| `notion.*` | Notion integration |
| `custom.*` | User-defined / custom integrations |
| `<tracker>.last_sync` | Updated automatically after each tracker sync; enables incremental sync |
| `status.custom` | Custom statuses with optional behavior categories (see [below](#custom-statuses-and-types)) |
| `types.custom` | Comma-separated list of custom issue types |
| `types.infra` | Infra types routed to the wisps table instead of the versioned issues table |
| `compact_tier1_days`, `compact_tier2_days` | Age thresholds in days for `bd admin compact` tier eligibility (defaults `30` and `90`) |
| `issue_id_mode` | `hash` (default) \| `counter` (see [below](#sequential-counter-ids)) |
| `min_hash_length`, `max_hash_length` | Adaptive ID bounds (defaults `3` and `8`) |
| `max_collision_prob` | Hash ID collision tolerance (default `0.25`) |
| `doctor.suppress.*` | Suppress specific `bd doctor` warnings by check slug (warnings only; errors always show) |

Issue prefix (`issue_prefix`) is **not** settable via `bd config set` — use `bd init --prefix`, `bd bootstrap`, or `bd rename-prefix`.

### Custom Statuses and Types

Custom statuses supplement the built-ins (`open`, `in_progress`, `blocked`, `deferred`, `closed`). Each entry is `name` or `name:category`:

```bash
bd config set status.custom "in_review:active,qa_testing:wip,on_hold:frozen,archived:done"
```

The category controls how the status behaves:

| Category | In `bd ready` | In default `bd list` |
|---|---|---|
| `active` | yes | yes |
| `wip` | no | yes |
| `done` | no | no (terminal) |
| `frozen` | no | no (on hold) |
| (none) | no | yes (backward compatible) |

Custom types extend the built-in issue types:

```bash
bd config set types.custom "agent,molecule,event"
```

Use `bd statuses` and `bd types` to list everything configured.

### Sequential Counter IDs

By default, beads generates hash-based IDs (e.g. `bd-a3f2`). For projects that prefer short sequential IDs (`bd-1`, `bd-2`, ...), enable counter mode:

```bash
bd config set issue_id_mode counter

bd create "First issue" -p 1    # → bd-1
bd create "Second issue" -p 2   # → bd-2
```

| Value | Behavior |
|---|---|
| `hash` | (default) Hash-based IDs, adaptive length, collision-safe |
| `counter` | Sequential integers per prefix: `bd-1`, `bd-2`, `bd-3`, ... |

Counter mode behavior:

- Each prefix (`bd`, `plug`, ...) has its own independent counter, so multi-repo or routed setups don't interleave.
- The counter is stored atomically in the database; concurrent creates within a single Dolt session are safe.
- On first use (including switching an existing repository to counter mode), the counter seeds itself from the highest existing numeric ID for that prefix, so new IDs don't collide with old ones.
- An explicit `--id` flag on `bd create` bypasses ID generation entirely; the counter is not incremented.
- Counter mode applies only to regular issues, not wisps.

Tradeoff — hash vs. counter:

| | Hash IDs | Counter IDs |
|---|---|---|
| Human readability | Lower (`bd-a3f2`) | Higher (`bd-1`) |
| Distributed/concurrent safety | Excellent (collision-free across branches) | Needs care (counters can diverge on parallel branches) |
| Predictability | Unpredictable | Sequential |
| Best for | Multi-agent, multi-branch workflows | Single-writer or project-management UIs |

### Adaptive Hash IDs

Hash IDs size themselves to the database: lengths start at `min_hash_length` and grow toward `max_hash_length` to keep the collision probability under `max_collision_prob`.

```bash
bd config set max_collision_prob "0.01"   # Stricter collision tolerance (default 0.25)
bd config set min_hash_length "5"         # Force minimum 5-char IDs (default 3)
bd config set max_hash_length "8"         # Upper bound (default 8)
```

## Sync and Federation

Beads syncs exclusively through Dolt remotes (`bd dolt push` / `bd dolt pull`) with cell-level merge. Use `bd export` for issue portability and `bd backup` for restorable database backups.

Federation settings live in `config.yaml`:

```yaml
federation:
  remote: dolthub://myorg/beads
  sovereignty: T2
```

- `federation.remote`: Dolt remote URL (`dolthub://org/beads`, `gs://bucket/beads`, `s3://bucket/beads`, `az://account.blob.core.windows.net/container/beads`, `file://...`)
- `federation.sovereignty`: data sovereignty tier:
  - `T1`: Full sovereignty — data never leaves controlled infrastructure
  - `T2`: Regional sovereignty — data stays within region/jurisdiction
  - `T3`: Provider sovereignty — data with trusted cloud provider
  - `T4`: No restrictions — data can be anywhere

`bd config validate` checks the remote URL format, the sovereignty tier, `federation.allowed-remote-patterns`, and `routing.mode`.

## Integration Configuration

Tracker settings are project-level config under the tracker's namespace; secrets (`jira.api_token`, `linear.api_key`, `github.token`, `gitlab.token`, `ado.pat`) are YAML-routed and better supplied as environment variables. Every tracker records `<tracker>.last_sync` automatically after a sync, enabling incremental syncs.

### Jira

```bash
bd config set jira.url "https://company.atlassian.net"
bd config set jira.project "PROJ"
bd config set jira.projects "PROJ1,PROJ2"   # Multiple projects (comma-separated)
export JIRA_API_TOKEN="YOUR_TOKEN"          # or: bd config set jira.api_token ...

# Map bd statuses to Jira statuses
bd config set jira.status_map.open "To Do"
bd config set jira.status_map.in_progress "In Progress"
bd config set jira.status_map.closed "Done"

# Map bd issue types to Jira issue types
bd config set jira.type_map.bug "Bug"
bd config set jira.type_map.feature "Story"
bd config set jira.type_map.task "Task"

# Set Jira custom fields on pushed issues
bd config set jira.custom_fields.customfield_10042 '{"value":"AI Platform"}'
bd config set jira.custom_fields.Story.customfield_10042 '{"value":"AI Platform"}'
```

`jira.custom_fields.<field>` applies to every issue pushed to Jira. `jira.custom_fields.<JiraType>.<field>` applies only when the mapped Jira issue type matches `<JiraType>`; per-type fields override global fields with the same field key. Values beginning with `{` or `[` are sent as JSON (useful for select-like fields); other values are sent as strings. `jira.url`, `jira.project`/`jira.projects`, and `jira.api_token` fall back to the `JIRA_URL`, `JIRA_PROJECT`/`JIRA_PROJECTS`, and `JIRA_API_TOKEN` environment variables. See [bd jira](/cli-reference/jira).

### Linear

```bash
export LINEAR_API_KEY="lin_api_YOUR_API_KEY"    # Settings → API → Personal API keys

bd config set linear.team_id "team-uuid-here"
bd config set linear.team_ids "uuid-1,uuid-2"   # Multiple teams (or LINEAR_TEAM_IDS)
```

When `linear.team_ids` is set, `bd linear sync` fetches issues from all listed teams; push with multiple teams configured requires an explicit `--team`. The singular `linear.team_id` remains supported.

Mapping namespaces — `linear.priority_map.*` (Linear 0–4 → beads 0–4), `linear.state_map.*` (Linear state types and custom state names → beads statuses, e.g. `bd config set linear.state_map.in_review in_progress`), `linear.label_type_map.*` (Linear labels → bd issue types), and `linear.relation_map.*` (Linear relations → bd dependencies; imported only when pulling with `--relations`) — are documented with defaults in [bd linear](/cli-reference/linear).

Staleness detection: after each successful pull, `bd` writes a timestamp to `.beads/last_pull` (a local-only, per-machine file covered by the `.beads/.gitignore` template). `bd linear sync --pull-if-stale` pulls only when data is older than the threshold (`--threshold`, default 20m), and a 5-minute debounce prevents agent loops. `bd prime` and other core commands never contact Linear — run `bd linear sync --pull-if-stale` from a session-start hook to keep data fresh in agent sessions.

### GitHub

```bash
bd config set github.org "myorg"
bd config set github.repo "myrepo"
export GITHUB_TOKEN="YOUR_TOKEN"    # or: bd config set github.token ...

# Map bd labels to GitHub labels
bd config set github.label_map.bug "bug"
bd config set github.label_map.feature "enhancement"
```

See [bd github](/cli-reference/github).

### Azure DevOps

Connection keys (`ado.pat`, `ado.org`, `ado.project`, `ado.projects`, `ado.url`) each have an `AZURE_DEVOPS_*` environment variable equivalent; config keys take priority over env vars. When `ado.projects` is set, `bd ado sync` fetches work items from all listed projects in a single query. State maps default to the Agile process template (override with `ado.state_map.*` / `ado.type_map.*` for Scrum or CMMI), and priority mapping (ADO 1–4 ↔ beads 0–4, with backlog collapsing to low) is automatic and not configurable. Full setup, mapping tables, and sync commands: [Azure DevOps integration](/integrations/azure-devops) and [bd ado](/cli-reference/ado).

## Environment Variables

The Viper env prefix is `BD_`. Config keys map to env vars by upper-casing and replacing `.` and `-` with `_` (e.g. `dolt.auto-commit` → `BD_DOLT_AUTO_COMMIT`, `validation.on-create` → `BD_VALIDATION_ON_CREATE`).

Selected commonly-used variables:

| Variable | Description |
|---|---|
| `BD_DB`, `BEADS_DB` | Database path (legacy `BEADS_DB` still honored) |
| `BD_JSON` | Force JSON output |
| `BD_DOLT_AUTO_COMMIT` | Override `dolt.auto-commit` (`on`/`off`) |
| `BD_DOLT_AUTO_PUSH`, `BD_DOLT_AUTO_PUSH_INTERVAL`, `BD_DOLT_AUTO_PUSH_TIMEOUT` | Override auto-push settings |
| `BD_BACKUP_ENABLED`, `BD_BACKUP_INTERVAL`, `BD_BACKUP_GIT_REPO` | Override backup settings |
| `BD_AGENT_PROFILE` | Override `agent.profile` |
| `BD_AI_MODEL` | Override AI model |
| `BD_FEDERATION_REMOTE`, `BD_FEDERATION_SOVEREIGNTY` | Override federation settings |
| `BD_VALIDATION_ON_CREATE` / `_ON_CLOSE` / `_ON_SYNC` | Override validation modes |
| `BD_NO_PAGER`, `BD_PAGER` | Pager behavior |
| `BD_NON_INTERACTIVE` | Disable prompts |
| `BD_DEBUG` | Enable debug logging |
| `BEADS_DIR` | Force the active beads workspace directory |
| `BEADS_ACTOR` | Actor identity (preferred over `BD_ACTOR`, which is a deprecated alias) |
| `BEADS_IDENTITY` | Sender identity for `bd mail` |
| `BEADS_FSCK_TIMEOUT` | Runtime-only timeout for the pre-push `dolt fsck --quiet` integrity check (default `30s`) |
| `BEADS_DOLT_SERVER_MODE`, `BEADS_DOLT_SHARED_SERVER`, `BEADS_DOLT_DATA_DIR`, `BEADS_DOLT_PORT`, ... | Embedded/server Dolt overrides |

Integration secrets follow tracker-specific conventions: `LINEAR_API_KEY`, `GITHUB_TOKEN`, `GITLAB_TOKEN`, `JIRA_API_TOKEN`, `AZURE_DEVOPS_PAT`, `ANTHROPIC_API_KEY`. These are preferred over storing the value in `config.yaml` for git-tracked projects.

`bd config show` will display the source of every effective key, making overrides explicit.

## Security: Where Secrets Live

- Tokens and API keys are never stored in the Dolt database — database config is pushed to remotes, which would expose secrets and trip GitHub secret scanning. `bd config set` routes secret keys to the local `config.yaml` instead.
- Writing a secret to a git-tracked `config.yaml` is refused unless you pass `--force-git-tracked`; environment variables are the safer default.
- `bd init` writes a `.beads/.gitignore` that keeps the database directories (`embeddeddolt/`, `dolt/`), runtime files, push state, and the federation credential key out of git.

## Example `.beads/config.yaml`

```yaml
# Default JSON output for scripting
json: true

# Dolt history & sync
dolt:
  auto-commit: on    # Create a Dolt commit after each successful write
  auto-push: false   # Opt-in for single-writer setups

# Issue creation policies
create:
  require-description: true

validation:
  on-create: warn    # Warn when creating issues missing required sections
  on-close: none
  on-sync: none

# Git commit signing for beads commits (GH#600)
git:
  author: "beads-bot <beads@example.com>"
  no-gpg-sign: true

# Periodic Dolt-native backup to .beads/backup/
backup:
  enabled: true
  interval: 15m

# Optional auto-export of issues.jsonl after writes for viewers/interchange
export:
  auto: false
  path: issues.jsonl
  interval: 60s
  git-add: false

# Optional Dolt federation
federation:
  remote: dolthub://myorg/beads
  sovereignty: T2

# Directory-aware label scoping for monorepos (GH#541)
directory:
  labels:
    packages/maverick: maverick
    packages/agency: agency

# Cross-project dependency resolution (bd-h807)
external_projects:
  beads: ../beads
  other-project: /absolute/path/to/other-project

output:
  title-length: 255
```

For machine-specific overrides that should not be committed, drop them in `.beads/config.local.yaml`; it is merged in last.

## Per-Command Override

```bash
bd --db /tmp/test.db list           # Override database for one command
bd --json --actor "ci-bot" create "Fix things"  # Multiple flags
```

## Use in Scripts

Configuration is designed for scripting; every `bd config` subcommand takes `--json`:

```bash
# Get one value ({"key":"jira.url","value":"..."})
JIRA_URL=$(bd config get --json jira.url | jq -r '.value')

# Get all database config as a flat object
bd config list --json | jq -r '.["jira.project"]'
```

## Viewing Active Configuration

```bash
bd config show                # Effective config with provenance
bd config show --json         # Machine-readable
bd config list                # Database-stored config
bd info --json | jq '.config' # Quick snapshot
```
