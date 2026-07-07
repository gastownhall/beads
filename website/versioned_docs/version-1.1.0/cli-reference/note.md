---
id: note
title: bd note
slug: /cli-reference/note
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc note`

## bd note

Append a note to an issue's notes field.

Shorthand for 'bd update &lt;id&gt; --append-notes "text"'.

Examples:
  bd note gt-abc "Fixed the flaky test"
  bd note gt-abc Fixed the flaky test
  echo "note from pipe" | bd note gt-abc --stdin
  bd note gt-abc --file notes.txt

```
bd note <id> [text...] [flags]
```

**Flags:**

```
      --file string   Read note text from file
      --stdin         Read note text from stdin
```
