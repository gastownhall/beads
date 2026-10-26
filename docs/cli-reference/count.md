---
title: "bd count"
description: "Count issues matching filters"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc count`.

Count issues matching the specified filters.

By default, returns the total count of issues matching the filters.
Use --by-* flags to group counts by different attributes.

Examples:
  bd count                          # Count all issues
  bd count --status open            # Count open issues
  bd count --by-status              # Group count by status
  bd count --by-priority            # Group count by priority
  bd count --by-type                # Group count by issue type
  bd count --by-assignee            # Group count by assignee
  bd count --by-label               # Group count by label
  bd count --assignee alice --by-status  # Count alice's issues by status
  bd count --include-infra          # Count issues + wisps tier (matches 'bd list --include-infra --all' cardinality)
  bd count --type task --include-ephemeral  # Count tasks in the durable AND wisps tiers


```
bd count [flags]
```

**Flags:**

```
  -a, --assignee string              Filter by assignee
      --by-assignee                  Group count by assignee
      --by-label                     Group count by label
      --by-priority                  Group count by priority
      --by-status                    Group count by status
      --by-type                      Group count by issue type
      --closed-after string          Filter issues closed after date (YYYY-MM-DD or RFC3339)
      --closed-before string         Filter issues closed before date (YYYY-MM-DD or RFC3339)
      --created-after string         Filter issues created after date (YYYY-MM-DD or RFC3339)
      --created-before string        Filter issues created before date (YYYY-MM-DD or RFC3339)
      --desc-contains string         Filter by description substring
      --empty-description            Filter issues with empty description
      --exclude-status strings       Exclude statuses from the count (comma-separated or repeatable)
      --exclude-type strings         Exclude issue types from the count (comma-separated or repeatable)
      --has-metadata-key string      Filter issues that have this metadata key set
      --id string                    Filter by specific issue IDs (comma-separated)
      --include-ephemeral            Include ephemeral wisp-plane rows in the count (normally hidden), without lifting type exclusions
      --include-infra                Include infrastructure beads and the wisps tier (matches 'bd list --include-infra --all' cardinality)
  -l, --label strings                Filter by labels (AND: must have ALL)
      --label-any strings            Filter by labels (OR: must have AT LEAST ONE)
      --metadata-field stringArray   Filter by metadata field (key=value, repeatable)
      --no-assignee                  Filter issues with no assignee
      --no-labels                    Filter issues with no labels
      --no-parent                    Count only top-level issues (exclude child issues)
      --notes-contains string        Filter by notes substring
      --parent string                Filter by parent issue ID (count children of specified issue)
  -p, --priority int                 Filter by priority (0-4: 0=critical, 1=high, 2=medium, 3=low, 4=backlog)
      --priority-max int             Filter by maximum priority (inclusive)
      --priority-min int             Filter by minimum priority (inclusive)
  -s, --status string                Filter by stored status (open, in_progress, blocked, deferred, closed). Note: dependency-blocked issues use 'bd blocked'
      --title string                 Filter by title text (case-insensitive substring match)
      --title-contains string        Filter by title substring
  -t, --type string                  Filter by type (bug, feature, task, epic, chore, decision, merge-request, molecule, gate)
      --updated-after string         Filter issues updated after date (YYYY-MM-DD or RFC3339)
      --updated-before string        Filter issues updated before date (YYYY-MM-DD or RFC3339)
```
