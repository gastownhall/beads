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

```
bd label remove [issue-id...] [label[,label...]] [flags]
```
