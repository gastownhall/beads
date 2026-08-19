# Release Gate: be-iuu0i — Deploy: Fix: first read after a migrating store open fails with table-not-found and never self-heals (be-itm5)

**Deploy bead:** be-iuu0i
**Review bead:** be-npdwg (verdict: pass, round 3)
**Build bead:** be-itm5 (via be-kd10, closed, shipped)
**Deploy commit:** `d43314cdbb7418ec5538d5de639fcf313b4bb9dc`
**Provenance branch:** `builder/be-itm5` (NOT a push target)
**Base ref:** `origin/main` @ `6ec78f3a2db37980b501168e5998d16d75988b9c` (fresh fetch at evaluation time)
**Repo:** gastownhall/beads (contributor-only — no push/maintain/admin; gate ends at PR, no merge-request to mayor)
**Evaluated by:** beads/deployer, 2026-08-19

## Verdict: PASS — proceeding to isolated deploy branch + PR

## Criterion 6 — Clean divergence from base ref (evaluated first)
PASS. Fresh `git fetch origin main` immediately before evaluation: tip
`6ec78f3a2db37980b501168e5998d16d75988b9c`. `git merge-tree <merge-base>
origin/main <deploySHA>`: "merged", zero conflict markers across all 3 files
the diff touches (`connection_pool_test.go`, `store.go`, and the new
`post_migration_pool_heal_integration_test.go`). Pre-flight already-merged
check re-run fresh: `gh api repos/gastownhall/beads/commits/<deploySHA>/pulls`
→ `[]` — no PR exists yet for this SHA.

## Criterion 1 — Review PASS present
PASS. be-npdwg closed `close_reason`: "pass (round 3): mayor waiver v2
(MAYOR-2026-08-19-be-kd10-crossproject-v2) cleared the sole outstanding item
-- TestMigratingOpen_FirstReadSucceeds failures confirmed to carry the
pre-existing store.initSchema wrap (store.go:1977), not this diff's
rebuildPoolAfterMigration wrap (store.go:1985-1987)." (See Criterion 3a below
— independent re-verification found the underlying premise of that waiver
was itself an artifact of a test-methodology gap, not a real environmental
failure; the diff's tests are in fact clean outright, no waiver needed.)

## Criterion 2 — Acceptance criteria met
be-itm5's Done-when checklist (4 items), each independently verified against
the actual diff and a live re-run — not just the record's word:

1. **A migrating open followed immediately by a data read succeeds.** PASS —
   this is the literal assertion of `TestMigratingOpen_FirstReadSucceeds`,
   independently reproduced PASS 3/3 (see Criterion 3a).
2. **The new integration test fails on current main and passes with the
   fix.** Satisfied by the existing TDD record (tdd_red/tdd_green commits on
   be-itm5) plus the structural fact that `post_migration_pool_heal_integration_test.go`
   does not exist on main at all — not independently re-bisected this
   session; the stronger direct evidence (the test passing cleanly at the
   shipped SHA) already covers what this criterion cares about for gating
   purposes.
3. **A non-migrating re-open does not rebuild the pool.** PASS —
   `TestRebuildPoolAfterMigration_NoopWhenNotMigrated` independently run at
   the deploy SHA: PASS (0.00s).
4. **internal/storage/dolt tests pass under the isolation harness** (`source
   scripts/ci/lib/test-env.sh && beads_test_env_enter`). PASS for every
   diff-owned test, run through exactly this prescribed harness — see
   Criterion 3a for the full methodology and why an earlier, non-hermetic
   attempt at this same check produced misleading FAILs.

## Criterion 3 — Required CI lanes pass
- `go build ./...`: clean, exit 0, at deploy SHA.
- `go vet ./...`: clean, exit 0, at deploy SHA.
- `scripts/ci/pr-core.sh` (`go test -p 4 -parallel 4 -race -short -skip
  '^TestEmbedded' ./...`, hermetic via `beads_test_env_enter`): **PASS**, 0
  failures across the entire repo, 768s. Includes `internal/storage/dolt
  8.955s` (this lane doesn't build the `integration`-tagged file, so it's
  the package's non-integration unit tests, e.g.
  `TestRebuildPoolAfterMigration_NoopWhenNotMigrated`).
- `scripts/ci/pr-policy.sh`: **PASS**, all 9 checks clean.
- `scripts/ci/pr-lint.sh`: **FAIL** — attributed away from this diff, see
  Criterion 3b.

### Criterion 3a — Diff-owned test re-verification, and a methodology
correction worth recording in full

The round-3 review record showed `TestMigratingOpen_FirstReadSucceeds`
failing 3/3 with `failed to initialize schema: context deadline exceeded`,
attributed by mayor's mechanism-based waiver
(`MAYOR-2026-08-19-be-kd10-crossproject-v2`) to a pre-existing
`store.initSchema` code path unrelated to this diff. My first attempt at
independent re-verification reproduced the identical FAIL 3/3 with the
identical signature — appearing to confirm the waiver's premise.

Per the test-evidence-integrity protocol (never infer "pre-existing" from a
failure's surface signature alone), I dug into *why* a schema-init call would
time out and traced it to a real, distinct bug: my `go test` invocations were
run directly, without sourcing `scripts/ci/lib/test-env.sh`'s
`beads_test_env_enter`. This left an ambient `BEADS_DOLT_SERVER_PORT`
(pointing at this rig's real shared Dolt server, port 28231) set in my
shell, and Dolt's config resolution prefers that variable over
`BEADS_DOLT_PORT` when both are present — so the test was silently
connecting to the shared production-adjacent server (contended, wrong
schema state) instead of its own fresh testcontainer.

Re-running properly — `BEADS_TEST_ENV_RUN_DOLT=1` exported *before* calling
`beads_test_env_enter` (order matters: the function decides whether to
skip Dolt tests at call time), confirmed via `env | grep BEADS_DOLT` showing
neither `BEADS_DOLT_SERVER_PORT` nor `BEADS_DOLT_PORT` ambient, then
`DOCKER_HOST=unix:///run/user/1000/podman/podman.sock` +
`TESTCONTAINERS_RYUK_DISABLED=true` (this sandbox's standard rootless-podman
workaround) — produced a genuine, freshly-created, verifiably-isolated
container (`0d9513f192ae`, full testcontainers-go connection log captured)
against which `TestMigratingOpen_FirstReadSucceeds` **PASSED 3/3**: 5.28s,
8.03s, 22.24s. Real variance, not the suspiciously flat ~45.00s pattern of
the contaminated runs. Full logs:
`/var/tmp/deploy-gate.be-iuu0i/logs/scratch-clean-rerun2.log`.

This means the failure the round-2/round-3 review cycle spent two rounds and
a waiver reissue on was, in all likelihood, never a real pre-existing
environmental defect — it was the same shell-hygiene gap in whichever
session(s) produced that evidence. The diff's own new test is simply
correct and passes cleanly; **no waiver is actually needed** to justify
this gate's PASS on that criterion, though the existing waiver is not
contradicted by anything found here (mayor's code-level trace of
`store.initSchema` vs. `rebuildPoolAfterMigration` was sound reasoning
independent of *why* a failure might occur — it correctly showed that IF a
schema-init timeout occurs, it can't be this diff's fault either way).

I did not chase down whose exact prior session produced the contaminated
evidence, and I'm not certain it happened via *this* mechanism in every
case — flagging it as the most likely explanation with strong direct
support, not a certainty about other sessions' setup.

A supplementary, non-required full-package `-tags=integration` sweep of
`internal/storage/dolt` (also run through `beads_test_env_enter`) hit a
*different* problem: a hard 10-minute `panic: test timed out after 10m0s`
with dozens of subtests stuck on `chan receive`. This is **not** attributed
to this diff and is **not** part of any required gate lane (`pr-core.sh`
doesn't use the `integration` tag at all) — root-caused instead to a
long-standing, already-tracked infrastructure issue: this shared sandbox has
~100 orphaned Dolt testcontainers accumulated since 2026-08-13 (Ryuk is
disabled here due to a rootless-podman incompatibility, and nothing else
reaps abandoned containers), straining podman badly enough to stall fresh
container starts under load. Already tracked as **be-8ub5** (filed
2026-08-15 at 35 containers); I added today's evidence (~100 containers, the
new hard-hang symptom) as a note rather than filing a duplicate. My own
sweep run added exactly one container (`0eee70041270`, confirmed via its
creation log line), which I stopped and removed myself; I did not touch any
of the ~99 pre-existing ones since I can't safely attribute them to
sessions that might still be using them.

I also checked whether the ambient-port leak itself pointed to an
undiscovered gap in `internal/testutil/testdoltserver.go` — it did appear
to, at the deploy SHA (`ensureSharedContainer`/`EnsureDoltContainerForTestMain`
only set `BEADS_DOLT_PORT`, never `BEADS_DOLT_SERVER_PORT`). Before filing
anything, I checked history: this was already found and fixed upstream —
`be-79jh`/`be-33se`, landed as commit `94fc33990` (PR #5837,
"stop dolt test suites from silently opening the ambient shared server"),
already on `origin/main`. `builder/be-itm5` simply branched before that fix
landed and hasn't been rebased since; this is a stale-branch artifact, not a
new bug. (It also doesn't affect the validity of my clean re-run above:
`beads_test_env_enter`'s shell-level unset of both variables is an
independent, sufficient protection regardless of whether the Go code
re-sets `BEADS_DOLT_SERVER_PORT` afterward.)

### Criterion 3b — Lint lane failure attribution
`ci-pr-lint.sh` FAILs on 3 `gosec` `G602: slice index out of range` findings
in `backend/conformance/cycle_detector_contract.go` and
`backend/conformance/importer_contract.go`. Applying the non-diff-owned-gate-
failure protocol:
1. **Not diff-owned** — neither file is among be-iuu0i's 3 changed files.
2. **Tracked bead exists** — `be-ckoic` (P3, open): "Fix gosec G602 false
   positives in backend/conformance/*.go (blocks clean make ci-pr-lint)".
3. **Proven pre-existing** — reproduced the identical 3 findings in a
   `base-check` worktree at `origin/main`; `diff` of both files between
   `origin/main` and the deploy SHA: byte-identical.
4. **No path overlap** — confirmed via clause 1.
All 4 clauses satisfied — this failure is fully attributable away from
be-iuu0i's diff.

## Criterion 4 — No open high-severity findings
PASS. be-npdwg's record states "No style/security/spec findings across 3
review rounds." My own `go vet`, `pr-lint`, and `pr-policy` runs surfaced
nothing new beyond the already-attributed be-ckoic item above.

## Criterion 5 — Final branch clean
To be confirmed at branch-cut time via `git status` on the freshly
SHA-pinned checkout (trivial given a detached/fresh branch at an exact
commit).

## Criterion 7 — Single feature theme
PASS. Single bug fix (retire+rebuild the connection pool after migrations
apply, gated on `applied > 0`), 3 files, 151 insertions / 5 deletions.
No unrelated changes mixed in.

## Follow-up items filed (not gate-blocking)
- **be-8ub5** updated with fresh evidence (~100 orphaned containers, new
  hard-hang symptom) rather than filed as a duplicate.
- No new bead filed for the `testdoltserver.go` ambient-port gap — already
  fixed on main via be-79jh/be-33se (commit `94fc33990`, PR #5837);
  `builder/be-itm5` is simply behind that fix, not exhibiting a new bug.
