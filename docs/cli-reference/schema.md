---
title: "bd schema"
description: "Print the JSON Schema for bd's --json / export output"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc schema`.

Print the JSON Schema for bd's canonical output record types.

The schema is reflected from the same Go structs bd serializes, so it stays in
lockstep with the actual --json / export output. Use it to generate typed
consumer models (datamodel-code-generator, quicktype, ...) rather than
hand-maintaining them.

  bd schema | jq '.types.issue'        # the issue record schema
  bd schema | jq '.types.dependency'   # the dependency record schema

```
bd schema [flags]
```
