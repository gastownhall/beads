---
id: jira
title: bd jira
slug: /cli-reference/jira
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc jira`

## bd jira

Synchronize issues between beads and Jira.

Configuration:
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set jira.projects "PROJ1,PROJ2"   # Multiple projects
  bd config set jira.api_token "YOUR_TOKEN"
  bd config set jira.username "your_email@company.com"  # For Jira Cloud
  bd config set jira.push_prefix "hippo"       # Only push hippo-* issues to Jira
  bd config set jira.push_prefix "proj1,proj2" # Multiple prefixes (comma-separated)

Environment variables (alternative to config):
  JIRA_API_TOKEN  - Jira API token
  JIRA_USERNAME   - Jira username/email
  JIRA_PROJECTS   - Comma-separated project keys

Examples:
  bd jira sync --pull         # Import issues from Jira
  bd jira sync --push         # Export issues to Jira
  bd jira sync                # Bidirectional sync (pull then push)
  bd jira sync --dry-run      # Preview sync without changes
  bd jira status              # Show sync status

```
bd jira
```

### bd jira pull

Pull one or more items from Jira.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd jira sync --pull --issues &lt;refs&gt;

```
bd jira pull [refs...] [flags]
```

**Flags:**

```
      --dry-run   Preview pull without making changes
```

### bd jira push

Push one or more beads issues to Jira.

Accepts bead IDs as positional arguments.
Equivalent to: bd jira sync --push --issues &lt;ids&gt;

```
bd jira push [bead-ids...] [flags]
```

**Flags:**

```
      --dry-run   Preview push without making changes
```

### bd jira status

Show the current Jira sync status, including:
  - Last sync timestamp
  - Configuration status
  - Number of issues with Jira links
  - Issues pending push (no external_ref)

```
bd jira status
```

### bd jira sync

Synchronize issues between beads and Jira.

Modes:
  --pull         Import issues from Jira into beads
  --push         Export issues from beads to Jira
  (no flags)     Bidirectional sync: pull then push, with conflict resolution

Conflict Resolution:
  By default, newer timestamp wins. Override with:
  --prefer-local   Always prefer local beads version
  --prefer-jira    Always prefer Jira version

Examples:
  bd jira sync --pull                # Import from Jira
  bd jira sync --push --create-only  # Push new issues only
  bd jira sync --dry-run             # Preview without changes
  bd jira sync --prefer-local        # Bidirectional, local wins

```
bd jira sync [flags]
```

**Flags:**

```
      --create-only       Only create new issues, don't update existing
      --dry-run           Preview sync without making changes
      --issues string     Comma-separated bead IDs to sync selectively (e.g., bd-abc,bd-def). Mutually exclusive with --parent.
      --parent string     Limit push to this bead and its descendants (push only). Mutually exclusive with --issues.
      --prefer-jira       Prefer Jira version on conflicts
      --prefer-local      Prefer local version on conflicts
      --project strings   Project key(s) to sync (overrides configured project/projects)
      --pull              Pull issues from Jira
      --push              Push issues to Jira
      --state string      Issue state to sync: open, closed, all (default "all")
```
