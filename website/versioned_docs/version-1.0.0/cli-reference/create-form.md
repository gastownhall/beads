---
id: create-form
title: bd create-form
slug: /cli-reference/create-form
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc create-form`

## bd create-form

Create a new issue using an interactive terminal form.

This command provides a user-friendly form interface for creating issues,
with fields for title, description, type, priority, labels, and more.

Use --parent to create a sub-issue under an existing parent issue.
The child will get an auto-generated hierarchical ID (e.g., parent-id.1).

The form uses keyboard navigation:
  - Tab/Shift+Tab: Move between fields
  - Enter: Submit the form (on the last field or submit button)
  - Ctrl+C: Cancel and exit
  - Arrow keys: Navigate within select fields

```
bd create-form [flags]
```

**Flags:**

```
      --parent string   Parent issue ID for creating a hierarchical child (e.g., 'bd-a3f8e9')
```
