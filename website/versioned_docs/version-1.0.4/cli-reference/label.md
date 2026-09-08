---
id: label
title: bd label
slug: /cli-reference/label
sidebar_position: 110
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc label`

## bd label

Manage issue labels

```
bd label
```

### bd label add

Add a label to one or more issues

```
bd label add [issue-id...] [label]
```

### bd label list

List labels for an issue

```
bd label list [issue-id]
```

### bd label list-all

List all unique labels in the database

```
bd label list-all
```

### bd label propagate

Push a label from a parent down to all direct children that don't already have it. Useful for applying branch: labels across an epic's subtasks.

```
bd label propagate [parent-id] [label]
```

### bd label remove

Remove a label from one or more issues

```
bd label remove [issue-id...] [label]
```
