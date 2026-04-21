# Azure DevOps (ADO) Integration Configuration

This guide covers all configuration options for the `bd ado sync` command, which synchronizes beads issues with Azure DevOps work items.

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

² At least one project must be configured via `ado.project` or `ado.projects`.

**Config vs env var precedence:** Config keys (set via `bd config set`) take priority over environment variables.

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
| `ado.filter.iteration_path` | `--iteration-path` | Sprint/iteration path | `Project\Sprint 1` |
| `ado.filter.types` | `--types` | Work item types (comma-separated) | `Bug,Task,User Story` |
| `ado.filter.states` | `--states` | ADO states (comma-separated) | `New,Active,Resolved` |

CLI flags override config values for that sync run.

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

> **Note:** Beads P3 and P4 both map to ADO priority 4. On a fresh pull into an empty database, ADO 4 maps back to beads P3. The original priority is not preserved across a full round-trip for P4 issues.

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
Bug:                  New → Approved → Committed → Done
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

When creating a work item in a non-initial state (e.g., pushing a closed issue), beads:

1. Creates the item in the initial state (e.g., `New`)
2. Transitions through intermediate states to reach the target
3. Example: Creating a closed Bug → `New → Active → Resolved → Closed`

If a direct transition fails (ADO returns 400), beads automatically walks the known transition path for the work item type and process template.

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
| `--no-create` | Only update existing items, never create new ones |
| `--bootstrap-match` | Enable heuristic title matching for first sync |
| `--reconcile` | Force reconciliation scan for deleted items |
| `--issues` | Sync specific issues by bead ID or ADO work item ID |
| `--label` | Filter by label |
| `--status` | Filter by beads status |
| `--type` | Filter by beads issue type |

## PAT Permissions

The Personal Access Token needs these scopes:

| Scope | Access | Required For |
|---|---|---|
| Work Items | Read & Write | Creating and updating work items |

Generate a PAT at: `https://dev.azure.com/{org}/_usersettings/tokens`

## Metadata Preserved

beads stores ADO-specific metadata for round-trip fidelity:

| Metadata Key | Description |
|---|---|
| `ado.rev` | ADO revision number |
| `ado.area_path` | Area path |
| `ado.iteration_path` | Iteration/sprint path |
| `ado.story_points` | Story points estimate |
| `ado.remaining_work` | Remaining work hours |
| `ado.severity` | Bug severity value |

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
| Max retries | 3 (GET and WIQL only) |
| Retry backoff | Exponential with jitter, respects `Retry-After` header |

## Troubleshooting

### Common Errors

**"Azure DevOps PAT not configured"**
```bash
bd config set ado.pat "your-pat-here"
# or
export AZURE_DEVOPS_PAT="your-pat-here"
```

**"Azure DevOps organization not configured"**
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
