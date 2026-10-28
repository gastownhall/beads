---
title: "bd sync"
description: "Pull, check for conflicts, repair is_blocked, and push (the federation loop)"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc sync`.

Run one full synchronization cycle against the Dolt remote.

This is the loop every multi-machine beads deployment otherwise hand-rolls in
shell:

  1. pull from the remote
  2. check for merge conflicts POSITIVELY, from the merge's own conflict rows
     and from Dolt's conflict tables — never inferred from the pull's exit
     status, which is not a trustworthy conflict signal in either direction
  3. recompute the denormalized is_blocked flag, so dependency edges merged in
     from another replica do not leave 'bd ready' stale
  4. push, retrying a bounded number of times when another replica wins the
     push race

The repair in step 3 refuses to run while another writer has uncommitted changes
to issues/dependencies. That is transient and not this sync's doing, so it is
retried on the same budget as a push race rather than failing the run. A working
set that is NOT transient exits 4 instead, because no amount of retrying will
ever publish and only an operator can clear it. Two kinds of evidence say so:
constraint violations on the dirty tables are detected positively and escalate
on the very attempt that finds them; an abandoned uncommitted edit has no such
positive signal, so it is only inferred once the same pending graph edits have
blocked every attempt of several consecutive runs.

Conflicts sync cannot resolve safely are NEVER auto-resolved: it halts before
recomputing or pushing and exits 2, and repeated runs keep halting the same way
until an operator resolves the divergence. (The pull underneath does auto-settle
the conflict classes it can settle convergently — machine-local metadata,
audit-only dependency rows, and last-write-wins on issue cells. Anything beyond
those halts here.) Whether the halted merge was aborted or left live in the
working set depends on the pull route, so the halt message reports which.

Exit codes (a sync timer can branch on these without parsing output):

  0  synced, or nothing to do
  1  error (transport, auth, storage)
  2  merge conflict — halted, nothing pushed, resolve it by hand
  3  retries exhausted (push race, or a concurrent writer's dirty working set)
     — transient, nothing pushed, retry on the next tick
  4  the dirty working set is stuck, not busy: identical pending graph edits
     blocked every attempt of several consecutive runs — nothing pushed, and no
     later tick will publish until an operator clears it

On the default-remote path, a rig with no Dolt remote yet but a git origin
configured adopts that origin as its Dolt remote first, exactly as 'bd dolt push'
does — so 'bd sync' works as a first-time federation bring-up step instead of
reporting 'no remote' and doing nothing. Passing --remote never adopts anything.

This is not 'bd federation sync', which syncs with named peer towns and takes a
--strategy ours|theirs to resolve whatever conflicts it meets. 'bd sync' targets
the configured remote and has no such switch: what it cannot settle, it halts on.

Examples:
  bd sync                        # sync with the default remote
  bd sync --remote mini          # sync with a specific remote
  bd sync --attempts 5           # allow more push-race retries
  bd sync --json                 # machine-parseable outcome

```
bd sync [flags]
```

**Flags:**

```
      --attempts int    Maximum pull/push attempts before reporting a transient retry exhaustion (exit 3) (default 3)
      --no-adopt        Never derive a Dolt remote from git origin (also BD_NO_REMOTE_ADOPT=1)
      --remote string   Sync with a specific named remote instead of the default
  -y, --yes             Consent to adopting a Dolt remote derived from git origin when none is configured
```
