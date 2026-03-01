---
description: Synchronize issues (deprecated — use bd dolt push/pull)
argument-hint: (deprecated)
---

`bd sync` is **deprecated** and is now a no-op.

## Use Dolt commands instead

- **Push to remote**: `bd dolt push`
- **Pull from remote**: `bd dolt pull`
- **Commit pending changes**: `bd dolt commit`
- **Check connection**: `bd dolt show`

## Note

Most users should rely on the Dolt server's automatic sync (with `dolt.auto-commit` enabled) instead of running manual sync commands.
