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
- A pull, `bd sync` or merge after migration 0062 refuses with `local changes
  would be stomped by merge: events` or `the table(s) events have schema
  conflicts` — see
  [Merge blocked on the events table (migration 0062)](#merge-blocked-on-the-events-table-migration-0062)
- On 1.2.2 or older, after another clone pushed migration 0062, `bd dolt
  pull` fails on `events` (on embedded storage: `merge conflicts in events
  require operator resolution`) — see
  [Pull fails on a clone still on the old release](#pull-fails-on-a-clone-still-on-the-old-release)
- A merge commit created by a pull names the tracking ref
  (`Merge branch 'origin/main' into main`) instead of quoting the remote
  URL, or a merge commit from an older release quotes a URL that carries a
  token — see
  [Credentials in a remote URL and merge commits](#credentials-in-a-remote-url-and-merge-commits)

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

## Merge blocked on the events table (migration 0062)

**Symptom:** after this clone migrated past schema 61, a `bd dolt pull`, a
`bd sync` or a merge of a peer's work fails with one of:

```text
local changes would be stomped by merge: events
```

```text
the table(s) events have schema conflicts
```

The first error comes before any merge starts, so nothing is left open. The
second leaves a merge open, and `dolt_schema_conflicts` names `events` with
`cannot merge a table deletion with data modification`. The bead tables
themselves may merge with no conflicts at all.

**What it means:** migration 0062 moved the `events` audit table onto Dolt's
ignore plane: it deleted the versioned table in a commit and recreated it as a
working table that is never committed. Dolt refuses to merge a commit that
still has the versioned table into a clone that holds the ignored one, and it
refuses before it checks whether there is anything to merge. So the first
error also appears when the remote is only behind this clone, still at a
commit from before this clone migrated, with no new work in it. `bd sync`
then fails on every run, because it pulls before it pushes.

When the remote does have new work from a peer that has not migrated (or
whose history predates the migration, such as unpushed work on a laptop), that
work still changes the versioned table. Merging the two meets a deletion on
one side and row changes on the other. Dolt cannot resolve that in either
direction: `DOLT_CONFLICTS_RESOLVE('--ours','events')` and `--theirs` both
fail with `table not found`, and `ALTER TABLE` does not apply. An `events`
schema conflict is not proof of this cause on its own, but after migration
0062 it is the likely one. bd v1.4.0 and later print this diagnosis under
the merge blockers.

Because `events` is ignored, `dolt status` shows a clean working set even
while the merge refuses. That does not mean the table is empty: it holds this
clone's local audit rows.

**Before this clone pushes, stop the writes on the old release.** Any push
from this clone publishes migrated history. A clone still on a release before
1.3.0 (1.2.2 or older, the releases without migration 0062) that commits after
that can no longer pull: see
[Pull fails on a clone still on the old release](#pull-fails-on-a-clone-still-on-the-old-release).
So first, on every such clone, publish its work with its current release, then
stop writing on it until this recovery is done:

```bash
bd dolt pull
bd dolt commit
bd dolt push
```

Keep `bd dolt commit` even on a clone where nothing was written: on those
releases a pull that merges can leave part of the merge uncommitted, and that
leftover blocks the clone's later pull just as a new commit would. It prints
`Committed.` when it finds something and `Nothing to commit.` otherwise. If
the push is rejected because another clone pushed in the meantime, run the
three commands again.

**Then check whether the remote is only behind.** For the `stomped` error, run
a plain push next:

```bash
bd dolt push        # no --force
```

If it succeeds, the remote was only behind this clone, and the push brought it
level: the next `bd dolt pull` or `bd sync` has nothing to merge and succeeds.
Then upgrade the clones on the old release as in step 7. If the push is
rejected as a non-fast-forward, the remote has commits this clone does not,
such as a peer's work from before the migration or work a clone has just
published: follow the steps below.

**Steps:**

1. Keep your local work. Back up the issues and, for the event rows, the whole
   store, while no `bd` command is running:
   ```bash
   mkdir -p .beads/backup
   bd export --all -o .beads/backup/pre-events-recovery.jsonl
   ```
   and copy the `.beads` directory (or take a `dolt backup` in server mode).
2. If a merge is open, abort it explicitly. On embedded storage, run
   `dolt merge --abort` in the database directory. In server mode, run
   `CALL DOLT_MERGE('--abort')` on a connection to the server that serves this
   store (on the server host for a proxied-server workspace). Do not leave
   the merge open and retry on top of it.
3. **Do not** run `DOLT_CHECKOUT('--','events')` to clear the `stomped`
   refusal. The committed HEAD no longer has `events`,
   so reverting it to HEAD deletes the working table and its rows, and every
   later bd write fails with `failed to record event ...: table not found:
   events`. If that has already happened, the next bd command that writes
   recreates an empty `events` table; the lost rows come back only from a
   backup.
4. Do not force past the problem. `bd migrate --force` and retrying the pull
   on the same bd release do not change the history that conflicts, and the
   advice in [Clone behind the remote](/getting-started/upgrading#clone-behind-the-remote)
   still holds: pull before migrating, never force past a data-behind stop.
5. Do not delete the peer's work: keep its branch and the remote ref that
   holds it. Step 6 merges it.
6. Migrate that work, then pull it. The remote's new commits still change the
   versioned `events` table because no clone has migrated them yet. With every
   clone on the old release published and stopped, one clone migrates all of
   them. Prefer the peer: migrating its own clone keeps its `events` rows
   there. On the peer, publish once more with its current release, so it holds
   what the others published (`bd dolt pull`, `bd dolt commit`,
   `bd dolt push`), then install this clone's release and run:
   ```bash
   bd migrate schema
   bd dolt push
   ```
   If that push is rejected as a non-fast-forward, another clone pushed after
   the peer published. The peer has migrated, so it is now in this clone's
   position: make sure every clone on the old release has stopped writing,
   migrate on a fresh clone as below, and then run the pull and push below on
   the peer as well.

   Without the peer, migrate on a fresh clone of the remote, made with this
   clone's bd release in a new empty directory outside this project
   (`bd dolt remote list` on this clone prints the URL):
   ```bash
   bd init --remote <url>
   bd migrate schema
   bd dolt push
   ```
   If that push is rejected as a non-fast-forward, someone pushed in the
   meantime: make sure every clone on the old release has stopped writing,
   then delete the fresh clone and make a new one.

   Once a migrated push succeeds, pull and publish on this clone:
   ```bash
   bd dolt pull
   bd dolt push
   ```
   Both sides of the merge have now deleted the versioned table, so the pull
   merges the peer's work like any other: its beads arrive, and this clone
   keeps its own `events` rows. Delete the fresh clone, if you made one, once
   the pull has succeeded.
7. Upgrade every clone still on the old release (the peer too, if a fresh
   clone migrated). On each, pull with its current release, then install the
   new release straight away:
   ```bash
   bd dolt pull        # still the current release
   ```
   This is step 3 of
   [Multiple clones sharing one remote](/getting-started/upgrading#remote-backed-databases-and-multiple-clones):
   the pull fast-forwards, because the clone stopped writing after it
   published, and afterwards the old release refuses the database with
   `schema version mismatch`.
   The pull also removes the clone's versioned `events` table, and the new
   release starts an empty one. Because `events` rows do not sync after
   migration 0062, that clone's earlier audit rows remain only in history.
   Only a clone that migrated its own history (this clone, and the peer if it
   migrated) keeps them in its working table.

Merging the peer's work without migrating it first, while it still changes
the versioned `events` table, needs a compatibility operation in the Dolt
driver, tracked in [#6727](https://github.com/gastownhall/beads/issues/6727).
Do not hand-write bridge SQL for that; none has been validated against this
procedure.

### Pull fails on a clone still on the old release

**Symptom:** on a clone still on 1.2.2 or older, after another clone pushed
migrated history, `bd dolt pull` fails on `events` and `bd dolt push` is
rejected as a non-fast-forward. On embedded storage the pull prints:

```text
merge conflicts in events require operator resolution; merge aborted and working set restored
```

**What it means:** this clone has a change to the versioned `events` table
that the migrated history does not have. It wrote after that history reached
the remote, or a merge it pulled earlier was never fully committed (the
`bd dolt commit` above). Merging meets the same deletion on one side and row
changes on the other as above, from the opposite direction, so a retry fails
the same way.

**Recovery:** carry its work over by re-cloning. With its current release,
back up the issues and move the database aside:

```bash
mkdir -p .beads/backup
bd export --all -o .beads/backup/pre-adopt.jsonl
mv .beads/embeddeddolt .beads/backup/embeddeddolt.pre-adopt   # embedded mode (the default)
```

Then install the new release, clone the migrated database and restore the
issues the remote does not have:

```bash
bd bootstrap
bd import .beads/backup/pre-adopt.jsonl
bd dolt push
```

This is the re-clone that a clone already running the new binary takes in
[Remote-backed databases and multiple clones](/getting-started/upgrading#remote-backed-databases-and-multiple-clones),
which also covers server mode. The clone's own commit history and `events`
rows stay in the moved-aside directory; keep it until you have checked the
result.

## Credentials in a remote URL and merge commits

A remote URL can carry credentials, for example
`git+https://x-access-token:<token>@github.com/org/repo` or
`http://user:<token>@host/org/db`. In older bd versions, a server-mode
`bd dolt pull` that had to merge diverged histories ran Dolt's own pull, and
its merge commit message copied that stored URL, token included:

```
Merge branch 'main' of git+https://x-access-token:<token>@github.com/org/repo into main
```

`bd dolt push` then published the commit, so anyone who can read the remote's
Dolt data could read the token in `dolt log`.

Server-mode pulls now fetch and then merge the tracking ref, as embedded mode
does, so the merge commit reads:

```
Merge branch 'origin/main' into main
```

Merge commits made by older bd versions stay in history that is already
pushed, and changing the remote URL does not remove them. If a remote URL ever
held a token, rotate the token. Check with:

```bash
bd sql "SELECT commit_hash, message FROM dolt_log WHERE message LIKE '%@%'"
```

Keep credentials out of remote URLs: set `DOLT_REMOTE_USER` /
`DOLT_REMOTE_PASSWORD` in the environment (see `bd dolt pull --help`),
or use a git credential helper for `git+https` remotes.

## Prevention

- Ensure stable network before sync
- Let sync complete before closing terminal
- Use `bd dolt stop` before system shutdown
