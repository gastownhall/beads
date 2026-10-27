---
title: Sync Failures
description: Recover from Dolt sync failures
---

This runbook helps you recover from Dolt sync failures.

## Symptoms

- `bd dolt push` or `bd dolt pull` hangs or times out
- Network-related error messages
- "failed to push" or "failed to pull" errors
- Dolt server not responding
- `bd dolt push` fails with "Found dangling references to HashSet" — see
  [Push fails with "Found dangling references"](#push-fails-with-found-dangling-references)

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
| Merge conflicts | See [Merge Conflicts](/recovery/merge-conflicts) |
| Remote refused a chunk ("Found dangling references") | See [Push fails with "Found dangling references"](#push-fails-with-found-dangling-references) |

## Push fails with "Found dangling references"

**Symptom:** `bd dolt push` exits 1 with one of:

```text
Error: failed to push to origin/main: Error 1105 (HY000): unknown push error; Found dangling references to HashSet {
  e564v1teutmhp289qspj9ba1lhrvit00
}
```

```text
Error: dolt push failed: … unknown push error; Found dangling references to HashSet {
  e564v1teutmhp289qspj9ba1lhrvit00
}: exit status 1
```

**What it means:** before Dolt moves the remote branch, it asks the remote
whether it has every chunk the pushed data points to. The remote said it does
not have the chunk named in the message, so Dolt refused the push. The remote
branch was **not** moved, and this is not a sign of local corruption: bd
reports local corruption separately, as `dangling chunk reference` from its
pre-push check.

**Telling the cases apart:** right after a failure, run:

```bash
bd sql "SELECT dolt_hashof('HEAD') AS head_commit, dolt_hashof_db('HEAD') AS head_root"
```

- If the hash in the error equals either value, it is a chunk this push had
  just uploaded and the remote had not registered yet: a transient race on the
  remote (often another push to the same remote). Re-run the push.
- If it matches neither and the same hash comes back on every attempt, the
  remote is missing a chunk it should already have: it is damaged. Seed a fresh
  remote.

**Steps:**

1. Re-run the same push once:
   ```bash
   bd dolt push
   ```
2. If it fails again with the same hash, seed a fresh remote from this clone.
   Create an empty database at your provider, then:
   ```bash
   bd dolt remote add fresh <url>
   bd dolt push --remote fresh
   ```
   Make it the default (this also rewrites `sync.remote`):
   ```bash
   bd dolt remote remove origin
   bd dolt remote add origin <url>
   ```
   On each other clone, re-point `origin` the same way and run `bd dolt pull`,
   or run `bd bootstrap` in a clean checkout.
3. If it keeps failing, report it to Dolt at
   <https://github.com/dolthub/dolt/issues> with the hash, the output of
   `dolt version`, the remote type, and the UTC times of the failures (DoltHub
   can match them to its manifest logs).

## Prevention

- Ensure stable network before sync
- Let sync complete before closing terminal
- Use `bd dolt stop` before system shutdown
