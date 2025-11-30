# Jira Issues to bd Importer

Import issues from Jira Cloud or Jira Server/Data Center into `bd`.

## Overview

This tool converts Jira Issues to bd's JSONL format, supporting:
1. **Jira REST API** - Fetch issues directly from any Jira instance
2. **JSON Export** - Parse exported Jira issues JSON
3. **bd config integration** - Read credentials and mappings from `bd config`

## Features

- Fetch from Jira Cloud or Server/Data Center
- JQL query support for flexible filtering
- Configurable field mappings (status, priority, type)
- Preserve timestamps, assignees, labels
- Extract issue links as dependencies
- Set `external_ref` for re-sync capability
- Hash-based or sequential ID generation

## Installation

No dependencies required! Uses Python 3 standard library.

## Quick Start

### Option 1: Using bd config (Recommended)

Set up your Jira credentials once:

```bash
# Required settings
bd config set jira.url "https://company.atlassian.net"
bd config set jira.project "PROJ"
bd config set jira.api_token "YOUR_API_TOKEN"

# For Jira Cloud, also set username (your email)
bd config set jira.username "you@company.com"
```

Then import:

```bash
python jira2jsonl.py --from-config | bd import
```

### Option 2: Using environment variables

```bash
export JIRA_API_TOKEN=your_token
export JIRA_USERNAME=you@company.com  # For Jira Cloud

python jira2jsonl.py \
  --url https://company.atlassian.net \
  --project PROJ \
  | bd import
```

### Option 3: Command-line arguments

```bash
python jira2jsonl.py \
  --url https://company.atlassian.net \
  --project PROJ \
  --username you@company.com \
  --api-token YOUR_TOKEN \
  | bd import
```

## Authentication

### Jira Cloud

Jira Cloud requires:
1. **Username**: Your email address
2. **API Token**: Create at https://id.atlassian.com/manage-profile/security/api-tokens

```bash
bd config set jira.username "you@company.com"
bd config set jira.api_token "your_api_token"
```

### Jira Server/Data Center

Jira Server/DC can use:
- **Personal Access Token (PAT)** - Just set the token, no username needed
- **Username + Password** - Set both username and password as the token

```bash
# Using PAT (recommended)
bd config set jira.api_token "your_pat_token"

# Using username/password
bd config set jira.username "your_username"
bd config set jira.api_token "your_password"
```

## Usage

### Basic Usage

```bash
# Fetch all issues from a project
python jira2jsonl.py --from-config | bd import

# Save to file first (recommended for large projects)
python jira2jsonl.py --from-config > issues.jsonl
bd import -i issues.jsonl --dry-run  # Preview
bd import -i issues.jsonl             # Import
```

### Filtering Issues

```bash
# Only open issues
python jira2jsonl.py --from-config --state open

# Only closed issues
python jira2jsonl.py --from-config --state closed

# Custom JQL query
python jira2jsonl.py --url https://company.atlassian.net \
  --jql "project = PROJ AND priority = High AND status != Done"
```

### ID Generation Modes

```bash
# Sequential IDs (bd-1, bd-2, ...) - default
python jira2jsonl.py --from-config

# Hash-based IDs (bd-a3f2dd, ...) - matches bd create
python jira2jsonl.py --from-config --id-mode hash

# Custom hash length (3-8 chars)
python jira2jsonl.py --from-config --id-mode hash --hash-length 4

# Custom prefix
python jira2jsonl.py --from-config --prefix myproject
```

### From JSON File

If you have an exported JSON file:

```bash
python jira2jsonl.py --file issues.json | bd import
```

## Field Mapping

### Default Mappings

| Jira Field | bd Field | Notes |
|------------|----------|-------|
| `key` | (internal) | Used for dependency resolution |
| `summary` | `title` | Direct copy |
| `description` | `description` | Direct copy |
| `status.name` | `status` | Mapped via status_map |
| `priority.name` | `priority` | Mapped via priority_map |
| `issuetype.name` | `issue_type` | Mapped via type_map |
| `assignee` | `assignee` | Display name or username |
| `labels` | `labels` | Direct copy |
| `created` | `created_at` | ISO 8601 timestamp |
| `updated` | `updated_at` | ISO 8601 timestamp |
| `resolutiondate` | `closed_at` | ISO 8601 timestamp |
| (computed) | `external_ref` | URL to Jira issue |
| `issuelinks` | `dependencies` | Mapped to blocks/related |
| `parent` | `dependencies` | Mapped to parent-child |

### Status Mapping

Default status mappings (Jira status -> bd status):

| Jira Status | bd Status |
|-------------|-----------|
| To Do, Open, Backlog, New | `open` |
| In Progress, In Development, In Review | `in_progress` |
| Blocked, On Hold | `blocked` |
| Done, Closed, Resolved, Complete | `closed` |

Custom mappings via bd config:

```bash
bd config set jira.status_map.backlog "open"
bd config set jira.status_map.in_review "in_progress"
bd config set jira.status_map.on_hold "blocked"
```

### Priority Mapping

Default priority mappings (Jira priority -> bd priority 0-4):

| Jira Priority | bd Priority |
|---------------|-------------|
| Highest, Critical, Blocker | 0 (Critical) |
| High, Major | 1 (High) |
| Medium, Normal | 2 (Medium) |
| Low, Minor | 3 (Low) |
| Lowest, Trivial | 4 (Backlog) |

Custom mappings:

```bash
bd config set jira.priority_map.urgent "0"
bd config set jira.priority_map.nice_to_have "4"
```

### Issue Type Mapping

Default type mappings (Jira type -> bd type):

| Jira Type | bd Type |
|-----------|---------|
| Bug, Defect | `bug` |
| Story, Feature, Enhancement | `feature` |
| Task, Sub-task | `task` |
| Epic, Initiative | `epic` |
| Technical Task, Maintenance | `chore` |

Custom mappings:

```bash
bd config set jira.type_map.story "feature"
bd config set jira.type_map.spike "task"
bd config set jira.type_map.tech_debt "chore"
```

## Issue Links & Dependencies

Jira issue links are converted to bd dependencies:

| Jira Link Type | bd Dependency Type |
|----------------|-------------------|
| Blocks/Is blocked by | `blocks` |
| Parent (Epic/Story) | `parent-child` |
| All others | `related` |

**Note:** Only links to issues included in the import are preserved. Links to issues outside the query results are ignored.

## Re-syncing from Jira

Each imported issue has an `external_ref` field containing the Jira issue URL. On subsequent imports:

1. Issues are matched by `external_ref` first
2. If matched, the existing bd issue is updated (if Jira is newer)
3. If not matched, a new bd issue is created

This enables incremental sync:

```bash
# Initial import
python jira2jsonl.py --from-config | bd import

# Later: import only recent changes
python jira2jsonl.py --from-config \
  --jql "project = PROJ AND updated >= -7d" \
  | bd import
```

## Examples

### Example 1: Import Active Sprint

```bash
python jira2jsonl.py --url https://company.atlassian.net \
  --jql "project = PROJ AND sprint in openSprints()" \
  | bd import

bd ready  # See what's ready to work on
```

### Example 2: Full Project Migration

```bash
# Export all issues
python jira2jsonl.py --from-config > all-issues.jsonl

# Preview import
bd import -i all-issues.jsonl --dry-run

# Import
bd import -i all-issues.jsonl

# View stats
bd stats
```

### Example 3: Sync High Priority Bugs

```bash
python jira2jsonl.py --from-config \
  --jql "project = PROJ AND type = Bug AND priority in (Highest, High)" \
  | bd import
```

### Example 4: Import with Hash IDs

```bash
# Use hash IDs for collision-free distributed work
python jira2jsonl.py --from-config --id-mode hash | bd import
```

## Limitations

- **Single assignee**: Jira supports multiple assignees (watchers), bd supports one
- **Custom fields**: Only standard fields are mapped; custom fields are ignored
- **Attachments**: Not imported
- **Comments**: Not imported (only description)
- **Worklogs**: Not imported
- **Sprints**: Sprint metadata not preserved (use labels or JQL filtering)
- **Components/Versions**: Not mapped to bd (consider using labels)

## Troubleshooting

### "Authentication failed"

**Jira Cloud:**
- Verify you're using your email as username
- Create a fresh API token at https://id.atlassian.com/manage-profile/security/api-tokens
- Ensure the token has access to the project

**Jira Server/DC:**
- Try using a Personal Access Token instead of password
- Check that your account has permission to access the project

### "403 Forbidden"

- Check project permissions in Jira
- Verify API token has correct scopes
- Some Jira instances restrict API access by IP

### "400 Bad Request"

- Check JQL syntax
- Verify project key exists
- Check for special characters in JQL (escape with backslash)

### Rate Limits

Jira Cloud has rate limits. For large imports:
- Add delays between requests (not implemented yet)
- Import in batches using JQL date ranges
- Use the `--file` option with a manual export

## API Rate Limits

- **Jira Cloud**: ~100 requests/minute (varies by plan)
- **Jira Server/DC**: Depends on configuration

This script fetches 100 issues per request, so a 1000-issue project requires ~10 API calls.

## See Also

- [bd README](../../README.md) - Main documentation
- [GitHub Import Example](../github-import/) - Similar import for GitHub Issues
- [CONFIG.md](../../docs/CONFIG.md) - Configuration documentation
- [Jira REST API docs](https://developer.atlassian.com/cloud/jira/platform/rest/v2/)
