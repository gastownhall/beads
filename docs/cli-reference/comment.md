---
title: "bd comment"
description: "Add a comment to an issue"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc comment`.

Add a comment to an issue.

Shorthand for 'bd comments add &lt;id&gt; "text"'.

Examples:
  bd comment bd-123 "Working on this now"
  bd comment bd-123 Working on this now
  echo "comment from pipe" | bd comment bd-123 --stdin
  bd comment bd-123 --file notes.txt

```
bd comment <id> [text...] [flags]
```

**Flags:**

```
      --file string   Read comment text from file
      --stdin         Read comment text from stdin
```
