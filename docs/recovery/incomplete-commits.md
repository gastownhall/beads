---
title: Incomplete Commits
description: Recover when a commit leaves tracked changes behind or cannot verify the working set
---

This runbook helps you recover when `bd` reports that a commit did not
capture every change, or could not check whether it did. The two errors
say different things:

- `commit incomplete` identifies tracked changes that are saved in the Dolt
  working set but are not in HEAD, so `bd dolt push` cannot send them.
- `commit verification failed` means the check after the commit could not
  run, so whether anything is missing from HEAD is unknown. It does not
  prove that any data is absent from HEAD.

Neither error proves the original write failed. The goal is to get any
changes left behind into a commit without losing or repeating any of them.

After a full commit (`bd dolt commit`, `bd vc commit`, auto-commit, or the
commit `bd dolt push` and `bd dolt pull` make before they talk to the
remote), embedded-mode storage (the default `bd init`) reads `dolt_status`
again to check that nothing is left. Two errors come out of that check.

## Symptoms

- **Changes left behind.** The message starts with `commit incomplete:` and
  names the tables still holding changes, for example:

  ```
  commit incomplete: tracked changes remain in issues; these changes are not in HEAD and push will not send them (enabled noncanonical dolt_ignore patterns that may cover them, a suggested cause, not proof: issues); ...
  ```

  When it says `a commit was created, but tracked changes remain in ...`,
  the commit is a partial one: it holds everything except the listed tables.
- **Check could not run.** The message starts with
  `commit verification failed:`. It says either
  `the commit may have landed, but completeness could not be verified` (Dolt
  reported a new commit) or
  `nothing to commit; working-set cleanliness could not be verified` (Dolt
  reported nothing to commit, so whether anything is left is unknown).
- `bd dolt push` or `bd dolt pull` stops with
  `commit pending before push:` or `commit pending before pull:` followed by
  one of the messages above. It stops before contacting the remote and prints
  no success line.

## What the errors carry

Programs that use beads as a library can match both errors instead of
parsing messages:

| Error | Match with | Fields |
|-------|------------|--------|
| `*storage.UncommittedChangesError` | `errors.As` | `Tables` (dirty `dolt_status` table names, sorted; a rename keeps Dolt's `old -> new` form), `IgnorePatterns` (enabled `dolt_ignore` patterns outside bd's canonical clone-local set: a hint at the cause, not proof of it or of who added them; empty when there are none or they could not be read), `Committed` (a partial commit was created) |
| `*storage.CommitVerificationError` | `errors.Is(err, storage.ErrCommitVerificationFailed)`, `errors.As` | `Outcome` (`commit_succeeded` or `nothing_to_commit`: what Dolt itself answered), `Operation` (the read that failed), `Cause` (its error, also reachable with `errors.Is`) |

Neither error means the write failed. Treat `commit incomplete` as "saved,
not committed" and `commit verification failed` as "commit state unknown",
never as a signal to retry the write.

## Diagnosis

```bash
# Current branch, HEAD, and uncommitted changes
bd vc status

# The database and branch the check ran against
bd sql 'SELECT DATABASE(), active_branch()'

# Which tables are dirty, and which ignore patterns are enabled
bd sql 'SELECT table_name, staged, status FROM dolt_status'
bd sql 'SELECT pattern, ignored FROM dolt_ignore WHERE ignored = 1'

# Recent commits, to see whether a commit landed
bd sql 'SELECT commit_hash, message, date FROM dolt_log LIMIT 5'

# What a dirty table holds in the working set but not in HEAD
bd sql "SELECT * FROM dolt_diff('HEAD', 'WORKING', 'issues')"
```

The usual cause of `commit incomplete` is an enabled `dolt_ignore` pattern
that covers a table bd tracks, such as `issues`. Dolt's commit skips tables
an ignore pattern matches, even ones that are already tracked, so their
changes stay behind.

What the check cannot see:

- Only rows `dolt_status` shows count. A new table an ignore pattern hides
  never appears there, so it is never reported.
- bd's own clone-local tables (the `__temp__%`, `bd_events_journal`,
  `bd_events_seq`, `ignored_schema_migrations`, `leases`, `local_metadata`,
  `repo_mtimes`, `wisp_%` and `wisps` patterns, plus `events` once the
  database has applied migration 0062) are left out on purpose: their
  changes are meant to stay local. The exemption comes from bd's canonical
  pattern set, not from whatever rows your `dolt_ignore` holds, so a
  pattern you added never hides a tracked table from the check.
- The listed patterns are a hint, not proof of the cause or of who added
  them: they are only the enabled patterns outside bd's canonical set.
- Server-mode storage does not run this check.

## Solution

**Step 1:** Stop every `bd` process using this database (in server mode, run
`bd dolt stop`), then back up the whole `.beads` directory:

```bash
cp -r .beads ".beads.backup-$(date +%Y%m%d%H%M%S)"
```

**Step 2:** Inspect with the Diagnosis commands before changing or
retrying anything. Confirm the active database and branch are the ones you
expect, compare the working-set data with HEAD for each dirty table, and
find which ignore pattern covers it. Then decide whether that pattern is
wanted. Do not change anything you have not confirmed.

**Step 3:** If a pattern you confirmed as unwanted covers a tracked table,
turn it off (this example uses the pattern `issues`):

```bash
bd sql "UPDATE dolt_ignore SET ignored = 0 WHERE pattern = 'issues'"
```

For `commit verification failed`, follow
[If the error is commit verification failed](#if-the-error-is-commit-verification-failed)
instead of assuming a pattern is at fault.

**Step 4:** Retry the commit:

```bash
bd dolt commit -m "Commit changes left in the working set"
```

**Step 5:** Verify the history: `bd vc status` should show no uncommitted
tracked changes, and the commit should appear in `dolt_log`. Then push:

```bash
bd vc status
bd sql 'SELECT commit_hash, message FROM dolt_log LIMIT 3'
bd dolt push
```

When the error said `a commit was created`, this retry is the second commit
of a two-commit recovery: the earlier partial commit holds the tables
outside the pattern, this one holds the rest.

<Warning>
**Do not replay the mutations.** The writes already landed in the working
set; running the same `bd create`, `bd update` or `bd close` commands again
applies them twice.

**Do not treat an export as a complete rescue snapshot.** `bd export` and
`.beads/issues.jsonl` hold issue data only, not configuration, clone-local
tables or Dolt history. Keep the `.beads` backup from Step 1 instead.

Do not delete `.beads`, re-run `bd init`, or import an export over the
database to "fix" this: that throws away the uncommitted changes you are
trying to save.
</Warning>

## If the error is commit verification failed

The commit ran; the read after it that checks `dolt_status` and bd's ignore
policy did not. The error names that read (`Operation`) and its error
(`Cause`), and `Outcome` records what Dolt answered. Back up and inspect as
in Steps 1 and 2, then go by what you find:

- **`bd vc status` still lists tracked tables.** Treat it as
  `commit incomplete`: confirm the covering pattern, correct it only if it
  is unwanted, retry the commit, and verify the history.
- **The working set is clean and `dolt_log` shows the commit.** The commit
  completed and nothing was left behind; nothing needs retrying.
- **The working set is clean and Dolt answered `nothing_to_commit`.**
  There was nothing to commit. Fix whatever made the read fail if it keeps
  failing; there is nothing to recover.

In every case, do not replay the mutations: inspect, then commit what is
still in the working set.

## Prevention

- Add `dolt_ignore` patterns only for tables you never want shared, and
  never one that matches a table bd tracks.
- Read the error before retrying anything: neither `commit incomplete` nor
  `commit verification failed` means the writes were lost.
