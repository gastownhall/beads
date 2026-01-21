# Configuration System

bd has two complementary configuration systems:

1. **Tool-level configuration** (Viper): User preferences for tool behavior (flags, output format)
2. **Project-level configuration** (`bd config`): Integration data and project-specific settings

## Tool-Level Configuration (Viper)

### Overview

Tool preferences control how `bd` behaves globally or per-user. These are stored in config files or environment variables and managed by [Viper](https://github.com/spf13/viper).

**Configuration precedence** (highest to lowest):
1. Command-line flags (`--json`, `--no-daemon`, etc.)
2. Environment variables (`BD_JSON`, `BD_NO_DAEMON`, etc.)
3. Config file (`~/.config/bd/config.yaml` or `.beads/config.yaml`)
4. Defaults

### Config File Locations

Viper searches for `config.yaml` in these locations (in order):
1. `.beads/config.yaml` - Project-specific tool settings (version-controlled)
2. `~/.config/bd/config.yaml` - User-specific tool settings
3. `~/.beads/config.yaml` - Legacy user settings

### Supported Settings

Tool-level settings you can configure:

| Setting | Flag | Environment Variable | Default | Description |
|---------|------|---------------------|---------|-------------|
| `json` | `--json` | `BD_JSON` | `false` | Output in JSON format |
| `no-daemon` | `--no-daemon` | `BD_NO_DAEMON` | `false` | Force direct mode, bypass daemon |
| `no-auto-flush` | `--no-auto-flush` | `BD_NO_AUTO_FLUSH` | `false` | Disable auto JSONL export |
| `no-auto-import` | `--no-auto-import` | `BD_NO_AUTO_IMPORT` | `false` | Disable auto JSONL import |
| `no-push` | `--no-push` | `BD_NO_PUSH` | `false` | Skip pushing to remote in bd sync |
| `sync.mode` | - | `BD_SYNC_MODE` | `git-portable` | Sync mode (see below) |
| `sync.export_on` | - | `BD_SYNC_EXPORT_ON` | `push` | When to export: `push`, `change` |
| `sync.import_on` | - | `BD_SYNC_IMPORT_ON` | `pull` | When to import: `pull`, `change` |
| `conflict.strategy` | - | `BD_CONFLICT_STRATEGY` | `newest` | Conflict resolution: `newest`, `ours`, `theirs`, `manual` |
| `federation.remote` | - | `BD_FEDERATION_REMOTE` | (none) | Dolt remote URL for federation |
| `federation.sovereignty` | - | `BD_FEDERATION_SOVEREIGNTY` | (none) | Data sovereignty tier: `T1`, `T2`, `T3`, `T4` |
| `create.require-description` | - | `BD_CREATE_REQUIRE_DESCRIPTION` | `false` | Require description when creating issues |
| `validation.on-create` | - | `BD_VALIDATION_ON_CREATE` | `none` | Template validation on create: `none`, `warn`, `error` |
| `validation.on-sync` | - | `BD_VALIDATION_ON_SYNC` | `none` | Template validation before sync: `none`, `warn`, `error` |
| `git.author` | - | `BD_GIT_AUTHOR` | (none) | Override commit author for beads commits |
| `git.no-gpg-sign` | - | `BD_GIT_NO_GPG_SIGN` | `false` | Disable GPG signing for beads commits |
| `directory.labels` | - | - | (none) | Map directories to labels for automatic filtering |
| `external_projects` | - | - | (none) | Map project names to paths for cross-project deps |
| `db` | `--db` | `BD_DB` | (auto-discover) | Database path |
| `actor` | `--actor` | `BD_ACTOR` | `git config user.name` | Actor name for audit trail (see below) |
| `flush-debounce` | - | `BEADS_FLUSH_DEBOUNCE` | `5s` | Debounce time for auto-flush |
| `auto-start-daemon` | - | `BEADS_AUTO_START_DAEMON` | `true` | Auto-start daemon if not running |
| `daemon-log-max-size` | - | `BEADS_DAEMON_LOG_MAX_SIZE` | `50` | Max daemon log size in MB before rotation |
| `daemon-log-max-backups` | - | `BEADS_DAEMON_LOG_MAX_BACKUPS` | `7` | Max number of old log files to keep |
| `daemon-log-max-age` | - | `BEADS_DAEMON_LOG_MAX_AGE` | `30` | Max days to keep old log files |
| `daemon-log-compress` | - | `BEADS_DAEMON_LOG_COMPRESS` | `true` | Compress rotated log files |

**Backend note (SQLite vs Dolt):**
- **SQLite** supports daemon mode and auto-start.
- **Dolt (embedded)** is treated as **single-process-only**. Daemon mode and auto-start are disabled; `auto-start-daemon` has no effect. If you need daemon mode, use the SQLite backend (`bd init --backend sqlite`).

### Actor Identity Resolution

The actor name (used for `created_by` in issues and audit trails) is resolved in this order:

1. `--actor` flag (explicit override)
2. `BD_ACTOR` environment variable
3. `BEADS_ACTOR` environment variable (alias for MCP/integration compatibility)
4. `git config user.name`
5. `$USER` environment variable (system username fallback)
6. `"unknown"` (final fallback)

For most developers, no configuration is needed - beads will use your git identity automatically. This ensures your issue authorship matches your commit authorship.

To override, set `BD_ACTOR` in your shell profile:
```bash
export BD_ACTOR="my-github-handle"
```

### Sync Mode Configuration

The sync mode controls how beads synchronizes data with git and/or Dolt remotes.

#### Sync Modes

| Mode | Description |
|------|-------------|
| `git-portable` | (default) Export JSONL on push, import on pull. Standard git-based workflow. |
| `realtime` | Export JSONL on every database change. Legacy behavior, higher I/O. |
| `dolt-native` | Use Dolt remotes directly for sync. JSONL is not used for sync (but manual `bd import` / `bd export` still work). |
| `belt-and-suspenders` | Both Dolt remote AND JSONL backup. Maximum redundancy. |

#### Sync Triggers

Control when sync operations occur:

- `sync.export_on`: `push` (default) or `change`
- `sync.import_on`: `pull` (default) or `change`

#### Conflict Resolution Strategies

When merging conflicting changes:

| Strategy | Description |
|----------|-------------|
| `newest` | (default) Keep the version with the newer `updated_at` timestamp |
| `ours` | Always keep the local version |
| `theirs` | Always keep the remote version |
| `manual` | Require interactive resolution for each conflict |

#### Federation Configuration

For Dolt-native or belt-and-suspenders modes:

- `federation.remote`: Dolt remote URL (e.g., `dolthub://org/beads`, `gs://bucket/beads`, `s3://bucket/beads`)
- `federation.sovereignty`: Data sovereignty tier:
  - `T1`: Full sovereignty - data never leaves controlled infrastructure
  - `T2`: Regional sovereignty - data stays within region/jurisdiction
  - `T3`: Provider sovereignty - data with trusted cloud provider
  - `T4`: No restrictions - data can be anywhere

#### Example Sync Configuration

```yaml
# .beads/config.yaml
sync:
  mode: git-portable    # git-portable | realtime | dolt-native | belt-and-suspenders
  export_on: push       # push | change
  import_on: pull       # pull | change

conflict:
  strategy: newest      # newest | ours | theirs | manual

# Optional: Dolt federation for dolt-native or belt-and-suspenders modes
federation:
  remote: dolthub://myorg/beads
  sovereignty: T2
```

#### When to Use Each Mode

- **git-portable** (default): Best for most teams. JSONL is committed to git, works with any git hosting.
- **realtime**: Use when you need instant JSONL updates (e.g., file watchers, CI triggers on JSONL changes).
- **dolt-native**: Use when you have Dolt infrastructure and want database-level sync; JSONL remains available for portability/audits/manual workflows.
- **belt-and-suspenders**: Use for critical data where you want both Dolt sync AND git-portable backup.

### Example Config File

`~/.config/bd/config.yaml`:
```yaml
# Default to JSON output for scripting
json: true

# Disable daemon for single-user workflows
no-daemon: true

# Custom debounce for auto-flush (default 5s)
flush-debounce: 10s

# Auto-start daemon (default true)
auto-start-daemon: true

# Daemon log rotation settings
daemon-log-max-size: 50      # MB per file (default 50)
daemon-log-max-backups: 7    # Number of old logs to keep (default 7)
daemon-log-max-age: 30       # Days to keep old logs (default 30)
daemon-log-compress: true    # Compress rotated logs (default true)
```

`.beads/config.yaml` (project-specific):
```yaml
# Project team prefers longer flush delay
flush-debounce: 15s

# Require descriptions on all issues (enforces context for future work)
create:
  require-description: true

# Template validation settings (bd-t7jq)
# Validates that issues include required sections based on issue type
# Values: none (default), warn (print warning), error (block operation)
validation:
  on-create: warn   # Warn when creating issues missing sections
  on-sync: none     # No validation on sync (backwards compatible)

# Git commit signing options (GH#600)
# Useful when you have Touch ID commit signing that prompts for each commit
git:
  author: "beads-bot <beads@example.com>"  # Override commit author
  no-gpg-sign: true                         # Disable GPG signing

# Directory-aware label scoping for monorepos (GH#541)
# When running bd ready/list from a matching directory, issues with
# that label are automatically shown (as if --label-any was passed)
directory:
  labels:
    packages/maverick: maverick
    packages/agency: agency
    packages/io: io

# Cross-project dependency resolution (bd-h807)
# Maps project names to paths for resolving external: blocked_by references
# Paths can be relative (from cwd) or absolute
external_projects:
  beads: ../beads
  gastown: /path/to/gastown
```

### Why Two Systems?

**Tool settings (Viper)** are user preferences:
- How should I see output? (`--json`)
- Should I use the daemon? (`--no-daemon`)
- How should the CLI behave?

**Project config (`bd config`)** is project data:
- What's our Jira URL?
- What are our Linear tokens?
- How do we map statuses?

This separation is correct: **tool settings are user-specific, project config is team-shared**.

Agents benefit from `bd config`'s structured CLI interface over manual YAML editing.

## Project-Level Configuration (`bd config`)

### Overview

Project configuration is:
- **Per-project**: Isolated to each `.beads/*.db` database
- **Version-control-friendly**: Stored in SQLite, queryable and scriptable
- **Machine-readable**: JSON output for automation
- **Namespace-based**: Organized by integration or purpose

## Commands

### Set Configuration

```bash
bd config set <key> <value>
bd config set --json <key> <value>  # JSON output
```

Examples:
```bash
bd config set jira.url "https://company.atlassian.net"
bd config set jira.project "PROJ"
bd config set jira.status_map.todo "open"
```

### Get Configuration

```bash
bd config get <key>
bd config get --json <key>  # JSON output
```

Examples:
```bash
bd config get jira.url
# Output: https://company.atlassian.net

bd config get --json jira.url
# Output: {"key":"jira.url","value":"https://company.atlassian.net"}
```

### List All Configuration

```bash
bd config list
bd config list --json  # JSON output
```

Example output:
```
Configuration:
  compact_tier1_days = 90
  compact_tier1_dep_levels = 2
  jira.project = PROJ
  jira.url = https://company.atlassian.net
```

JSON output:
```json
{
  "compact_tier1_days": "90",
  "compact_tier1_dep_levels": "2",
  "jira.project": "PROJ",
  "jira.url": "https://company.atlassian.net"
}
```

### Unset Configuration

```bash
bd config unset <key>
bd config unset --json <key>  # JSON output
```

Example:
```bash
bd config unset jira.url
```

## Namespace Convention

Configuration keys use dot-notation namespaces to organize settings:

### Core Namespaces

- `compact_*` - Compaction settings (see EXTENDING.md)
- `issue_prefix` - Issue ID prefix (managed by `bd init`)
- `max_collision_prob` - Maximum collision probability for adaptive hash IDs (default: 0.25)
- `min_hash_length` - Minimum hash ID length (default: 4)
- `max_hash_length` - Maximum hash ID length (default: 8)
- `import.orphan_handling` - How to handle hierarchical issues with missing parents during import (default: `allow`)
- `export.error_policy` - Error handling strategy for exports (default: `strict`)
- `export.retry_attempts` - Number of retry attempts for transient errors (default: 3)
- `export.retry_backoff_ms` - Initial backoff in milliseconds for retries (default: 100)
- `export.skip_encoding_errors` - Skip issues that fail JSON encoding (default: false)
- `export.write_manifest` - Write .manifest.json with export metadata (default: false)
- `auto_export.error_policy` - Override error policy for auto-exports (default: `best-effort`)
- `sync.branch` - Name of the dedicated sync branch for beads data (see docs/PROTECTED_BRANCHES.md)
- `sync.require_confirmation_on_mass_delete` - Require interactive confirmation before pushing when >50% of issues vanish during a merge AND more than 5 issues existed before (default: `false`)

### Integration Namespaces

Use these namespaces for external integrations:

- `jira.*` - Jira integration settings
- `linear.*` - Linear integration settings
- `github.*` - GitHub integration settings
- `custom.*` - Custom integration settings

### Example: Adaptive Hash ID Configuration

```bash
# Configure adaptive ID lengths (see docs/ADAPTIVE_IDS.md)
# Default: 25% max collision probability
bd config set max_collision_prob "0.25"

# Start with 4-char IDs, scale up as database grows
bd config set min_hash_length "4"
bd config set max_hash_length "8"

# Stricter collision tolerance (1%)
bd config set max_collision_prob "0.01"

# Force minimum 5-char IDs for consistency
bd config set min_hash_length "5"
```

See [ADAPTIVE_IDS.md](ADAPTIVE_IDS.md) for detailed documentation.

### Example: Export Error Handling

Controls how export operations handle errors when fetching issue data (labels, comments, dependencies).

```bash
# Strict: Fail fast on any error (default for user-initiated exports)
bd config set export.error_policy "strict"

# Best-effort: Skip failed operations with warnings (good for auto-export)
bd config set export.error_policy "best-effort"

# Partial: Retry transient failures, skip persistent ones with manifest
bd config set export.error_policy "partial"
bd config set export.write_manifest "true"

# Required-core: Fail on core data (issues/deps), skip enrichments (labels/comments)
bd config set export.error_policy "required-core"

# Customize retry behavior
bd config set export.retry_attempts "5"
bd config set export.retry_backoff_ms "200"

# Skip individual issues that fail JSON encoding
bd config set export.skip_encoding_errors "true"

# Auto-export uses different policy (background operation)
bd config set auto_export.error_policy "best-effort"
```

**Policy details:**

- **`strict`** (default) - Fail immediately on any error. Ensures complete exports but may block on transient issues like database locks. Best for critical exports and migrations.

- **`best-effort`** - Skip failed batches with warnings. Continues export even if labels or comments fail to load. Best for auto-exports and background sync where availability matters more than completeness.

- **`partial`** - Retry transient failures (3x by default), then skip with manifest file. Creates `.manifest.json` alongside JSONL documenting what succeeded/failed. Best for large databases with occasional corruption.

- **`required-core`** - Fail on core data (issues, dependencies), skip enrichments (labels, comments) with warnings. Best when metadata is secondary to issue tracking.

**When to use each mode:**

- Use `strict` (default) for production backups and critical exports
- Use `best-effort` for auto-exports (default via `auto_export.error_policy`)
- Use `partial` when you need visibility into export completeness
- Use `required-core` when labels/comments are optional

**Context-specific behavior:**

User-initiated exports (`bd sync`, manual export commands) use `export.error_policy` (default: `strict`).

Auto-exports (daemon background sync) use `auto_export.error_policy` (default: `best-effort`), falling back to `export.error_policy` if not set.

**Example: Different policies for different contexts:**

```bash
# Critical project: strict everywhere
bd config set export.error_policy "strict"

# Development project: strict user exports, permissive auto-exports
bd config set export.error_policy "strict"
bd config set auto_export.error_policy "best-effort"

# Large database with occasional corruption
bd config set export.error_policy "partial"
bd config set export.write_manifest "true"
bd config set export.retry_attempts "5"
```

### Example: Import Orphan Handling

Controls how imports handle hierarchical child issues when their parent is missing from the database:

```bash
# Strictest: Fail import if parent is missing (safest, prevents orphans)
bd config set import.orphan_handling "strict"

# Auto-resurrect: Search JSONL history and recreate missing parents as tombstones
bd config set import.orphan_handling "resurrect"

# Skip: Skip orphaned issues with warning (partial import)
bd config set import.orphan_handling "skip"

# Allow: Import orphans without validation (default, most permissive)
bd config set import.orphan_handling "allow"
```

**Mode details:**

- **`strict`** - Import fails immediately if a child's parent is missing. Use when database integrity is critical.
- **`resurrect`** - Searches the full JSONL file for missing parents and recreates them as tombstones (Status=Closed, Priority=4). Preserves hierarchy with minimal data. Dependencies are also resurrected on best-effort basis.
- **`skip`** - Skips orphaned children with a warning. Partial import succeeds but some issues are excluded.
- **`allow`** - Imports orphans without parent validation. Most permissive, works around import bugs. **This is the default** because it ensures all data is imported even if hierarchy is temporarily broken.

**Override per command:**
```bash
# Override config for a single import
bd import -i issues.jsonl --orphan-handling strict

# Auto-import (sync) uses config value
bd sync  # Respects import.orphan_handling setting
```

**When to use each mode:**

- Use `allow` (default) for daily imports and auto-sync - ensures no data loss
- Use `resurrect` when importing from another database that had parent deletions
- Use `strict` only for controlled imports where you need to guarantee parent existence
- Use `skip` rarely - only when you want to selectively import a subset

### Example: Sync Safety Options

Controls for the sync branch workflow (see docs/PROTECTED_BRANCHES.md):

```bash
# Configure sync branch (required for protected branch workflow)
bd config set sync.branch beads-sync

# Enable mass deletion protection (optional, default: false)
# When enabled, if >50% of issues vanish during a merge AND more than 5
# issues existed before the merge, bd sync will:
# 1. Show forensic info about vanished issues
# 2. Prompt for confirmation before pushing
bd config set sync.require_confirmation_on_mass_delete "true"
```

**When to enable `sync.require_confirmation_on_mass_delete`:**

- Multi-user workflows where accidental mass deletions could propagate
- Critical projects where data loss prevention is paramount
- When you want manual review before pushing large changes

**When to keep it disabled (default):**

- Single-user workflows where you trust your local changes
- CI/CD pipelines that need non-interactive sync
- When you want hands-free automation

### Example: Jira Integration

```bash
# Configure Jira connection
bd config set jira.url "https://company.atlassian.net"
bd config set jira.project "PROJ"
bd config set jira.api_token "YOUR_TOKEN"

# Map bd statuses to Jira statuses
bd config set jira.status_map.open "To Do"
bd config set jira.status_map.in_progress "In Progress"
bd config set jira.status_map.closed "Done"

# Map bd issue types to Jira issue types
bd config set jira.type_map.bug "Bug"
bd config set jira.type_map.feature "Story"
bd config set jira.type_map.task "Task"
```

### Example: Linear Integration

Linear integration provides bidirectional sync between bd and Linear via GraphQL API.

**Required configuration:**

```bash
# API Key (can also use LINEAR_API_KEY environment variable)
bd config set linear.api_key "lin_api_YOUR_API_KEY"

# Team ID (find in Linear team settings or URL)
bd config set linear.team_id "team-uuid-here"
```

**Getting your Linear credentials:**

1. **API Key**: Go to Linear → Settings → API → Personal API keys → Create key
2. **Team ID**: Go to Linear → Settings → General → Team ID (or extract from URLs)

**Priority mapping (Linear 0-4 → Beads 0-4):**

Linear and Beads both use 0-4 priority scales, but with different semantics:
- Linear: 0=no priority, 1=urgent, 2=high, 3=medium, 4=low
- Beads: 0=critical, 1=high, 2=medium, 3=low, 4=backlog

Default mapping (configurable):

```bash
bd config set linear.priority_map.0 4    # No priority -> Backlog
bd config set linear.priority_map.1 0    # Urgent -> Critical
bd config set linear.priority_map.2 1    # High -> High
bd config set linear.priority_map.3 2    # Medium -> Medium
bd config set linear.priority_map.4 3    # Low -> Low
```

**State mapping (Linear state types → Beads statuses):**

Map Linear workflow state types to Beads statuses:

```bash
bd config set linear.state_map.backlog open
bd config set linear.state_map.unstarted open
bd config set linear.state_map.started in_progress
bd config set linear.state_map.completed closed
bd config set linear.state_map.canceled closed

# For custom workflow states, use lowercase state name:
bd config set linear.state_map.in_review in_progress
bd config set linear.state_map.blocked blocked
bd config set linear.state_map.on_hold blocked
```

**Label to issue type mapping:**

Infer bd issue type from Linear labels:

```bash
bd config set linear.label_type_map.bug bug
bd config set linear.label_type_map.defect bug
bd config set linear.label_type_map.feature feature
bd config set linear.label_type_map.enhancement feature
bd config set linear.label_type_map.epic epic
bd config set linear.label_type_map.chore chore
bd config set linear.label_type_map.maintenance chore
bd config set linear.label_type_map.task task
```

**Relation type mapping (Linear relations → Beads dependencies):**

```bash
bd config set linear.relation_map.blocks blocks
bd config set linear.relation_map.blockedBy blocks
bd config set linear.relation_map.duplicate duplicates
bd config set linear.relation_map.related related
```

**Sync commands:**

```bash
# Bidirectional sync (pull then push, with conflict resolution)
bd linear sync

# Pull only (import from Linear)
bd linear sync --pull

# Push only (export to Linear)
bd linear sync --push

# Dry run (preview without changes)
bd linear sync --dry-run

# Conflict resolution options
bd linear sync --prefer-local    # Local version wins on conflicts
bd linear sync --prefer-linear   # Linear version wins on conflicts
# Default: newer timestamp wins

# Check sync status
bd linear status
```

**Automatic sync tracking:**

The `linear.last_sync` config key is automatically updated after each sync, enabling incremental sync (only fetch issues updated since last sync).

### Example: GitHub Integration

```bash
# Configure GitHub connection
bd config set github.org "myorg"
bd config set github.repo "myrepo"
bd config set github.token "YOUR_TOKEN"

# Map bd labels to GitHub labels
bd config set github.label_map.bug "bug"
bd config set github.label_map.feature "enhancement"
```

## Use in Scripts

Configuration is designed for scripting. Use `--json` for machine-readable output:

```bash
#!/bin/bash

# Get Jira URL
JIRA_URL=$(bd config get --json jira.url | jq -r '.value')

# Get all config and extract multiple values
bd config list --json | jq -r '.["jira.project"]'
```

Example Python script:
```python
import json
import subprocess

def get_config(key):
    result = subprocess.run(
        ["bd", "config", "get", "--json", key],
        capture_output=True,
        text=True
    )
    data = json.loads(result.stdout)
    return data["value"]

def list_config():
    result = subprocess.run(
        ["bd", "config", "list", "--json"],
        capture_output=True,
        text=True
    )
    return json.loads(result.stdout)

# Use in integration
jira_url = get_config("jira.url")
jira_project = get_config("jira.project")
```

## Best Practices

1. **Use namespaces**: Prefix keys with integration name (e.g., `jira.*`, `linear.*`)
2. **Hierarchical keys**: Use dots for structure (e.g., `jira.status_map.open`)
3. **Document your keys**: Add comments in integration scripts
4. **Security**: Store tokens in config, but add `.beads/*.db` to `.gitignore` (bd does this automatically)
5. **Per-project**: Configuration is project-specific, so each repo can have different settings

## Integration with bd Commands

Some bd commands automatically use configuration:

- `bd admin compact` uses `compact_tier1_days`, `compact_tier1_dep_levels`, etc.
- `bd init` sets `issue_prefix`

External integration scripts can read configuration to sync with Jira, Linear, GitHub, etc.

## See Also

- [README.md](../README.md) - Main documentation
- [EXTENDING.md](EXTENDING.md) - Database schema and compaction config
