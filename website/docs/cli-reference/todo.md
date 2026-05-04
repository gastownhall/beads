---
id: todo
title: bd todo
slug: /cli-reference/todo
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc todo` (bd version 1.0.3)

## bd todo

Manage TODO items as lightweight task issues.

TODOs are regular task-type issues with convenient shortcuts:
  bd todo add "Title"    -&gt; bd create "Title" -t task -p 2
  bd todo                -&gt; bd list --type task --status open
  bd todo done &lt;id&gt;      -&gt; bd close &lt;id&gt;

TODOs can be promoted to full issues by changing type or priority:
  bd update todo-123 --type bug --priority 0

```
bd todo
```

### bd todo add

Add a new TODO item

```
bd todo add <title> [flags]
```

**Flags:**

```
  -d, --description string   Description
  -p, --priority int         Priority (0-4, default 2) (default 2)
```

### bd todo done

Mark TODO(s) as done

```
bd todo done <id> [<id>...] [flags]
```

**Flags:**

```
      --reason string   Reason for closing (default: Completed)
```

### bd todo list

List TODO items

```
bd todo list [flags]
```

**Flags:**

```
      --all   Show all TODOs including completed
```
