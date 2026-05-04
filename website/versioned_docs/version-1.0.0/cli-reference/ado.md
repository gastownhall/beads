---
id: ado
title: bd ado
slug: /cli-reference/ado
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc ado` (bd version 1.0.3)

## bd ado

Commands for syncing issues between beads and Azure DevOps.

Configuration can be set via 'bd config' or environment variables:
  ado.org / AZURE_DEVOPS_ORG              - Organization name
  ado.project / AZURE_DEVOPS_PROJECT      - Project name (single)
  ado.projects / AZURE_DEVOPS_PROJECTS    - Project names (comma-separated)
  ado.pat / AZURE_DEVOPS_PAT              - Personal access token
  ado.url / AZURE_DEVOPS_URL              - Custom base URL (on-prem)

```
bd ado
```

### bd ado projects

List Azure DevOps projects that the configured token has access to.

```
bd ado projects
```

### bd ado pull

Pull one or more items from Azure DevOps.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd ado sync --pull-only --issues &lt;refs&gt;

```
bd ado pull [refs...] [flags]
```

**Flags:**

```
      --dry-run   Preview pull without making changes
```

### bd ado push

Push one or more beads issues to Azure DevOps.

Accepts bead IDs as positional arguments.
Equivalent to: bd ado sync --push-only --issues &lt;ids&gt;

```
bd ado push [bead-ids...] [flags]
```

**Flags:**

```
      --dry-run   Preview push without making changes
```

### bd ado status

Display current Azure DevOps configuration and sync status.

```
bd ado status
```

### bd ado sync

Synchronize issues between beads and Azure DevOps.

By default, performs bidirectional sync:
- Pulls new/updated work items from Azure DevOps to beads
- Pushes local beads issues to Azure DevOps

Use --pull-only or --push-only to limit direction.

Filters (--area-path, --iteration-path, --types, --states) restrict
which work items are synced. On pull, they limit the WIQL query. On push,
--types and --states filter local beads before pushing to ADO. Use
--no-create with push to skip creating new ADO work items (only update
existing linked items). Filters can also be persisted via config:
  ado.filter.area_path, ado.filter.iteration_path,
  ado.filter.types, ado.filter.states
CLI flags override config values when both are set.

```
bd ado sync [flags]
```

**Flags:**

```
      --area-path string        Filter to ADO area path (e.g., "Project\Team")
      --bootstrap-match         Enable heuristic matching for first sync
      --dry-run                 Show what would be synced without making changes
      --issues string           Comma-separated bead IDs to sync selectively (e.g., bd-abc,bd-def). Mutually exclusive with --parent.
      --iteration-path string   Filter to ADO iteration path (e.g., "Project\Sprint 1")
      --no-create               Never create new items in either direction (pull or push)
      --parent string           Limit push to this bead and its descendants (push only). Mutually exclusive with --issues.
      --prefer-ado              On conflict, use Azure DevOps version
      --prefer-local            On conflict, keep local beads version
      --prefer-newer            On conflict, use most recent version (default)
      --project strings         Project name(s) to sync (overrides configured project/projects)
      --pull-only               Only pull issues from Azure DevOps
      --push-only               Only push issues to Azure DevOps
      --reconcile               Force reconciliation scan for deleted items
      --states string           Filter to ADO states, comma-separated (e.g., "New,Active,Resolved")
      --types string            Filter to work item types, comma-separated (e.g., "Bug,Task,User Story")
```
