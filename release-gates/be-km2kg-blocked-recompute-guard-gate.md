# Release gate: be-km2kg — Blocked-recompute guard fails against real Dolt server

**Deploy bead:** be-km2kg
**Build bead:** be-mge0b (closed, fix: hermetic peer-branch naming in `TestMergeRecomputesIsBlocked`)
**Review bead:** be-dbpsx (closed, reason: pass — `=== REVIEWER VERDICT: PASS ===`)
**Source branch:** `builder/be-mge0b` (provenance only, not a push target)
**Deploy branch:** `deploy/be-km2kg-gate`
**Push target:** `headfork` (`quad341/beads-sec003-contrib.git`) — already pushed by the builder's rebase-recovery step; independently confirmed via `git ls-remote` against both `headfork` and `fork` this session
**Deploy commit:** `3e1a002a35c133115d00496c280d5bb05ac70cf9`

## Provenance note: rebase recovery

The originally reviewed commit (`160deb6011369c182ea24d225475d41afc819da9`) hit a stale-base
conflict at deploy time against just-merged PR #5836 content in `scripts/ci_workflow_test.go`
(non-diff-owned to this bead's reviewed scope: `internal/storage/dolt/blocked_merge_test.go`,
`TestMergeRecomputesIsBlocked` only). The builder rebased `builder/be-mge0b` cleanly onto
`origin/main`, producing `3e1a002a35c133115d00496c280d5bb05ac70cf9`. `blocked_merge_test.go`
is verified byte-identical to the reviewed/PASSED `160deb601` content — untouched by the
conflict resolution, which was entirely in the non-diff-owned CI-wiring file.

## Criteria (0–7, in mandated order)

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 0 | Pre-flight: already merged? | PASS | `git merge-base --is-ancestor 3e1a002a3 origin/main` → not an ancestor. `gh pr list --search` on be-km2kg/be-dbpsx/both SHAs against `gastownhall/beads` → empty. Not merged, no duplicate PR. |
| 1 | Review PASS present | PASS | be-dbpsx CLOSED, close reason `pass`, notes contain explicit `=== REVIEWER VERDICT: PASS ===`. |
| 2 | Acceptance criteria met | PASS | Review evidence: real-Dolt-server run on `160deb601`, 12 PASS / 0 FAIL / 0 SKIP, covering all 5 #5848 acceptance-criteria tests (repair/dirty-detect/merge) including the diff-owned `TestMergeRecomputesIsBlocked` (3.43s). |
| 3 | Tests pass (diff-owned SKIP = FAIL, no self-granted waivers) | **PENDING — real CI requested, not self-graded** | See "Criterion 3" section below. |
| 3a | Pre-existing-failure attribution | N/A | No failure is being attributed as pre-existing; this is a SKIP-with-real-CI-path situation, not a FAIL-attribution situation. |
| 3b | Policy/lint lane | PASS | `gc beads-contributor pre-pr-check`: 0 blockers, 0 warnings. No postgres tokens, no `.claude/**` paths, no undefined build tags, changed-file count 1, commit count 1. |
| 4 | No open HIGH findings | PASS | Review notes: "Security: test-only file, SQL hardened to parameterized calls, no findings." No other HIGH findings on record. |
| 5 | Branch clean | PASS | `git status --short` empty on `deploy/be-km2kg-gate` @ `3e1a002a3`. |
| 6 | Diverges cleanly from main | PASS | `assert_deploy_ancestry_scope origin/main 3e1a002a3 be-km2kg be-dbpsx be-mge0b` → rc=0 (no `.claude/**` paths, every commit cites an accepted bead id). pre-pr-check: "branch is 0 commit(s) behind origin/main." |
| 7 | Single feature theme | PASS | 1 file, 1 commit, entirely the peer-branch-collision hermeticity fix in `TestMergeRecomputesIsBlocked`. |

## Independent verification (this session, fresh on `3e1a002a3`)

```
gofmt -l internal/storage/dolt/blocked_merge_test.go   → (clean, no output)
go build ./...                                          → exit 0
go vet ./...                                             → exit 0
gc beads-contributor pre-pr-check                        → 0 blockers, 0 warnings
```

## Criterion 3 — full detail

The diff-owned test (`TestMergeRecomputesIsBlocked`) has a **real, completed PASS** on
byte-identical content, from the review phase: real-Dolt-server run, 12 PASS / 0 FAIL / 0 SKIP,
recorded on be-dbpsx. That run predates the rebase; the rebase changed only the parent commit
(re-parenting onto `origin/main`), not this file's bytes.

Re-attempting that same real-Dolt-server run in this deployer's sandbox, on the current checkout,
produces SKIP:

```
$ BEADS_TEST_ENV_RUN_DOLT=1 go test ./internal/storage/dolt/... -run TestMergeRecomputesIsBlocked -v
WARN: Docker image dolthub/dolt-sql-server:2.2.0 not cached locally, skipping Dolt tests
=== RUN   TestMergeRecomputesIsBlocked
    blocked_merge_test.go:231: Test Dolt server not running, skipping test
--- SKIP: TestMergeRecomputesIsBlocked (0.00s)
```

Root cause independently reproduced this session (not just cited from a tracked memory): a raw
`docker pull dolthub/dolt-sql-server:2.2.0` on this host fails with
`failed to get reader from content store: blob sha256:4f4fb700... not found` — corrupted
containerd content-store, tracked as `docker-containerd-content-store-corrupted-2026-08-20`.
This is host-level and commit-independent; it would SKIP identically on `origin/main` or any
other checkout on this host.

**This SKIP is not being scored as a PASS.** Mayor's explicit ruling on be-vc1m
(round 42, msg `gm-wisp-5vw1ga`), which addressed the structurally identical situation (a
diff-owned Dolt-container test unable to produce a real result in a rig sandbox), is general and
unambiguous: *"hold the gate, a SKIP is not a PASS, nobody is to fix this by loosening the gate,
no waiver is available."* Citing byte-identical prior-PASS evidence plus an independently-proven
environmental cause is exactly the kind of "more careful route to the same substitute outcome"
that ruling rejects (see also be-vc1m's PR #5339 precedent: a conformance-audit substitute scored
PASS while the actual new test SKIPped — rejected). No waiver is self-granted here.

**Unlike be-vc1m's original blocker, the underlying CI-lane gap is already fixed.** be-vc1m's
saga stalled because no PR-triggered GitHub Actions lane executed anything beyond
`^TestConformance$` from this package. PR #5836 (merged into `gastownhall/beads:main`
2026-08-20T03:39:38Z, mergeCommit `30f30ca95b9d177b84451ba16cf53e208aab4d5b`) fixed exactly this
gap by adding `test-server-storage-full` to `.github/workflows/pr-risk.yml`. Verified by direct
inspection of the merged workflow and script (not assumed):

- `full_embedded` tier-detection (`.github/scripts/ci-embedded-tier.sh`) sets `full_embedded=true`
  whenever the PR diff touches `internal/*` — this diff does (`internal/storage/dolt/blocked_merge_test.go`),
  so `test-server-storage-full` will run on this PR.
- `.github/scripts/server-storage-test-shard.sh` discovers **every top-level `Test*` function in
  `internal/storage/dolt/*_test.go`** (excluding `TestConformance`, which has its own lane) and
  shards them across 16 real-Docker jobs. `TestMergeRecomputesIsBlocked` lives in that same
  package/directory, so it will be reached and run against a real `dolt-sql-server` container on
  GitHub-hosted infrastructure — independent of this host's corrupted containerd store.

**Verdict on criterion 3: PENDING, not FAIL, not PASS.** The PR is being opened specifically to
obtain the real CI result this criterion requires, per this bead's own prose ("Run the standard
deploy gate... Open the PR from the isolated branch") and the established be-vc1m/mayor pattern.
Gate will be finalized (PASS 7/7, or FAIL if CI actually fails/still-skips) once that CI result is
in hand — tracked in bd notes on be-km2kg, not assumed here.

## Merge authority

`gastownhall/beads` is a contributor-only repo — no rig agent (including mayor) has merge access.
Per established precedent (be-vc1m, be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3), the
deployer's job ends at a verified PR; no clearance-status, no merge-request is routed, even though
this bead's own prose says to route one (deviated from deliberately, matching precedent). This
gate additionally holds the bead open (not closed) pending the criterion-3 real-CI result before
even that verified-PR endpoint is reached.
