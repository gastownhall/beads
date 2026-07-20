---
title: "bd ping"
description: "Lightweight health check that confirms bd can reach its database."
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc ping`.

Lightweight health check that confirms bd can reach its database.

Steps:
  1. Resolve the .beads workspace
  2. Open the store (embedded or server)
  3. Run a trivial query (issue count)
  4. Report timing

Exit 0 on success, exit 1 on failure.

Examples:
  bd ping              # Quick connectivity check
  bd ping --json       # Structured output for automation

```
bd ping [flags]
```
