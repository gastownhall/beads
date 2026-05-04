---
id: notion
title: bd notion
slug: /cli-reference/notion
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc notion` (bd version 1.0.3)

## bd notion

Commands for syncing issues between beads and Notion.

```
bd notion
```

### bd notion connect

Connect bd to an existing Notion database or data source

```
bd notion connect [flags]
```

**Flags:**

```
      --url string   Existing Notion database or data source URL
```

### bd notion init

Create a dedicated Beads database in Notion

```
bd notion init [flags]
```

**Flags:**

```
      --parent string   Parent page ID
      --title string    Database title (default "Beads Issues")
```

### bd notion pull

Pull one or more items from Notion.

Accepts bead IDs or external references as positional arguments.
Equivalent to: bd notion sync --pull --issues &lt;refs&gt;

```
bd notion pull [refs...] [flags]
```

**Flags:**

```
      --dry-run   Preview pull without making changes
```

### bd notion push

Push one or more beads issues to Notion.

Accepts bead IDs as positional arguments.
Equivalent to: bd notion sync --push --issues &lt;ids&gt;

```
bd notion push [bead-ids...] [flags]
```

**Flags:**

```
      --dry-run   Preview push without making changes
```

### bd notion status

Show Notion sync status

```
bd notion status
```

### bd notion sync

Synchronize issues between beads and Notion.

By default this performs bidirectional sync. Use --pull or --push to limit direction.

```
bd notion sync [flags]
```

**Flags:**

```
      --create-only     Only create missing remote pages, do not update existing ones
      --dry-run         Preview changes without making mutations
      --issues string   Comma-separated bead IDs to sync selectively (e.g., bd-abc,bd-def). Mutually exclusive with --parent.
      --parent string   Limit push to this bead and its descendants (push only). Mutually exclusive with --issues.
      --prefer-local    On conflict, keep the local beads version
      --prefer-notion   On conflict, use the Notion version
      --pull            Only pull issues from Notion
      --push            Only push issues to Notion
      --state string    Issue state to sync: open, closed, or all (default "all")
```
