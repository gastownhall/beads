---
description: Synchronize issues with a configured Dolt remote
argument-hint: ""
---

The top-level `bd sync` command has been removed. Running it returns an
unknown-command error; it is not a compatibility no-op. Nested commands such
as `bd backup sync` and tracker-specific sync commands are separate commands
and remain supported.

## Supported workflow

```bash
# Before starting work or consuming changes from another clone
bd dolt pull

# After local issue updates and before handoff: commit, integrate, then publish
bd dolt commit
bd dolt pull
bd dolt push
```

`bd dolt commit` creates an explicit commit boundary when auto-commit is off or
in batch mode; it is a safe no-op when there is nothing pending. The following
pull integrates remote changes before the final push. The pull steps require a
configured remote; use the one-time setup below first when needed.

Inspect the configured Dolt remotes with:

```bash
bd dolt remote list
```

If no Dolt remote exists, the project has a Git origin, and remote sync is
enabled, `bd dolt push` automatically adopts that origin; do not add the
matching URL manually. To use a distinct custom Dolt remote, first confirm that
the chosen name is absent, then add it explicitly:

```bash
bd dolt remote add origin <distinct-dolt-remote-url>
```

## Note

When enabled, `dolt.auto-commit` records successful writes in local Dolt
history; it does not push them to a remote. Remote auto-push is a separate,
opt-in setting and is disabled by default. Auto-push publishes committed HEAD
only; it does not commit pending working-set writes when auto-commit is off or
in batch mode. Keep an explicit commit boundary before handoff unless the
project has a coordinated policy that provides one.
