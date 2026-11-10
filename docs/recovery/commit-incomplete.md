---
title: Incomplete Commits
description: Recover when bd reports "commit incomplete" or "commit verification failed"
---

This runbook helps you recover when a commit reports that tracked changes
stayed behind in the working set, or that it could not verify whether they
did. Both errors protect the same thing: your writes are saved in the Dolt
working set, but they are not in HEAD, so a push will not send them.

## What happened

Embedded mode (the default) commits the whole working set with one
`DOLT_COMMIT('-Am')`. Dolt's `-A` staging filters out tables covered by an
**enabled `dolt_ignore` pattern**, even when the table is tracked and its
changes are real. If such a pattern covers a tracked table — `issues`, most
commonly, from a hand-added or scripted `dolt_ignore` row — every commit
silently skips that table's writes. Older bd releases answered "nothing to
commit" or plain success, and the data stayed stranded in the working set
while `push` reported completion. bd now detects this and reports it.

## Symptoms

From a full commit (`bd dolt commit`, `bd vc commit`, the commit step of
`bd sync`, or auto-commit) on embedded storage:

```text
Error: commit incomplete: tracked changes remain in issues; these changes are
not in HEAD and push will not send them. Enabled dolt_ignore pattern(s) that
may cover them: issues (suggested cause, not proof). The writes are saved in
the working set: back up the store, inspect the dolt_ignore configuration,
correct unwanted patterns, and retry the commit; do not replay the writes
```

The same finding with `DOLT_COMMIT reported success, but …` means a
**partial commit landed**: the dirty tables outside the pattern were
committed, the covered ones were not. `bd dolt push` (and the push step of
`bd sync`) refuses with the same finding instead of publishing the
incomplete history, and so do the pull verbs.

A second error means the post-commit completeness check itself could not
run:

```text
Error: commit verification failed: DOLT_COMMIT reported success; the commit
may have landed, but completeness could not be verified (failed operation:
load the Beads-owned dolt_ignore policy: …); inspect the working set and the
dolt_ignore configuration before retrying the commit; do not replay the writes
```

Its `nothing to commit` variant means DOLT_COMMIT reported nothing to commit
and the cleanliness check could not confirm even that.

## Reading the error

The message is forensic, not an asserted cause:

- **Tables** are exactly what `dolt_status` still lists after the commit —
  the tracked tables whose changes did not reach HEAD. A table rename shows
  in Dolt's `<from> -> <to>` spelling.
- **Patterns** are the enabled `dolt_ignore` rows outside bd's own set.
  They suggest where the skip comes from; they do not prove who wrote them
  or when. They can be absent when no such pattern exists or when the
  optional read that collects them failed — the table list is the finding,
  not the pattern list.
- **`reported success` vs `tracked changes remain`**: the first means
  DOLT_COMMIT committed everything it staged (a partial commit — the other
  dirty tables landed); the second means it committed nothing.
- The verification error's outcome line records **DOLT_COMMIT's own answer**
  and separates it from what the failed check could not confirm. It never
  claims the commit failed and never prescribes replaying it.

**Detection limits.** The check runs only on full working-set commits in
embedded mode. bd's own clone-local tables (the `__temp__%`, journal,
`wisp`/`wisps`, `leases`, `local_metadata`, `repo_mtimes` and, after
migration 0062, `events` patterns) are expected to stay out of commits and
are exempted by policy, not by pattern-matching your `dolt_ignore` rows.

## Solution

**Step 1:** Back up the store while no `bd` command is running:

```bash
cp -r .beads .beads.backup.$(date -u +%Y%m%dT%H%M%SZ)   # embedded mode (the default)
dolt backup -f <dest-url>                                # server mode
```

An export (`bd export`) is **not** this backup: it snapshots the current
rows, drops history and clone-local state, and re-import does not restore
either. Keep the file-level copy (or the `dolt backup`) as the rescue; an
export is at best a convenience alongside it.

**Step 2:** Inspect the working set and the ignore configuration. In
embedded mode, run the dolt CLI in the database directory
(`.beads/embeddeddolt/<database>/` — the name from the error or
`bd dolt remote list`'s URL):

```bash
cd .beads/embeddeddolt/<database>
dolt sql -q "SELECT table_name, status FROM dolt_status"
dolt sql -q "SELECT pattern, ignored FROM dolt_ignore WHERE ignored = 1"
```

`bd dolt status` shows the same working set from outside. Confirm which
enabled pattern covers each residual table before changing anything.

**Step 3:** Correct the configuration you have confirmed is unwanted — only
those rows, and never bd's own patterns listed above:

```bash
dolt sql -q "UPDATE dolt_ignore SET ignored = 0 WHERE pattern = '<pattern>'"
# or, to remove the row entirely:
dolt sql -q "DELETE FROM dolt_ignore WHERE pattern = '<pattern>'"
```

**Step 4:** Retry the commit that failed:

```bash
bd dolt commit -m "recovery: commit the changes the ignore pattern skipped"
```

**Do not replay the writes** — re-running the commands or SQL that produced
the stranded changes applies them a second time (duplicate rows, side
effects on timestamps and counters). The writes are still in the working
set; the commit is all they need. If the error said `reported success`, the
retry is the second commit of a two-commit recovery: the earlier one holds
the tables outside the pattern, this one the rest.

**Step 5:** Verify and publish:

```bash
bd dolt status        # residual tables gone from the working set
bd dolt show          # the new commit contains the previously stranded rows
bd dolt push
```

## If the error is "commit verification failed"

The commit ran; the check that reads `dolt_status` and the ignore policy
after it could not. Inspect before retrying (Step 2), then by what you find:

- **`dolt_status` still lists tracked tables** — treat it as `commit
  incomplete` above: confirm the covering pattern, correct it, retry the
  commit.
- **The working set is clean** — the commit completed; nothing was left
  behind and nothing needs retrying. Publish with `bd dolt push`.
- **The `nothing to commit` variant with no prior changes expected** — the
  commit was a no-op; retry once after fixing whatever made the check fail
  (the error names the failed operation).

## Common Causes

| Cause | Solution |
|-------|----------|
| A hand-added or scripted `dolt_ignore` row covers a tracked table (`issues`, `labels`, …) | Disable or delete the confirmed row, retry the commit |
| An operator pattern copied from another store | Audit `dolt_ignore` against the table list above |
| A transient failure of the post-commit status/policy read | Inspect, then retry per the table above |

## Prevention

- Do not add `dolt_ignore` rows that cover bd's tracked tables; ignore
  patterns are for clone-local data you never want synced.
- After running scripts or tools that touch `dolt_ignore`, check
  `dolt sql -q "SELECT * FROM dolt_ignore WHERE ignored = 1"` covers no
  tracked table.
- Push refusals with this finding are the safety net working — do not
  force past them; fix the configuration instead.
