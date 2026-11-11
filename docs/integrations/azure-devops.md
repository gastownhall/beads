---
title: Azure DevOps (ADO) Integration Configuration
description: Configuration reference for bd ado sync, which bidirectionally syncs beads issues with Azure DevOps work items
---

Last reviewed: 2026-11-07

Freshness source: `cmd/bd/ado*.go` and `internal/ado/`.

This guide covers all configuration options for the `bd ado sync` command, which synchronizes beads issues with Azure DevOps work items.

**Proxied-server mode:** `bd ado sync`, `bd ado status`, and `bd ado projects`
are not supported when bd is connected to a proxied server (error:
`ado <command> is not supported in proxied-server mode`); run them from a
workspace with direct database access.

## Quick Start

```bash
# Set required config
bd config set ado.pat "your-personal-access-token"
bd config set ado.org "your-organization"
bd config set ado.project "your-project"

# Or use environment variables
export AZURE_DEVOPS_PAT="your-personal-access-token"
export AZURE_DEVOPS_ORG="your-organization"
export AZURE_DEVOPS_PROJECT="your-project"

# Sync (bidirectional)
bd ado sync

# Pull only (import from ADO)
bd ado sync --pull-only

# Push only (export to ADO)
bd ado sync --push-only

# Preview without making changes
bd ado sync --dry-run
```

## Connection Configuration

| Config Key | Env Variable | Required | Description |
|---|---|---|---|
| `ado.pat` | `AZURE_DEVOPS_PAT` | Yes | Personal Access Token |
| `ado.org` | `AZURE_DEVOPS_ORG` | Conditional¹ | Organization name (e.g., `myorg`) |
| `ado.url` | `AZURE_DEVOPS_URL` | Conditional¹ | Custom base URL (on-prem ADO Server) |
| `ado.project` | `AZURE_DEVOPS_PROJECT` | Conditional² | Single project name |
| `ado.projects` | `AZURE_DEVOPS_PROJECTS` | Conditional² | Comma-separated project names |

¹ Either `ado.org` or `ado.url` must be set. Use `ado.url` for on-premises Azure DevOps Server.

² At least one project must be configured via `ado.project` or `ado.projects`. When both are set, `ado.projects` wins.

**Config vs env var precedence:** Config keys (set via `bd config set`) take priority over environment variables.

`ado.url` must use `https://`; plain `http://` is accepted only for
`localhost`, `127.0.0.1` and `::1` (error: `HTTPS required for ADO connections`).

`ado.pat` is a secret key stored in `config.yaml` (per-repo or user-global),
never the shared database, so the PAT cannot leak via `dolt push`.

### On-Premises ADO Server

For Azure DevOps Server (on-prem), use `ado.url` instead of `ado.org`:

```bash
bd config set ado.url "https://tfs.company.com/DefaultCollection"
bd config set ado.project "MyProject"
```

### Multi-Project Sync

Sync across multiple projects in a single command:

```bash
bd config set ado.projects "ProjectA,ProjectB,ProjectC"
```

The first project is used as the primary for URL construction. WIQL queries use `TeamProject IN (...)` for multi-project support.

## Filter Configuration

Filters control which ADO work items are included in sync operations.

| Config Key | CLI Flag | Description | Example |
|---|---|---|---|
| `ado.filter.area_path` | `--area-path` | Area path (uses UNDER) | `Project\Team` |
| `ado.filter.iteration_path` | `--iteration-path` | Sprint/iteration path (uses UNDER) | `Project\Sprint 1` |
| `ado.filter.types` | `--types` | Work item types (comma-separated) | `Bug,Task,User Story` |
| `ado.filter.states` | `--states` | ADO states (comma-separated) | `New,Active,Resolved` |

CLI flags override config values for that sync run. On pull, the filters
narrow the WIQL query; on push, the type and state filters (from the flags or
from config) also filter which local beads are pushed.

**WIQL query example** (generated from filters):

```sql
SELECT [System.Id] FROM WorkItems WHERE
  [System.TeamProject] = 'MyProject'
  AND [System.IsDeleted] = false
  AND [System.AreaPath] UNDER 'Project\Team'
  AND [System.WorkItemType] IN ('Bug', 'Task')
  AND [System.State] IN ('New', 'Active')
  ORDER BY [System.ChangedDate] ASC
```

An incremental pull also adds `[System.ChangedDate] >= 'YYYY-MM-DD'`. WIQL
compares that field with a date-only literal, so the window is the whole UTC
day of the last sync, and items changed earlier that day are fetched again.

## Default Mappings

### Priority Mapping

Priority mapping is bidirectional but **lossy for P3/P4**:

| Beads Priority | ADO Priority | Direction | Notes |
|---|---|---|---|
| 0 (Critical) | 1 | ↔ | |
| 1 (High) | 2 | ↔ | |
| 2 (Medium) | 3 | ↔ | Default for unknown values |
| 3 (Low) | 4 | → | |
| 4 (Backlog) | 4 | → | **Lossy**: becomes P3 on pull |

> **Note:** Beads P3 and P4 both map to ADO priority 4, and a pull always maps ADO 4 back to beads P3, even in the database that pushed it. Nothing stores the original priority, so a P4 bead that has been pushed becomes P3 the next time a pull reads its work item back. That is usually the first pull after the push, even when nothing changed in ADO.

For Bug-type work items, ADO also requires a Severity field:

| Beads Priority | ADO Severity |
|---|---|
| 0 | 1 - Critical |
| 1 | 2 - High |
| 2 | 3 - Medium |
| 3, 4 | 4 - Low |

### Status Mapping

| Beads Status | Default ADO State | Config Key |
|---|---|---|
| `open` | `New` | `ado.state_map.open` |
| `in_progress` | `Active` | `ado.state_map.in_progress` |
| `blocked` | `Active` + `beads:blocked` tag | `ado.state_map.blocked` |
| `deferred` | `Removed` | `ado.state_map.deferred` |
| `closed` | `Closed` | `ado.state_map.closed` |

On pull, the default reverse mapping also maps `Resolved` → `closed`; unknown
states map to `open`. A custom `ado.state_map.*` value is matched
case-insensitively before the defaults.

**Blocked status:** ADO has no native blocked state. beads maps blocked to `Active` and adds a `beads:blocked` tag. On pull, `Active` + `beads:blocked` tag restores `StatusBlocked`.

Override defaults for your process template:

```bash
# Example: Scrum template
bd config set ado.state_map.open "To Do"
bd config set ado.state_map.in_progress "In Progress"
bd config set ado.state_map.closed "Done"
```

### Type Mapping

| Beads Type | Default ADO Type | Config Key |
|---|---|---|
| `bug` | `Bug` | `ado.type_map.bug` |
| `feature` | `User Story` | `ado.type_map.feature` |
| `task` | `Task` | `ado.type_map.task` |
| `epic` | `Epic` | `ado.type_map.epic` |
| `chore` | `Task` | `ado.type_map.chore` |

Reverse mapping (ADO → beads) also recognizes:
- `Product Backlog Item` → `feature` (Scrum template)
- `Issue` → `task`

A custom `ado.type_map.*` value is matched case-insensitively first; the
defaults still apply to the types it does not name, and any other ADO type
maps to `task`.

Override for your process template:

```bash
# Example: Scrum template
bd config set ado.type_map.feature "Product Backlog Item"
```

## Process Template Configuration

ADO supports multiple process templates with different work item types and state transitions. The defaults assume the **Agile** template. Override mappings for other templates.

### Agile (Default)

No configuration needed. Default mappings work out of the box.

State transitions:
```
Bug:         New → Active → Resolved → Closed
Task:        New → Active → Closed
User Story:  New → Active → Resolved → Closed
Epic:        New → Active → Resolved → Closed
```

### Scrum

```bash
bd config set ado.type_map.feature "Product Backlog Item"
bd config set ado.state_map.open "New"
bd config set ado.state_map.in_progress "Committed"
bd config set ado.state_map.closed "Done"
```

State transitions:
```
Product Backlog Item: New → Approved → Committed → Done
Task:                 To Do → In Progress → Done
```

### CMMI

```bash
bd config set ado.type_map.feature "Requirement"
bd config set ado.state_map.open "Proposed"
bd config set ado.state_map.in_progress "Active"
bd config set ado.state_map.closed "Closed"
```

State transitions:
```
Requirement: Proposed → Active → Resolved → Closed
Task:        Proposed → Active → Closed
Bug:         Proposed → Active → Resolved → Closed
```

### State Transition Handling

When creating a work item whose target state is not an initial state
(`New`, `To Do` or `Proposed`; e.g., pushing a closed issue), beads:

1. Creates the item without a state, so ADO puts it in the type's initial state
2. Tries a direct update to the target state
3. If ADO rejects that with a 400, walks the known transition path for the work item type
4. Example: Creating a closed Bug → `New → Active → Resolved → Closed`

The known paths are the Agile ones listed above (plus `Feature` and `Issue`),
Scrum `Product Backlog Item` from `New`, `Task` from `To Do` (via
`In Progress`), `Issue` and `Epic` from `To Do` (via `Doing`), and CMMI `Bug`,
`Task` and `Requirement` from `Proposed`. There is no known path for other
type/state pairs (for example a Scrum `Bug`); a rejected direct update then
fails with `no known transition path`. Updates to existing work items set the
state directly.

When the transition fails, the new work item stays short of its target state
(in its initial state when no path is known), and the push reports an error
that names the work item but does not link the bead to it. Each later push of
the bead creates another such work item, and a pull can import each one as a
new bead. To clean up, delete the extra work items in ADO and any beads a pull
created for them, and keep the bead out of the push until its status maps
(`ado.state_map.*`) to a state ADO can reach from the item's initial state.

## Sync Options

### Direction

| Flag | Description |
|---|---|
| (none) | Bidirectional: pull then push |
| `--pull-only` | Import from ADO only |
| `--push-only` | Export to ADO only |

### Conflict Resolution

When the same issue has been modified both locally and in ADO:

| Flag | Description |
|---|---|
| `--prefer-newer` | Most recently updated version wins (default) |
| `--prefer-local` | Local beads version always wins |
| `--prefer-ado` | ADO version always wins |

### Additional Flags

| Flag | Description |
|---|---|
| `--dry-run` | Preview sync without making changes |
| `--no-create` | Never create new items in either direction (pull or push); only update linked ones |
| `--bootstrap-match` | Enable heuristic title matching for first sync |
| `--reconcile` | Force reconciliation scan for deleted items (see Reconciliation below) |
| `--issues` | Sync specific issues by bead ID or ADO work item ID |
| `--parent` | Push only a bead and its descendants (push mode only; mutually exclusive with `--issues`) |
| `--area-path` | Filter by area path (see Filter Configuration) |
| `--iteration-path` | Filter by iteration path (see Filter Configuration) |
| `--project` | Project name(s) for this run, repeatable — overrides `ado.project`/`ado.projects` |
| `--states` | Filter by work item states (comma-separated) |
| `--types` | Filter by work item types (comma-separated) |

`--issues` and `--parent` narrow the work items and beads synced, but not the
dependency-to-relation pass that runs after a push: it still writes ADO
relations for every linked bead.

### Reconciliation

A reconciliation scan re-checks work items beads already tracks, so items deleted
in ADO (404) or no longer readable (403) are detected instead of lingering. It
does not run on every sync — that would cost an API call per tracked item — so it
runs periodically, and `--reconcile` forces it immediately.

| Config Key | Default | Description |
|---|---|---|
| `ado.reconcile_interval` | `10` | Number of syncs between automatic reconciliation scans |

`ado.syncs_since_reconcile` also appears in config: it is the counter beads keeps
to know when the next scan is due, not a setting to edit.

## PAT Permissions

The Personal Access Token needs these scopes:

| Scope | Access | Required For |
|---|---|---|
| Work Items | Read & Write | Creating and updating work items |
| Project and Team | Read | `bd ado projects` |

Generate a PAT at: `https://dev.azure.com/{org}/_usersettings/tokens`

## Metadata Preserved

A pull stores this ADO metadata on each bead it creates or updates, for
round-trip fidelity:

| Metadata Key | Description |
|---|---|
| `ado.rev` | ADO revision number |
| `ado.area_path` | Area path |
| `ado.iteration_path` | Iteration/sprint path |
| `ado.story_points` | Story points estimate |

## Description Conversion

- **Push (beads → ADO):** Markdown converted to HTML
- **Pull (ADO → beads):** HTML converted to Markdown

## Tags and Labels

- ADO tags are semicolon-separated; beads labels use arrays
- User labels round-trip through ADO tags
- Internal `beads:*` tags (e.g., `beads:blocked`) are filtered on pull — they don't appear as user labels

## API Limits

| Limit | Value |
|---|---|
| Max batch size | 200 work items per GET request |
| Max response size | 50 MB |
| Request timeout | 30 seconds |
| Max retries | 3 (GET and WIQL only), on network errors, 429 and 5xx |
| Retry backoff | Exponential from 1s with jitter; a numeric `Retry-After` header replaces it |

400, 401, 403 and 404 responses are never retried.

## Troubleshooting

### Common Errors

**`ado.pat not configured: set via 'bd config set ado.pat <token>' or AZURE_DEVOPS_PAT env var`**
```bash
bd config set ado.pat "your-pat-here"
# or
export AZURE_DEVOPS_PAT="your-pat-here"
```

**`ado.org not configured: set via 'bd config set ado.org <org>' or AZURE_DEVOPS_ORG env var`**
```bash
bd config set ado.org "your-org"
# or for on-prem:
bd config set ado.url "https://tfs.company.com/DefaultCollection"
```

**State transition errors (400 Bad Request)**
This usually means the process template doesn't support a direct state change. Check your `ado.state_map.*` config matches your actual process template.

**Type not found errors**
Verify your `ado.type_map.*` config matches the work item types available in your project. Use `--types` filter to restrict which types are synced.

### Debugging

```bash
# Preview what would happen
bd ado sync --dry-run

# Check current config
bd config get ado.pat
bd config get ado.org
bd config get ado.project
```
