# Release gate — schema-perf (D4v2 date-index benchmarks) theme (be-p2if / be-auu.3)

- **Builder bead (CLOSED):** be-auu.3 — Extract schema-perf (be-s54 FR-5
  benchmarks) theme onto a clean branch off `origin/main` (child of epic
  be-auu, which isolates dolt purge/schema/testutil infra work from a shared
  builder branch for review).
- **Review bead (CLOSED):** be-1ubq — Verdict **PASS** (round 2, after a
  round-1 `request-changes` fix). `deploy_bead: be-p2if`,
  `deploy_commit: 724c658dfbed085f5eec8aad8136f03126351cfe`.
- **Deploy bead:** be-p2if
- **Commit shipped:** `724c658dfbed085f5eec8aad8136f03126351cfe`, unchanged —
  this branch was cut directly from the reviewed SHA (`git checkout -B
  deploy/be-p2if-gate 724c658dfbed085f5eec8aad8136f03126351cfe^{commit}`), no
  cherry-pick or rebase needed. `origin/main` (`7505e173f`) is already a
  direct ancestor of this commit, so it is byte-identical to what the
  reviewer inspected with zero base drift.
- **Branch:** `deploy/be-p2if-gate` on `headfork`
  (`quad341/beads-sec003-contrib`)
- **Evaluated:** 2026-08-15 by beads/deployer

## Scope

Adds EXPLAIN-verified read-path benchmarks and schema regression coverage for
the D4v2 composite `(status, updated_at)` and `defer_until` indexes. The
actual production migration (`idx_issues_status_updated_at`,
`idx_issues_defer_until`) already landed independently on `origin/main` via a
separately-merged PR #3662 (commit `0268ba894`, now migration 0052).
`3b8601c3b` was a parallel/independent implementation of the same migration
whose schema-changing SQL was superseded and dropped; what remains here is
legitimate new value on top of #3662, verified compatible (not duplicative)
by the reviewer.

- `internal/storage/dolt/dolt_benchmark_test.go` (+222/-1) — new
  `BenchmarkGetStaleIssues_{1K,10K,50K}`, `BenchmarkSearchIssues_UpdatedAfter_{1K,10K,50K}`,
  plus `BenchmarkCreateIssue_Existing{1K,10K}` / `BenchmarkUpdateIssue_Existing{1K,10K}`,
  and a same-commit fixup restoring `seedForSummaryBench` (a rebase
  silent-casualty of the same defect class already documented for
  `315bbff29` on the shared branch) which the new FR-5 benchmarks call.
- `internal/storage/dolt/testmain_test.go` (+37) — `BEADS_TEST_EXTERNAL_DOLT_PORT`
  escape hatch for pointing benchmarks at a long-lived external dolt
  sql-server instead of spinning up a fresh one per run.
- `internal/storage/embeddeddolt/schema_test.go` (+57/-3 net after round-2
  correction) — new SHOW-CREATE-TABLE index-name spot-checks in
  `TestSchemaAfterInit`, confirming `idx_issues_status_updated_at` and
  `idx_issues_defer_until` exist and match #3662's actual index names.

No production code touched — all three files are test-only (`_test.go`).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| — | Pre-flight: already merged? | **N/A** | No PR exists yet for `724c658dfbed085f5eec8aad8136f03126351cfe` (`gh api repos/gastownhall/beads/commits/724c658d.../pulls` returned `[]`). Normal gate/PR flow applies, not reconcile. |
| 1 | Review PASS present | **PASS** | be-1ubq `verdict: pass` (round 2), with `deploy_bead: be-p2if` and `deploy_commit` matching this gate's reviewed SHA exactly. |
| 2 | Acceptance criteria met | **PASS** | be-auu.3's stated acceptance: "Branch off current origin/main containing exactly 3b8601c3b, and the seedForSummaryBench hunk (+import) of 315bbff29. go vet clean. git diff --stat vs origin/main touches only schema-perf files. Submitted via normal builder flow with its own review bead." All independently verified below. |
| 3 | Tests pass | **PASS** | Diff-owned test independently re-run by the deployer: `TestSchemaAfterInit` — **PASS (32.47s)**, own run, not just trusting the reviewer. Canonical full-package regression: independent deployer re-run was environmentally blocked (host-wide dolt-process contention on this shared rig, not a code/process defect); reviewer's own two-round-verified 356/0/1040 stands as the evidence for the non-diff-owned bulk of the suite. See "Tests run" below for the full attempt log and reasoning. |
| 4 | No high-severity findings open | **PASS** | be-1ubq: one `security_findings` item, explicitly **minor (mitigated)**, not HIGH — `testmain_test.go`'s `BEADS_TEST_EXTERNAL_DOLT_PORT` escape hatch has a port-precedence asymmetry the reviewer traced end-to-end and found fail-closed (store.go's unconditional production-port guard), no code change requested. `style_findings: none`. Independent `bd list` sweep for "high" scoped to be-auu/be-1ubq/be-p2if/schema-perf/D4v2 returned zero matches. |
| 5 | Final branch is clean | **PASS** | `git status --porcelain` on `deploy/be-p2if-gate` shows only the deployer's own untracked tooling script (`scripts/rebase-resolve-lib.sh`), not part of this diff and excluded from the gate commit. |
| 6 | Branch diverges cleanly from base | **PASS** | `origin/main` (`7505e173f2659ba6e1f955b86d81a4f9e21810ca`) is a direct ancestor of the reviewed commit — `git merge-base --is-ancestor origin/main HEAD` succeeds, and `git merge --no-commit --no-ff origin/main` reports "Already up to date." Zero divergence, zero conflicts possible; no rebase needed. |
| 7 | Single feature theme | **PASS** | Independently measured via `git diff --stat origin/main...HEAD`: exactly 3 files (`dolt_benchmark_test.go`, `testmain_test.go`, `internal/storage/embeddeddolt/schema_test.go`), 313 insertions(+), 3 deletions(-) — matches the reviewer's claim exactly. |

## Acceptance criteria verification (be-auu.3)

| Criterion | Status | Evidence |
|---|---|---|
| Branch off current `origin/main` containing exactly `3b8601c3b` + the `seedForSummaryBench` hunk of `315bbff29` | ✓ | Reviewer confirmed the restored `seedForSummaryBench` (67 lines + doc comment, storage import hunk) is present and is exactly what the new FR-5 benchmarks call at 4 sites; `setupBenchStore`'s `testing.B` signature (PURGE's concern, not this theme's) confirmed unmodified. |
| `go vet` clean (undefined `seedForSummaryBench` would only show here) | ✓ | Reviewer: `go vet ./internal/storage/dolt/... ./internal/storage/embeddeddolt/...` clean, exit 0, both rounds. |
| `git diff --stat` vs `origin/main` touches only schema-perf files | ✓ | Exactly 3 files (see criterion 7 above), independently re-measured by the deployer. |
| Submitted via normal builder flow with its own review bead | ✓ | be-1ubq, verdict PASS (round 2). |

## Tests run

| Test | Result | Notes |
|------|--------|-------|
| `TestSchemaAfterInit` (diff-owned, targeted) | **PASS (32.47s)** | Independently re-run by the deployer: `BEADS_TEST_EMBEDDED_DOLT=1 go test -tags cgo -run '^TestSchemaAfterInit$' -v ./internal/storage/embeddeddolt/...`. Matches the reviewer's round-2 re-verification exactly. This is the test that had 3 stale index-name assertions in round 1 (fixed in commit `724c658d`). |
| Canonical full-package regression | **Environmentally blocked — see below** | Reviewer's own count (round 2, canonical entrypoint): 356 PASS / 0 FAIL / 1040 SKIP (SKIP is the expected `BEADS_TEST_EMBEDDED_DOLT`-gated tier, not diff-owned, per skip_justification below). Deployer's own independent re-run of `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true BEADS_TEST_ENV_RUN_DOLT=1 ./scripts/test.sh -v ./internal/storage/dolt/... ./internal/storage/embeddeddolt/...` did not complete — see "Canonical-run attempt log" below. |
| Benchmarks (`BenchmarkGetStaleIssues_*`, `BenchmarkSearchIssues_UpdatedAfter_*`, `BenchmarkCreateIssue_Existing*`, `BenchmarkUpdateIssue_Existing*`) | Not independently re-run | Smoke-verified by the reviewer across both rounds (file byte-identical since round 1): 4/4 representative (1K) variants ran clean against a real throwaway dolt server, plausible ns/op and allocs/op, no crashes. Benchmarks have no pass/fail semantics beyond "did it crash"; re-running requires a standalone throwaway dolt sql-server + `BEADS_BENCH_DOLT_PORT`, judged disproportionate given two independent clean smoke runs already exist and the file is unchanged since round 1. |

### Canonical-run attempt log

The deployer made four independent attempts to reproduce the reviewer's
canonical full-package run before concluding it was environmentally blocked
on this shared rig, not skipping it as a convenience:

1. Backgrounded run (`BEADS_TEST_ENV_RUN_DOLT=1`, correct env) — alive and
   progressing (confirmed via live `go test` PID, 329s elapsed, no output yet
   because `-v` only flushes per-package) at last check, but the underlying
   background task was later reported `killed` with no further log output
   — consistent with a session-level interruption (this conversation
   underwent a context-compaction cycle while the task was in flight), not a
   test failure.
2. A second background attempt (fresh log, same command) was reported
   `killed` roughly 90 seconds after launch — too fast to be doing
   meaningful work, ruling out "it just needs more time" as the sole
   explanation for attempt 1.
3. A foreground attempt (same command, 9.5-minute bound) produced **zero**
   output beyond the startup banner (`Prebuilding bd...`, `Running: go
   test...`, `Skipping: ` empty) for the entire 9.5 minutes, then hit the
   deployer's own timeout (exit 143). No test, no package, not even a `go
   test -v` `=== RUN` line had flushed — i.e. it never got past initial
   setup/compile, let alone into the dolt-service-backed test bodies.
4. Isolating diagnostic: `pgrep -c -f "dolt sql-server"` on the shared host
   returned **44** concurrent dolt sql-server processes (from other agents
   active on this rig concurrently), while `podman ps -q` showed only 1
   container and a plain `podman run --rm hello-world` smoke test completed
   cleanly and fast (exit 0). This isolates the cause to host-wide resource
   contention specific to the dolt-heavy path (44 competing dolt processes
   starving CPU/ports for compile + container-health-check), not a broken
   container runtime and not a defect in this diff.

Conclusion: this is a rig-wide capacity problem outside the scope of this
test-only, 3-file diff, not a signal about the code under review. Re-running
under contention this severe would not have produced meaningfully different
or more trustworthy evidence than a fifth attempt at the same command;
further retries were judged not worth the wall-clock cost. Criterion 3 rests
on (a) the deployer's own clean independent PASS of the one test that
actually changed behavior in this diff, and (b) the reviewer's own
two-round, 356/0/1040 canonical result for the rest — not on an unverified
claim of an independent full re-run that did not happen.

### Skip justification (canonical run)

`BEADS_TEST_EMBEDDED_DOLT`-gated tests (dolt package's `TestEmbedded*`-prefixed
self-skips, and embeddeddolt's non-diff-owned contract/`TestEmbedded*` tests)
are a real, documented, separate CI tier (test-embedded-storage shard;
`engdocs/CI_TEST_SURFACE_AUDIT.md:104-116`), not part of this diff's changed
test files. `TestSchemaAfterInit` IS diff-owned and was independently
re-verified above under its actual tier's env var, not accepted on the
strength of its self-skip in the canonical run.

## Findings (no action required)

- **KNOWN GAP, flagged by the reviewer (not fixed here, not this diff's
  responsibility):** `3b8601c3b`'s own commit message describes a
  `migration_0033_test.go` round-trip test (up→down→up, 2K rows) as part of
  its test-infra additions, but that file is not present in the actual diff
  and does not exist anywhere on `origin/main` or the shared branch under any
  name — a silent casualty of the same clean-auto-merge rebase-corruption
  class already seen twice elsewhere on this epic. The reviewer judged
  restoring it out of scope for a pure extraction task and recommended a
  separate P2/P3 follow-up bead. Not filed by the deployer — this was
  explicitly flagged by the reviewer as a builder-epic follow-up, not a
  deploy-blocking item.
- **Security finding (mitigated, no code change requested):** see gate
  criterion 4 above.
- **`BEADS_TEST_ENV_RUN_DOLT` opt-in gate:** `scripts/test.sh`'s hermetic
  wrapper (`scripts/ci/lib/test-env.sh`) skips Dolt-service-requiring tests
  by default unless `BEADS_TEST_ENV_RUN_DOLT=1` is explicitly set — distinct
  from the diff's own `BEADS_TEST_EMBEDDED_DOLT` tier gate. The deployer's
  first test invocation omitted this and produced a false-green partial
  result; caught before being recorded as evidence and corrected. Recording
  here since it's an easy trap for a future deploy gate to fall into.

## Push target

`origin` (`gastownhall/beads`) has push explicitly disabled (fetch-only
guard, sentinel URL). Both `fork` (`quad341/beads.git`) and `headfork`/`prhead`
(`quad341/beads-sec003-contrib.git`) accept a dry-run push; using `headfork`
for consistency with the reviewer's own inspected remote (`branch pushed to
fork, tracked as quad341/beads-sec003-contrib after GitHub-reported repo
migration`) and the `be-pow0` precedent.

PR opens cross-repo against `gastownhall/beads:main` with head
`quad341:deploy/be-p2if-gate`.

## Verdict

**PASS** — push `deploy/be-p2if-gate` (this gate-file commit) to `headfork`,
open the PR. `gastownhall/beads` is a contributor-only repo for this rig (no
merge authority) — stop at the open PR; do not route a merge-request to
mayor.
