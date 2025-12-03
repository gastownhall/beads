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
| `db` | `--db` | `BD_DB` | (auto-discover) | Database path |
| `actor` | `--actor` | `BD_ACTOR` | `$USER` | Actor name for audit trail |
| `flush-debounce` | - | `BEADS_FLUSH_DEBOUNCE` | `5s` | Debounce time for auto-flush |
| `auto-start-daemon` | - | `BEADS_AUTO_START_DAEMON` | `true` | Auto-start daemon if not running |
| `daemon-log-max-size` | - | `BEADS_DAEMON_LOG_MAX_SIZE` | `50` | Max daemon log size in MB before rotation |
| `daemon-log-max-backups` | - | `BEADS_DAEMON_LOG_MAX_BACKUPS` | `7` | Max number of old log files to keep |
| `daemon-log-max-age` | - | `BEADS_DAEMON_LOG_MAX_AGE` | `30` | Max days to keep old log files |
| `daemon-log-compress` | - | `BEADS_DAEMON_LOG_COMPRESS` | `true` | Compress rotated log files |

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

See [docs/ADAPTIVE_IDS.md](docs/ADAPTIVE_IDS.md) for detailed documentation.

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
bd config set sync.branch beads-metadata

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

```bash
# Configure Linear connection
bd config set linear.api_token "YOUR_TOKEN"
bd config set linear.team_id "team-123"

# Map statuses
bd config set linear.status_map.open "Backlog"
bd config set linear.status_map.in_progress "In Progress"
bd config set linear.status_map.closed "Done"
```

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

- `bd compact` uses `compact_tier1_days`, `compact_tier1_dep_levels`, etc.
- `bd init` sets `issue_prefix`

External integration scripts can read configuration to sync with Jira, Linear, GitHub, etc.

## See Also

- [README.md](README.md) - Main documentation
- [EXTENDING.md](EXTENDING.md) - Database schema and compaction config
- [examples/integrations/](examples/integrations/) - Integration examples
