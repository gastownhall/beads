---
id: hooks
title: bd hooks
slug: /cli-reference/hooks
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc hooks` (bd version 1.0.3)

## bd hooks

Install, uninstall, or list git hooks for beads integration.

The hooks provide:
- pre-commit: Run chained hooks before commit
- post-merge: Run chained hooks after pull/merge
- pre-push: Run chained hooks before push
- post-checkout: Run chained hooks after branch checkout
- prepare-commit-msg: Add agent identity trailers for forensics

```
bd hooks
```

### bd hooks install

Install git hooks for beads integration.

By default, hooks are installed to .git/hooks/ in the current repository.
Use --beads to install to .beads/hooks/ (recommended for Dolt backend).
Use --shared to install to a versioned directory (.beads-hooks/) that can be
committed to git and shared with team members.

Hooks use section markers to coexist with existing hooks — any user content
outside the markers is preserved across installs and upgrades.

Installed hooks:
  - pre-commit: Run chained hooks before commit
  - post-merge: Run chained hooks after pull/merge
  - pre-push: Run chained hooks before push
  - post-checkout: Run chained hooks after branch checkout
  - prepare-commit-msg: Add agent identity trailers (for orchestrator agents)

```
bd hooks install [flags]
```

**Flags:**

```
      --beads    Install hooks to .beads/hooks/ (recommended for Dolt backend)
      --chain    Chain with existing hooks (run them before bd hooks)
      --force    Overwrite existing hooks without backup
      --shared   Install hooks to .beads-hooks/ (versioned) instead of .git/hooks/
```

### bd hooks list

Show the status of bd git hooks (installed, outdated, missing).

```
bd hooks list
```

### bd hooks run

Execute the logic for a git hook. This command is typically called by
thin shim scripts installed in .git/hooks/.

Supported hooks:
  - pre-commit: Run chained hooks before commit
  - post-merge: Run chained hooks after pull/merge
  - pre-push: Run chained hooks before push
  - post-checkout: Run chained hooks after branch checkout
  - prepare-commit-msg: Add agent identity trailers for forensics

The thin shim pattern ensures hook logic is always in sync with the
installed bd version - upgrading bd automatically updates hook behavior.

```
bd hooks run <hook-name> [args...]
```

### bd hooks uninstall

Remove bd git hooks from .git/hooks/ directory.

```
bd hooks uninstall
```
