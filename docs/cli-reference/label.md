---
title: "bd label"
description: "Manage issue labels"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc label`.

Manage issue labels

```
bd label [command]
```

## bd label add

Add labels to issues. Issue IDs come first; the final argument is the label. Pass multiple labels comma-separated: bd label add bd-123 label1,label2

```
bd label add [issue-id...] [label[,label...]] [flags]
```

## bd label list

List labels for an issue

```
bd label list [issue-id] [flags]
```

## bd label list-all

List all unique labels in the database

```
bd label list-all [flags]
```

## bd label propagate

Push a label from a parent down to all direct children that don't already have it. Useful for applying branch: labels across an epic's subtasks.

```
bd label propagate [parent-id] [label] [flags]
```

## bd label remove

Remove labels from issues. Issue IDs come first; the final argument is the label. Pass multiple labels comma-separated: bd label remove bd-123 label1,label2

With --prefix, no label argument is needed: every label on the given issue(s) starting with the prefix is removed, e.g. bd label remove bd-123 --prefix pool:refused:

```
bd label remove [issue-id...] [label[,label...]] [flags]
```

**Flags:**

```
      --prefix bd label list <issue-id>   Remove every label matching this prefix instead of an exact label (e.g., 'pool:refused:' removes all pool:refused:* labels). No label positional argument is needed when set. CAUTION: a short prefix removes every match on an issue in a single edit, including labels you may not have meant to touch (e.g. --prefix t removes both 'tier:opus' and 'test-needed') — check bd label list <issue-id> first if unsure. State-dimension labels (e.g. 'launch:', 'scalegate:') should move via `bd set-state` instead, which this flag bypasses without its event-bead audit trail.
```

## bd label rename

Rename a label everywhere it appears. An issue that already carries the new label keeps it and drops the old one instead of erroring - a merge, reported honestly (the write path measures merged from what the insert actually affected, never from a snapshot check, so its count is correct under concurrent writes). Pass --dry-run to preview the blast radius (a count and the first issues) without writing anything; the preview's merged count is a plain snapshot intersection of two separate reads, so treat it as an estimate, not the number the rename itself will report.

```
bd label rename <old-label> <new-label> [flags]
```

**Flags:**

```
      --dry-run   Preview the blast radius without renaming anything (merged count is a snapshot intersection, not authoritative - see --help)
```
