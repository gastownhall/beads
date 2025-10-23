# Configuration System

bd supports per-project configuration stored in `.beads/*.db` for external integrations and user preferences.

## Overview

Configuration is:
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

### Integration Namespaces

Use these namespaces for external integrations:

- `jira.*` - Jira integration settings
- `linear.*` - Linear integration settings
- `github.*` - GitHub integration settings
- `custom.*` - Custom integration settings

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
