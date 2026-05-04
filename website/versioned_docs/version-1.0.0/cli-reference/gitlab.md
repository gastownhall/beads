---
id: gitlab
title: bd gitlab
slug: /cli-reference/gitlab
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc gitlab` (bd version 1.0.3)

## bd gitlab

Commands for syncing issues between beads and GitLab.

Configuration can be set via 'bd config' or environment variables:
  gitlab.url / GITLAB_URL                         - GitLab instance URL
  gitlab.token / GITLAB_TOKEN                     - Personal access token
  gitlab.project_id / GITLAB_PROJECT_ID           - Project ID or path
  gitlab.group_id / GITLAB_GROUP_ID               - Group ID for group-level sync
  gitlab.default_project_id / GITLAB_DEFAULT_PROJECT_ID - Project for creating issues in group mode

```
bd gitlab
```

### bd gitlab projects

List GitLab projects that the configured token has access to.

```
bd gitlab projects
```

### bd gitlab pull

Pull one or more items from GitLab.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd gitlab sync --pull-only --issues &lt;refs&gt;

```
bd gitlab pull [refs...] [flags]
```

**Flags:**

```
      --dry-run   Preview pull without making changes
```

### bd gitlab push

Push one or more beads issues to GitLab.

Accepts bead IDs as positional arguments.
Equivalent to: bd gitlab sync --push-only --issues &lt;ids&gt;

```
bd gitlab push [bead-ids...] [flags]
```

**Flags:**

```
      --dry-run   Preview push without making changes
```

### bd gitlab status

Display current GitLab configuration and sync status.

```
bd gitlab status
```

### bd gitlab sync

Synchronize issues between beads and GitLab.

By default, performs bidirectional sync:
- Pulls new/updated issues from GitLab to beads
- Pushes local beads issues to GitLab

Use --pull-only or --push-only to limit direction.

```
bd gitlab sync [flags]
```

**Flags:**

```
      --assignee string       Filter by assignee username
      --dry-run               Show what would be synced without making changes
      --exclude-type string   Exclude these issue types from sync (comma-separated)
      --issues string         Comma-separated bead IDs to sync selectively (e.g., bd-abc,bd-def). Mutually exclusive with --parent.
      --label string          Filter by labels (comma-separated, AND logic)
      --milestone string      Filter by milestone title
      --no-ephemeral          Exclude ephemeral/wisp issues from push (default: true) (default true)
      --parent string         Limit push to this bead and its descendants (push only). Mutually exclusive with --issues.
      --prefer-gitlab         On conflict, use GitLab version
      --prefer-local          On conflict, keep local beads version
      --prefer-newer          On conflict, use most recent version (default)
      --project string        Filter to issues from this project ID (group mode)
      --pull-only             Only pull issues from GitLab
      --push-only             Only push issues to GitLab
      --type string           Only sync these issue types (comma-separated, e.g. 'epic,feature,task')
```
