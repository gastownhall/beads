# Release gate — be-ulyj3 (be-rikgp schema-migration lock context-deadline fix)

**Date:** 2026-08-04
**Deployer:** beads/deployer
**Bead (deploy):** be-ulyj3 — needs-deploy: Review: [bug] schema migrations restart from scratch when they exceed bd's per-command context deadline, instead of converging (from:be-8lthe)
**Source bead:** be-rikgp — closed, bug: schema migrations restart from scratch when they exceed bd's per-command context deadline, instead of converging
**Review bead:** be-8lthe — closed, review verdict PASS
**Source commit:** `c8e0afb66124ead6b7cc9ab9d841c66077ef75d4` — "feat: green — [bug] schema migrations restart from scratch when they exceed bd's per-command context deadline, instead of converging (refs be-rikgp)"
**Provenance branch:** `builder/be-rikgp` — provenance only; confirmed tip == source commit on `origin` (`git ls-remote origin refs/heads/builder/be-rikgp` → `c8e0afb66...`, exact match, nothing unreviewed layered on top)
**Branch (to cut in push-and-pr):** `deploy/be-ulyj3-gate`, isolated, off the exact source commit above
**Base:** `origin/main` @ `67f812a23` ("fix(hooks): refuse symlinked and git-tracked hook writes, back up foreign hooks (bd-5vdt8) (#5284)")
**Merge-base:** `67f812a23` — identical to origin/main's current tip. The source branch was cut directly from today's origin/main with zero intervening drift; this is a pure fast-forward, not a 3-way merge.
**Merge-tree simulation:** `git merge-tree --write-tree origin/main c8e0afb66...` → tree `da8082e9c5a6195b3f4c09e961d61de3d0f9bb5a`, exit 0, **zero conflict markers**

## Verdict: PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | merge-base(source commit, origin/main) == origin/main's exact current tip. Zero drift — cleanest possible case, pure fast-forward. `git merge-tree` confirms zero conflicts regardless. |
| 1 | Review PASS present | PASS | be-8lthe closed by beads/reviewer, `verdict: pass`. Security/style/spec findings all clean; one non-blocking note on fix-site placement (see Acceptance check below). |
| 2 | Acceptance criteria met | PASS | See Acceptance check below — the fix lands one layer deeper than the acceptance criteria's literal fix-site, at a shared choke point. Independently confirmed to satisfy the criteria's actual intent, more broadly than the originally-planned fix would have. |
| 3 | Tests pass (documented CI-equivalent command, real counts) | PASS | Independently re-run at the reviewed commit (not taken on the reviewer's word) — see Test evidence below. Also independently reproduced genuine RED at the pre-fix commit (`594f4946f`) with the exact documented failure mode, confirming this is real TDD evidence, not retrofitted. |
| 4 | No HIGH-severity findings open | PASS | be-8lthe: no blocker/major/minor security findings across the full diff, including a specifically-reasoned examination of the `context.WithoutCancel` DoS/availability angle (concluded not exploitable — narrow scope, fixed non-attacker-controlled migration content, fail-closed on the one traced residual path). No style findings (gofmt/vet/golangci-lint all clean). |
| 5 | Feature branch clean | PASS | `git status --short` at the detached checkout of the reviewed commit: empty. `builder/be-rikgp` tip on `origin` == the reviewed SHA exactly — nothing unreviewed on top. |
| 7 | Single feature theme | PASS | `git diff --stat origin/main c8e0afb66...`: 2 files (`internal/storage/schema/lock.go`, `internal/storage/schema/lock_test.go`), 6 insertions/2 deletions in the production file plus one new test (and minor test-helper plumbing to support it). One coherent theme: decouple an in-flight migration from the caller's context once the migration lock is held. |

## Acceptance check (be-rikgp acceptance criteria / exit_contract)

The acceptance criteria's literal text names `internal/storage/dolt/store.go`'s `initSchemaOnDB` as the expected fix site (`context.Background()` instead of the inbound ctx). The actual diff instead touches `internal/storage/schema/lock.go`'s `MigrateUpWithLock`, using `context.WithoutCancel(ctx)` at both of its `MigrateUp` call sites. The reviewer flagged this as a deliberate, non-blocking deviation and explicitly punted the accept/reject judgment to this gate rather than resolving it unilaterally.

Independently verified, not taken on the reviewer's characterization:

- `initSchemaOnDBWithBootstrapHeal` (what `initSchemaOnDB` calls into) invokes `schema.MigrateUpWithLock(ctx, conn, dbName, opts...)` directly — `internal/storage/dolt/store.go:2390`. The fix at `MigrateUpWithLock` transparently changes `initSchemaOnDB`'s actual runtime behavior; the acceptance criteria's real intent (a short-deadline caller can no longer cancel a long-running migration mid-flight) is satisfied exactly as specified, just from one layer deeper.
- `MigrateUpWithLock` has a second, independent caller — `internal/storage/uow/dolt_sql_provider.go:267` — which the originally-planned `store.go`-only fix would **not** have covered. Fixing the shared function protects both call sites instead of one.
- `context.WithoutCancel(ctx)` (used) vs `context.Background()` (originally planned): `WithoutCancel` strips only cancellation/deadline propagation while preserving context-carried values (trace IDs, loggers, request-scoped metadata). `Background()` would have discarded those entirely. This is a strictly better choice for the same intent, not merely an equivalent one.
- The new test (`TestMigrateUpWithLockContinuesMigrationAfterCallerContextExpiresPostLockAcquire`) directly exercises the acceptance criteria's verification requirement — a short caller deadline (30ms) expiring while a slower migration (250ms, mocked) is in flight under a held lock, confirming the migration still runs to completion (`applied == 1`, no error) rather than being abandoned.

**Conclusion:** the fix-site deviation is accepted as a broader, more architecturally sound implementation of the acceptance criteria's actual intent — not a shortcut or scope gap. Explicitly-excluded items (storage_class/migration-0059 symptom, binary-schema-skew warning `ga-drlztz`) are correctly untouched by this diff.

## Test evidence (independently re-run at the reviewed commit, detached checkout)

- `go build ./...` — exit 0, clean.
- `go vet ./...` — exit 0, clean.
- `gofmt -l internal/storage/schema/lock.go internal/storage/schema/lock_test.go` — no output, clean.
- Focused test, reviewed commit (`c8e0afb66`): `go test -run '^TestMigrateUpWithLockContinuesMigrationAfterCallerContextExpiresPostLockAcquire$' -v ./internal/storage/schema/...` → **PASS (0.25s)**, matching the mocked 250ms delay — confirms the migration ran to completion past the 30ms caller deadline.
- Same test, pre-fix commit (`594f4946f`, the tdd_red commit): re-run to independently confirm genuine RED, not retrofitted — **FAIL**, with the exact documented failure mode (`canceling query due to user request`, migration abandoned mid-flight, deferred `RELEASE_LOCK` call left unmatched). Real TDD RED→GREEN, verified by this deployer, not taken on the builder's or reviewer's word.
- `./scripts/test.sh ./internal/storage/schema/...` (canonical CI-equivalent wrapper) — `ok`, 0.401s.
- `go test -v ./internal/storage/schema/...` — **198 PASS / 0 FAIL / 0 SKIP** (113 top-level + 85 subtests). Differs from the reviewer's reported 185 PASS/13 SKIP (same 198 total test executions, different pass/skip split) — environment/version variance, not a regression: strictly more coverage in this run, 0 FAIL either way. No skip reason text found anywhere in this run's output (all "skip" hits are test *names* containing "Skips", all of which PASS).
- `go test -v ./internal/storage/dolt/...` — **522 PASS / 0 FAIL / 700 SKIP** (232 top-level + 290 subtests; 576 top-level + 124 subtest skips). Closely matches the reviewer's reported 517/0/702. Skip reason independently confirmed: `"Test Dolt server not running, skipping test"` — matches `docker info` failing in this sandbox too (confirmed directly), the same documented, pre-existing, justified sandbox limitation the reviewer already identified (engdocs/TESTING.md: dolt tests skip when no test server is reachable). Not introduced or masked by this diff.

## Hand-off

- Push target: `fork` (`quad341/beads`) — `origin` (`gastownhall/beads`) push is deliberately disabled (fetch-only sentinel), upstream is fork-and-PR workflow. Matches proven precedent (be-pt3sv / PR #5194, since merged upstream). `fork` and `prhead` remotes point at the same underlying repo (`quad341/beads`, renamed to `quad341/beads-sec003-contrib`, GitHub redirects the old URL) — using `fork` for consistency with precedent.
- PR: cross-repo `quad341:deploy/be-ulyj3-gate` → `gastownhall:main`.
- **gastownhall/beads is upstream-only for this rig** (contributor relationship, not maintainer). Per role instructions, job ends at opening the PR — no merge-request routed to mayor for this repo; merge belongs to the upstream maintainers.
