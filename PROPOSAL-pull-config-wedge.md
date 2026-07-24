# Proposal: fix `bd dolt pull` "cannot merge with uncommitted changes" wedge

**Status:** for review (diagnosis + patch). Authored 2026-06-14 from a live repro in
the Wyvern beads DB (server mode, git-protocol remote on Bitbucket).

## Symptom

`bd dolt pull` always fails:

```
Error 1105 (HY000): cannot merge with uncommitted changes
```

`bd doctor` reports `Dolt Locks: config: modified` immediately after every command,
even right after `bd dolt commit`, and even with `--dolt-auto-commit on`. Tracked as
Wyvern wy-71t. Push is unaffected; only pull (the cross-clone *receive* path) is wedged.

## Root cause — two correct PRs collide over the `config` table

1. **Memories live in `config`.** `bd remember` stores each persistent memory as a
   `kv.memory.<slug>` row in the **synced** `config` table. Confirmed via the working
   set on the live DB — the only dirty rows were exactly the 5 memories:

   ```
   SELECT to_key, diff_type FROM dolt_diff('main','WORKING','config');
   kv.memory.beads-jsonl-sync-procedure                       added
   kv.memory.bitbucket-altssh-dead                            added
   kv.memory.local-dev-ssl-posture                            added
   kv.memory.react-client-e2e-against-the-local-game-server   added
   kv.memory.react-e2e-same-character-parallel-logins         added
   ```

2. **`Commit` deliberately excludes `config`** (GH#2455, `store.go` ~1731):
   `DOLT_COMMIT('-Am', …)` once swept stale `issue_prefix` changes from concurrent
   operations into commits, so `Commit` now stages every dirty table *except* `config`.

3. **The pre-pull auto-commit uses `Commit`** (GH#2474). Both pull paths try to clean
   the working set before merging, but call `s.Commit`, which skips `config`:

   - `internal/storage/dolt/store.go:2309` — `pullFromRemote` (default remote; what
     `bd dolt pull` invokes via `st.Pull`)
   - `internal/storage/dolt/federation.go:65` — `PullFrom` (named-peer pull)

**Net effect:** once any memory exists, the `kv.memory.*` rows sit permanently
uncommitted (nothing ever commits `config`), so the pre-pull "clean the working set"
step leaves `config` dirty, and `DOLT_MERGE` refuses to start — the exact case
`abortMerge`'s comment already calls out: *"a merge that REFUSED TO START on a dirty
working set."* GH#2474's intent is silently defeated by GH#2455 whenever `config` is
the dirty table, which is always after the first `bd remember`.

This is independent of the v42→v51 schema-migration fork (#4259) seen in the same DB;
that was a separate, newer problem. Migrating + force-publishing fixed the fork and
restored `push`, but `pull` stays wedged because of this config issue.

## Empirical validation (live DB)

Confirmed the mechanism end-to-end without any source change:

1. The only dirty table was `config`; the only dirty rows were the 5 `kv.memory.*`
   entries (see query above).
2. Explicitly committing config — `CALL DOLT_ADD('config'); CALL DOLT_COMMIT('-m', …)` —
   left the working set **clean** (`dolt_diff('HEAD','WORKING','config')` → 0 rows).
3. Running ordinary commands afterward (`bd list`, `bd show`) kept it clean (still 0).
   So bd's per-command memory re-sync is **idempotent**: once the rows match HEAD,
   re-writing identical values produces no diff. The "re-dirties after every command"
   symptom existed *only* because the rows were never committed (Commit skips config),
   so they showed as permanently uncommitted. Committing config once breaks the cycle.

This proves a clean `config` working set is the *only* thing the merge needs, and that
a pre-merge commit of config is a permanent fix, not a per-command band-aid.

**Confirmed:** after committing config, `bd dolt pull` succeeds ("Pull complete.")
repeatably, `bd dolt push` still works, and `bd doctor` no longer reports
`config: modified`. The wedge is entirely the uncommitted-config condition; the patch
just makes the pull path establish it automatically. (Without the patch the wedge
returns on the next `bd remember`, which adds a fresh uncommitted `kv.memory.*` row.)

**One wrinkle to verify in the patch:** a standalone `bd dolt commit` (which already
routes through `CommitWithConfig` → `DOLT_COMMIT('-Am', …)`) did **not** visibly clean
`config` in this server-mode DB, whereas the explicit `DOLT_ADD('config')` +
`DOLT_COMMIT('-m', …)` did. That suggests `-Am` may not be staging `config` reliably
under the SQL stored-procedure path here. The patch author should confirm `CommitWithConfig`
actually commits `config` in server mode; if not, the pre-pull step should stage config
explicitly (`CALL DOLT_ADD('config')` then commit) rather than rely on `-Am`. A regression
test should assert `dolt_status` is empty immediately after the pre-pull commit.

## Fix (primary) — pre-pull commit must include `config`

`CommitWithConfig` already exists (`store.go:1780`) for exactly this "commit everything,
including intentional config" case — `CommitPending` (`bd dolt commit`) already uses it.
Use it in the two pre-pull sites:

```diff
--- a/internal/storage/dolt/store.go
+++ b/internal/storage/dolt/store.go
@@ pullFromRemote (GH#2474 pre-pull auto-commit)
-        if err := s.Commit(ctx, "auto-commit before pull"); err != nil {
+        // Include config: persistent memories live in config as kv.memory.* rows,
+        // and Commit() excludes config (GH#2455), so they never commit and leave
+        // the working set permanently dirty — DOLT_MERGE then refuses to start.
+        if err := s.CommitWithConfig(ctx, "auto-commit before pull"); err != nil {
             if !isDoltNothingToCommit(err) {
                 return fmt.Errorf("failed to commit pending changes before pull: %w", err)
             }
         }
```

```diff
--- a/internal/storage/dolt/federation.go
+++ b/internal/storage/dolt/federation.go
@@ PullFrom (GH#2474 pre-pull auto-commit)
-        if err := s.Commit(ctx, "auto-commit before pull"); err != nil {
+        if err := s.CommitWithConfig(ctx, "auto-commit before pull"); err != nil {
             if !isDoltNothingToCommit(err) {
                 return nil, fmt.Errorf("failed to commit pending changes before pull: %w", err)
             }
         }
```

### Why this does not reintroduce GH#2455

GH#2455 guards against `-Am` sweeping a *concurrent writer's* half-applied `issue_prefix`
change into an unrelated commit. The pre-pull commit is different in kind: it is an
explicit user action (`bd dolt pull`) that **requires** a clean working set, and it is
committing *this clone's own* config state as the basis for the merge — exactly what
`CommitPending` already does for `bd dolt commit`. The race window GH#2455 worried about
(another bd process mutating `issue_prefix` at the same instant) is not meaningfully
widened by committing config here versus letting the user's next `bd dolt commit` do it.

## Fix (optional hardening) — auto-resolve convergent `config` merge conflicts

With the primary fix, `config` (incl. `kv.memory.*`) becomes a committed, synced table,
so two clones that edit the *same* memory will produce a `config` merge conflict.
`TryAutoResolveMergeConflicts` (`mergesettle.go`) currently handles only `metadata`,
`dependencies`, and `schema_migrations` — **not** `config` — so such a conflict would
stop the pull for the operator.

Recommend a follow-up that teaches the resolver to auto-resolve `config` conflicts on
machine-convergent keys (e.g. `kv.memory.*`) with `--theirs`, the same convergent policy
already used for the `metadata` table. Out of scope for the wedge fix itself; the primary
change resolves the reported bug. (Until then, divergent same-memory edits are a rare,
operator-visible case rather than a silent wedge.)

## Alternative considered — move memories to a `dolt_ignore`'d table

Rejected: memories are meant to **sync** (they round-trip through `bd export | bd import`,
and are part of cross-clone shared state). `dolt_ignore` (as used for wisps) would make
them machine-local and stop syncing them via Dolt — a behavior change, not a fix.

## Test plan

Regression test (alongside `pull_conflict_test.go` / `federation_pull_settle_test.go`):

1. Two clones of a tiny remote-backed DB.
2. On clone A: `bd remember --key t "x"` (dirties `config` with a `kv.memory.*` row,
   committing nothing via the normal path).
3. On clone B: create+push an issue.
4. On clone A: `Pull` — **must succeed** (today it returns
   `cannot merge with uncommitted changes`), and clone A must end with B's issue *and*
   its own memory intact.

Manual repro on the Wyvern DB: a fresh `bd dolt pull` fails today purely on the 5
`kv.memory.*` rows; with the patch the pre-pull `CommitWithConfig` commits them and the
merge proceeds.

## Files

- `internal/storage/dolt/store.go:2309` (`pullFromRemote`) — primary
- `internal/storage/dolt/federation.go:65` (`PullFrom`) — primary
- `internal/storage/dolt/store.go:1780` (`CommitWithConfig`) — reused, unchanged
- `internal/storage/versioncontrolops/mergesettle.go` (`TryAutoResolveMergeConflicts`)
  — optional hardening
```
