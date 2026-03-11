---
description: Import issues from JSONL format (removed)
argument-hint: (removed)
---

`bd import` has been **removed**.

## Migration

If you need to import issues from a JSONL file, use `bd init` with the `--from-jsonl` flag:

```bash
bd init <prefix> --from-jsonl issues.jsonl
```

## Note

Dolt is the primary storage backend. Manual JSONL import is no longer supported as a standalone command.
