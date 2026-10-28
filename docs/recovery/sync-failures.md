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
- `bd dolt push`, `bd sync` or dolt auto-push fails with "Found dangling
  references to HashSet" — see
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

`bd sync` reports the same refusal after `Error: sync failed: push: `, and
dolt auto-push after `Warning: dolt auto-push failed: `.

**What it means:** before Dolt moves the remote branch, it asks the remote
whether it has every chunk the pushed data points to. The remote said it does
not have the chunk named in the message, so Dolt refused the push. The remote
branch was **not** moved. This is not a sign of local corruption: the check
asks the remote, not your local store. (When bd pushes through the dolt CLI,
its pre-push check reports local corruption separately, as
`dangling chunk reference`.)

**Telling the cases apart:** under the error, bd prints this clone's HEAD
commit hash and HEAD root hash, which it reads right after the failure:

```text
  1. Compare the hash in the error with this clone's HEAD:
       HEAD commit: <hash>
       HEAD root:   <hash>
```

They are `dolt_hashof('HEAD')` and `dolt_hashof_db('HEAD')`. bd prints them in
embedded mode, the default, as well as in server mode. In server mode you can
also read them yourself:

```bash
bd sql "SELECT dolt_hashof('HEAD') AS head_commit, dolt_hashof_db('HEAD') AS head_root"
```

`bd sql` does not run in embedded mode, so there, use the values bd printed.

- If the hash in the error equals either value, it is a chunk this push had
  just uploaded and the remote had not registered yet: a transient race on the
  remote (often another push to the same remote). Re-run the push, even when
  the same hash comes back.
- If it matches neither, the remote is missing a chunk it should already have.
  Re-run the push once. If the same hash comes back, the remote is damaged:
  seed a fresh remote (step 2).

**Steps:**

1. Compare the hash with the two values, then re-run the push that failed,
   for example:
   ```bash
   bd dolt push
   ```
2. Only if it fails again with the same hash, and that hash matches neither
   value, seed a fresh remote from this clone. Create an empty database at
   your provider, point the remote that failed at it, and push. For `origin`,
   the default remote (this also rewrites `sync.remote`):
   ```bash
   bd dolt remote remove origin
   bd dolt remote add origin <url>
   bd dolt push
   ```
   On each other clone, re-point `origin` the same way and run `bd dolt pull`,
   or run `bd bootstrap` in a clean checkout.

   If the push that failed named another remote (`--remote <name>`),
   re-point that remote instead and leave `origin` alone:
   ```bash
   bd dolt remote remove <name>
   bd dolt remote add <name> <url>
   bd dolt push --remote <name>
   ```
   In server mode bd sends Hosted Dolt credentials (`DOLT_REMOTE_USER`,
   `DOLT_REMOTE_PASSWORD`) only to `origin`, so that last push would reach a
   fresh Hosted Dolt database without them. In that case, make the fresh
   database the default remote for the seeding push, then put `origin` back
   and point `<name>` at the fresh database. The `origin` commands rewrite
   `sync.remote` on the way, and the last of them restores it:
   ```bash
   bd dolt remote remove origin
   bd dolt remote add origin <url>
   bd dolt push
   bd dolt remote remove origin
   bd dolt remote add origin <origin-url>
   bd dolt remote remove <name>
   bd dolt remote add <name> <url>
   ```
   Here `<url>` is the fresh database and `<origin-url>` is the URL `origin`
   had before. Re-point `<name>` the same way on each other clone that uses it.
3. If it keeps failing, report it to Dolt at
   [dolthub/dolt issues](https://github.com/dolthub/dolt/issues) with the hash,
   the remote type, the output of `bd version` (and of `dolt version` in server
   mode: embedded mode runs the Dolt engine built into bd), and the UTC times
   of the failures (DoltHub can match them to its manifest logs).

## Prevention

- Ensure stable network before sync
- Let sync complete before closing terminal
- Use `bd dolt stop` before system shutdown
