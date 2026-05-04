---
id: edit
title: bd edit
slug: /cli-reference/edit
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc edit` (bd version 1.0.3)

## bd edit

Edit an issue field using your configured $EDITOR.

By default, edits the description. Use flags to edit other fields.

Examples:
  bd edit bd-42                    # Edit description
  bd edit bd-42 --title            # Edit title
  bd edit bd-42 --design           # Edit design notes
  bd edit bd-42 --notes            # Edit notes
  bd edit bd-42 --acceptance       # Edit acceptance criteria

```
bd edit [id] [flags]
```

**Flags:**

```
      --acceptance    Edit the acceptance criteria
      --description   Edit the description (default)
      --design        Edit the design notes
      --notes         Edit the notes
      --title         Edit the title
```
