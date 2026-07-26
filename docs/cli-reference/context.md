---
title: "bd context"
description: "Show the effective backend identity information including repository paths,"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc context`.

Show the effective backend identity information including repository paths,
backend configuration, and sync settings.

This command reads directly from config files and does not require the
database to be open, making it useful for diagnostics in degraded states.

Examples:
  bd context           # Show context information
  bd context --json    # Output in JSON format


```
bd context [flags]
```
