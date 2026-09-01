---
title: Pre-write gate
description: Configure a synchronous, fail-closed workspace admission hook for Beads mutations.
---

# Pre-write gate

Beads can synchronously ask one workspace-owned executable whether a mutation
may begin. This is an admission boundary, not an event notification: a refusal,
timeout, invalid executable, execution error, oversized response, or malformed
response prevents the write. When no hook is configured, mutations retain their
normal behavior.

Place an executable at `.beads/hooks/pre_write` on Unix. On Windows, configure
exactly one of `.beads/hooks/pre_write.exe`, `.cmd`, or `.bat`. The hook runs
with its working directory set to the workspace root and receives no command
arguments or issue content. Its environment is reduced to the OS command-path
variables needed to launch it; the complete input arrives on standard input.

The hook must exit successfully and write exactly one JSON object:

```json
{"allow": true}
```

To reject a mutation, return a successful JSON response with `allow: false`:

```json
{"allow": false, "reason": "exclusive maintenance is in progress"}
```

The hook input is stable and versioned:

```json
{
  "version": 1,
  "operation": "issue.update",
  "repository": {
    "root": "/absolute/workspace",
    "beads_dir": "/absolute/workspace/.beads"
  }
}
```

`operation` identifies the mutation family, including `issue.create`,
`issue.update`, `issue.close`, `issue.claim`, `issue.comment`,
`dependency.add`, `dependency.remove`, and workspace-level changes. New
operation names are additive. Hooks must reject unknown protocol versions and
operation names if their own policy cannot safely decide them.

The same admission layer is installed for the direct storage chain and the
unit-of-work provider used by proxied and HTTP server paths. A transaction that
is denied before a later write returns an error and rolls back instead of
committing earlier writes. Read-only operations do not invoke the gate.

The existing `on_create`, `on_update`, and `on_close` scripts remain
post-commit, asynchronous notifications. `--no-hooks` disables only those
best-effort notifications; it never bypasses a configured `pre_write` gate.

Hooks run with the local user's authority. Review and protect the executable
like any other repository automation. The runner rejects symlinks, directories,
non-executable Unix files, ambiguous Windows candidates, and paths that resolve
outside the configured hooks directory.
