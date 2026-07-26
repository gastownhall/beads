---
description: Synchronize issues with a configured Dolt remote
argument-hint: ""
---

`bd sync` runs one full synchronization cycle against the configured Dolt
remote: pull, check for merge conflicts, recompute the denormalized
`is_blocked` flag, then push — retrying a bounded number of times when another
replica wins the push race. It is the loop a multi-machine deployment otherwise
hand-rolls in shell, and it is the verb to reach for on a sync timer.

(This is not the old JSONL-era `bd sync`, which was removed in v0.51. Nested
commands such as `bd backup sync` and the tracker-specific sync commands are
separate commands and are unaffected.)

```bash
bd sync                  # sync with the default remote
bd sync --remote mini    # sync with a specific named remote
bd sync --attempts 5     # allow more push-race retries (default 3)
bd sync --json           # machine-parseable outcome
```

Exit codes are the machine contract, so a timer can branch on them without
parsing output:

| Code | Meaning |
|------|---------|
| `0` | Synced, or nothing to do |
| `1` | Error (transport, auth, storage) |
| `2` | Merge conflict — halted, nothing pushed; resolve it by hand |
| `3` | Push-race retries exhausted — transient, retry on the next tick |

Two properties are the point of the verb. Conflicts are detected **positively**,
from the merge's own conflict rows and from Dolt's conflict tables — never
inferred from the pull's exit status, which is untrustworthy in both directions.
And a conflict sync cannot settle is **never** resolved by picking a side: it
halts before recomputing or pushing, and keeps halting the same way until an
operator resolves the divergence. (The pull underneath still auto-settles the
convergent conflict classes it always has — machine-local metadata rows,
audit-only dependency rows, last-write-wins on issue cells. Anything past those
halts here.)

The halt message says which state the halt left behind, because it depends on
the pull route and both are real: the SQL route aborts the conflicted merge and
restores the working set, while the CLI/git-protocol route leaves the conflict
rows **live** for you. A conflict that was already live when sync started — from
an earlier halted sync or a hand-run merge — halts before the pull instead, and
says that too.

The recompute between pull and push is not bookkeeping either — `is_blocked` is
denormalized, so a dependency edge merged in from another replica leaves
`bd ready` stale until it runs. It runs on every attempt, unconditionally:
`bd recompute-blocked`'s full pass is specifically the repair that does *not*
depend on a merge advancing HEAD, so it is what recovers a column left stale by
a conflicted pull you resolved by hand — a state sync itself creates by exiting
2. Gating it on "did this tick merge anything" would mean that repair never runs
again while every tick reports success.

This is not `bd federation sync`, which syncs with named peer towns and takes a
`--strategy ours|theirs` to resolve whatever conflicts it meets. `bd sync`
targets the configured remote and has no such switch.

`--json` reports `{"status", "attempts", "conflicts", "conflicts_live",
"rows_corrected", "pushed"}` on every non-error exit; exit 1 emits bd's standard
`{"error": ...}` envelope instead, so a timer reading `.status` must treat
`null` as the error case (or just branch on the exit code, which is the point).
A rig with no remote configured exits 0 with setup guidance; `dolt.local-only`
and the `no-push` config flag are honored.

## The equivalent by hand

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
