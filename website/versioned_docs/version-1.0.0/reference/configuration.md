---
id: configuration
title: Configuration
sidebar_position: 1
---

# Configuration

Complete configuration reference for beads.

beads has two complementary configuration systems:

1. **Tool-level configuration** (YAML, managed by [Viper](https://github.com/spf13/viper)) — startup flags and tool behavior, stored in `config.yaml` files.
2. **Project-level configuration** (managed by `bd config`) — integration credentials, status maps, and project-specific settings, stored in the Dolt database. Some keys are routed to `config.yaml` instead (see [YAML-only keys](#yaml-only-keys-startup-settings) below).

For a deeper treatment of every namespace and its semantics, see [docs/CONFIG.md](https://github.com/gastownhall/beads/blob/main/docs/CONFIG.md).

## Configuration Locations

`config.yaml` is searched in this order, with later files overriding earlier ones:

1. `~/.beads/config.yaml` (legacy user-level, lowest priority)
2. `~/.config/bd/config.yaml` (XDG user-level)
3. `<repo>/.beads/config.yaml` (project-level, walked up from the current directory)
4. `$BEADS_DIR/config.yaml` (highest priority, when `BEADS_DIR` is set)

A `config.local.yaml` next to the project `config.yaml` is also merged in last for machine-specific overrides that should not be committed.

## Precedence

For Viper-managed (YAML) keys, highest to lowest:

1. **Command-line flags** (e.g. `--json`, `--db`, `--actor`)
2. **Environment variables** (`BD_*`, plus a small set of legacy `BEADS_*` names — see below)
3. **`config.yaml`** files (in the order listed above)
4. **Built-in defaults**

Project-level keys written via `bd config set` (Jira, Linear, GitHub, status maps, etc.) live in the Dolt database. They are read at command time and have no env var override.

## Managing Configuration

```bash
# Set a value (auto-routes to config.yaml or the database)
bd config set jira.url "https://company.atlassian.net"
bd config set validation.on-create warn   # YAML-only key

# Set many values in one go
bd config set-many jira.url=https://example.atlassian.net jira.project=PROJ

# Get a value
bd config get jira.url

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

## YAML-only Keys (Startup Settings)

These keys must live in `config.yaml`, not the database, because they are read before the database is opened. Writing them with `bd config set` automatically updates `config.yaml`.

The full namespaces routed to YAML are:

`routing.*`, `sync.*`, `git.*`, `directory.*`, `repos.*`, `external_projects.*`, `validation.*`, `hierarchy.*`, `ai.*`, `backup.*`, `export.*`, `dolt.*`, `federation.*`

Plus these individual keys:

`no-db`, `json`, `db`, `actor`, `identity`, `no-push`, `no-git-ops`, `create.require-description`, `github.token`, `linear.api_key`, `linear.oauth_client_id`, `linear.oauth_client_secret`.

Secrets in this list are refused on git-tracked `config.yaml` files unless you pass `--force-git-tracked`; export the value as an environment variable instead (e.g. `LINEAR_API_KEY`).

## Tool-Level Settings (config.yaml)

| Setting | Flag | Env Var | Default | Description |
|---|---|---|---|---|
| `json` | `--json` | `BD_JSON` | `false` | JSON output for scripting |
| `db` | `--db` | `BD_DB` | (auto-discover) | Database path |
| `actor` | `--actor` | `BEADS_ACTOR` | `git config user.name` | Actor name for audit trail |
| `identity` | `--identity` | `BEADS_IDENTITY` | (git user / hostname) | Sender identity for `bd mail` |
| `no-db` | `--no-db` | `BD_NO_DAEMON` (related) | `false` | Run without opening the database |
| `no-push` | `--no-push` | — | `false` | Skip pushing to Dolt remote |
| `no-git-ops` | — | — | `false` | Disable git ops in `bd prime` close protocol |
| `dolt.auto-commit` | `--dolt-auto-commit` | `BD_DOLT_AUTO_COMMIT` | `on` | Create a Dolt history commit after each successful write |
| `dolt.auto-push` | — | `BD_DOLT_AUTO_PUSH` | `false` | Auto-push to Dolt remote after writes (opt-in) |
| `dolt.idle-timeout` | — | — | `30m` | Idle auto-stop timeout (`"0"` disables) |
| `dolt.shared-server` | `--shared-server` | `BEADS_DOLT_SHARED_SERVER` | `false` | Share one Dolt server at `~/.beads/shared-server/` |
| `dolt.max-conns` | — | `BEADS_DOLT_MAX_CONNS` | `10` | Connection pool size |
| `git.author` | — | — | (none) | Override commit author for beads commits |
| `git.no-gpg-sign` | — | — | `false` | Disable GPG signing for beads commits |
| `create.require-description` | — | `BD_CREATE_REQUIRE_DESCRIPTION` | `false` | Require description on `bd create` |
| `validation.on-create` | — | `BD_VALIDATION_ON_CREATE` | `none` | Template validation: `none`, `warn`, `error` |
| `validation.on-close` | — | `BD_VALIDATION_ON_CLOSE` | `none` | Template validation on close |
| `validation.on-sync` | — | `BD_VALIDATION_ON_SYNC` | `none` | Template validation before sync |
| `validation.metadata.mode` | — | — | `none` | Metadata schema validation |
| `hierarchy.max-depth` | — | — | `3` | Max hierarchical ID nesting depth |
| `backup.enabled` | — | `BD_BACKUP_ENABLED` | `false` | Enable periodic Dolt-native backup |
| `backup.interval` | — | `BD_BACKUP_INTERVAL` | `15m` | Minimum time between auto-backups |
| `backup.git-push` | — | — | `false` | Auto-push backup repo |
| `backup.git-repo` | — | `BD_BACKUP_GIT_REPO` | (none) | Backup git repo URL |
| `export.auto` | — | — | `true` | Refresh `.beads/issues.jsonl` after every write |
| `export.path` | — | — | `issues.jsonl` | Output filename relative to `.beads/` |
| `export.interval` | — | — | `60s` | Minimum time between auto-exports |
| `export.git-add` | — | — | `true` | Run `git add` on the export file |
| `routing.mode` | — | — | (none) | Multi-repo routing: `auto`, `maintainer`, `contributor`, `explicit` |
| `routing.default` | — | — | `.` | Default routing target |
| `routing.maintainer` | — | — | `.` | Maintainer-routed path |
| `routing.contributor` | — | — | `~/.beads-planning` | Contributor-routed path |
| `federation.remote` | — | `BD_FEDERATION_REMOTE` | (none) | Dolt remote URL (`dolthub://`, `gs://`, `s3://`, `az://`, `file://`) |
| `federation.sovereignty` | — | `BD_FEDERATION_SOVEREIGNTY` | (none) | Sovereignty tier: `T1`, `T2`, `T3`, `T4` |
| `federation.allowed-remote-patterns` | — | — | `[]` | Glob patterns restricting allowed remote URLs |
| `federation.exclude_types` | — | — | `[wisp]` | Issue types excluded from federation push |
| `sync.require_confirmation_on_mass_delete` | — | — | `false` | Prompt before pushing >50% issue deletions |
| `directory.labels` | — | — | `{}` | Map directory patterns → labels for monorepos |
| `external_projects` | — | — | `{}` | Map project names → paths for cross-project deps |
| `output.title-length` | — | — | `255` | Title display in feedback (`0` hides) |
| `ai.model` | — | `BD_AI_MODEL` | `claude-haiku-4-5-20251001` | Default AI model |
| `agents.file` | — | — | `AGENTS.md` | Agents instruction filename |

`bd config show` is the source of truth for what's currently effective on your machine, including provenance.

## Project-Level Settings (Database)

These are written to the Dolt database by `bd config set` and have no env var override. Common namespaces:

| Namespace | Purpose |
|---|---|
| `jira.*` | Jira integration (URL, project, status_map, type_map) |
| `linear.*` | Linear integration (team_id, state_map, label_type_map, relation_map) |
| `github.*` | GitHub integration (org, repo, label_map) |
| `ado.*` | Azure DevOps integration (org, project, state_map, type_map) |
| `custom.*` | User-defined / custom integrations |
| `status.custom` | Comma-separated list of custom statuses |
| `types.custom` | Comma-separated list of custom issue types |
| `types.infra` | Infra types routed to wisps table |
| `import.orphan_handling` | `allow` (default) \| `resurrect` \| `skip` \| `strict` |
| `compact_*` | Compaction tuning (see `docs/EXTENDING.md`) |
| `issue_id_mode` | `hash` (default) \| `counter` (sequential) |
| `min_hash_length`, `max_hash_length` | Adaptive ID bounds (defaults `4` and `8`) |
| `max_collision_prob` | Hash ID collision tolerance (default `0.25`) |
| `doctor.suppress.*` | Suppress specific `bd doctor` warnings by check slug |

Issue prefix (`issue_prefix`) is **not** settable via `bd config set` — use `bd init --prefix`, `bd bootstrap`, or `bd rename-prefix`.

## Environment Variables

The Viper env prefix is `BD_`. Config keys map to env vars by upper-casing and replacing `.` and `-` with `_` (e.g. `dolt.auto-commit` → `BD_DOLT_AUTO_COMMIT`, `validation.on-create` → `BD_VALIDATION_ON_CREATE`).

Selected commonly-used variables:

| Variable | Description |
|---|---|
| `BD_DB`, `BEADS_DB` | Database path (legacy `BEADS_DB` still honored) |
| `BD_JSON` | Force JSON output |
| `BD_DOLT_AUTO_COMMIT` | Override `dolt.auto-commit` (`on`/`off`) |
| `BD_DOLT_AUTO_PUSH` | Override `dolt.auto-push` |
| `BD_BACKUP_ENABLED`, `BD_BACKUP_INTERVAL`, `BD_BACKUP_GIT_REPO` | Override backup settings |
| `BD_AI_MODEL` | Override AI model |
| `BD_FEDERATION_REMOTE`, `BD_FEDERATION_SOVEREIGNTY` | Override federation settings |
| `BD_VALIDATION_ON_CREATE` / `_ON_CLOSE` / `_ON_SYNC` | Override validation modes |
| `BD_NO_PAGER`, `BD_PAGER` | Pager behavior |
| `BD_NON_INTERACTIVE` | Disable prompts |
| `BD_DEBUG` | Enable debug logging |
| `BEADS_DIR` | Force the active beads workspace directory |
| `BEADS_ACTOR` | Actor identity (preferred over `BD_ACTOR`, which is a deprecated alias) |
| `BEADS_IDENTITY` | Sender identity for `bd mail` |
| `BEADS_DOLT_SERVER_MODE`, `BEADS_DOLT_SHARED_SERVER`, `BEADS_DOLT_DATA_DIR`, `BEADS_DOLT_PORT`, ... | Embedded/server Dolt overrides |

Secrets like API tokens follow integration-specific conventions: `LINEAR_API_KEY`, `GITHUB_TOKEN`, `ANTHROPIC_API_KEY`, `AZURE_DEVOPS_PAT`. These are preferred over storing the value in `config.yaml` for git-tracked projects.

`bd config show` will display the source of every effective key, making overrides explicit.

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

# Auto-export issues.jsonl after writes (default settings shown for clarity)
export:
  auto: true
  path: issues.jsonl
  interval: 60s
  git-add: true

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

## Viewing Active Configuration

```bash
bd config show                # Effective config with provenance
bd config show --json         # Machine-readable
bd config list                # Database-stored config
bd info --json | jq '.config' # Quick snapshot
```
