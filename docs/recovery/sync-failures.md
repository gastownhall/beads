---
title: Sync Failures
description: Recover from Dolt sync failures
---

This runbook helps you recover from Dolt sync failures.

## Symptoms

- `bd dolt push` or `bd dolt pull` hangs or times out
- Network-related error messages
- "failed to push" or "failed to pull" errors
- `bd dolt push` fails with `Found dangling references to HashSet` (see
  [below](#push-fails-with-found-dangling-references))
- Dolt server not responding

## Diagnosis

```bash
# Check Dolt server health
bd doctor
bd dolt show

# View Dolt server logs
tail -50 .beads/dolt-server.log   # server mode
```

## Solution

**Step 1:** Stop the Dolt server
```bash
bd dolt stop
```

**Step 2:** Check for lock files
```bash
ls -la .beads/*.lock
# Remove stale locks if Dolt server is definitely stopped
rm -f .beads/*.lock
```

**Step 3:** Back up and preview fixes
```bash
cp -r .beads .beads.backup
bd doctor --dry-run
```

**Step 4:** Apply fixes if needed
```bash
bd doctor --fix
```

**Step 5:** Restart the Dolt server
```bash
dolt sql-server
```

**Step 6:** Verify sync works
```bash
bd dolt push
bd doctor
```

## Common Causes

| Cause | Solution |
|-------|----------|
| Network timeout | Retry with better connection |
| Stale lock file | Remove lock after stopping Dolt server |
| Corrupted state | Back up, then `bd doctor --fix` |
| Remote missing a chunk this push points to | See [Push fails with "Found dangling references"](#push-fails-with-found-dangling-references) |
| Merge conflicts | See [Merge Conflicts](/recovery/merge-conflicts) |

## Prevention

- Ensure stable network before sync
- Let sync complete before closing terminal
- Use `bd dolt stop` before system shutdown

## Push fails with "Found dangling references"

**Symptom.** `bd dolt push` fails with either rendering of the same refusal:

```text
Error 1105 (HY000): unknown push error; Found dangling references to HashSet {
  <hash>
}
```

or, on the `dolt` CLI route:

```text
dolt push failed: … Found dangling references to HashSet {…}: exit status 1
```

A different `<hash>` can appear on each failure in the same repository.

**What it means.** Dolt checks, before it moves the remote branch, that the
remote already has every chunk this push points at. The remote reported one
of those chunks missing. The check is about the remote: it did not move the
remote branch, and it is not a sign that your local database is damaged.
(Local corruption is a different, bd-side pre-push check that reports
`dangling chunk reference` and aborts the push before it reaches the remote.)

**Telling the cases apart.** A different hash on each failure usually means a
transient race on the remote — another push to the same remote, or an upload
the remote had not yet registered. The same hash every attempt suggests the
remote itself is damaged (a chunk it should already have is missing). To
decide, run right after a failure:

```bash
bd sql "SELECT dolt_hashof('HEAD') AS head_commit, dolt_hashof_db('HEAD') AS head_root"
```

- If the refused hash equals `head_commit` or `head_root`, it is a chunk this
  push just uploaded: a transient remote-side race, so re-run.
- If it matches neither and repeats across attempts, the remote is missing a
  chunk it should already have: it is damaged, so seed a fresh remote.

**Steps.**

1. Re-run the same push once.

2. If it fails again with the same hash, seed a fresh remote from this
   clone. Create an empty database at your provider, then:

   ```bash
   bd dolt remote add fresh <url>
   bd dolt push --remote fresh
   ```

   Make it the default with `bd dolt remote remove origin` followed by
   `bd dolt remote add origin <url>`. On each other clone, re-point `origin`
   the same way and run `bd dolt pull`, or run `bd bootstrap` in a clean
   checkout.

3. If it keeps failing, report it to Dolt with the refused hash, the output
   of `dolt version`, the remote type, and the UTC failure times (DoltHub
   can match them to its manifest logs):
   <https://github.com/dolthub/dolt/issues>
