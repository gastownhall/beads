# Release gate — be-vc1m (Extract PURGE dropped-database theme onto clean branch off origin/main)

**Date:** 2026-08-21 (resolving re-evaluation; original evaluation 2026-08-15)
**Deployer:** beads/deployer
**Bead (deploy):** be-vc1m — Deploy Review: Extract PURGE dropped-database theme onto clean branch off origin/main
**Source bead:** be-0x72 — closed, review verdict: mayor-authorized conditional PASS (gm-wisp-kq6kcu), criterion 3 explicitly left UNVERIFIED by the reviewer, not passed, not waived
**Source commit:** `e5bdf5945634517355508a7bf62f9d354913ee52` (provenance branch `builder/be-auu.1`, review bead be-0x72, molecule be-l1w8) — unchanged since 2026-08-15; only the rebase parent moved
**Branch:** `deploy/be-vc1m-gate` — recreated fresh this round from the raw reviewed SHA (see "Branch recreation" below)
**Base:** `origin/main` @ `1617f3a85cec67ad0f78ea1b8217bd3f1e00095d` (was `7505e173f` at original evaluation — six days of upstream movement, including the CI-wiring fix this gate was waiting on)
**Self-rebase:** `PUSH_REMOTE=headfork attempt_bounded_self_rebase deploy/be-vc1m-gate main` → rc=0. `BEFORE_SHA=e5bdf5945634517355508a7bf62f9d354913ee52`, `AFTER_SHA=d0ee74ba9883e54e9ae543a9a14d6f972ac1acb7`. Push landed on `headfork`, independently verified (`git ls-remote headfork refs/heads/deploy/be-vc1m-gate` matches local HEAD exactly). `git merge-base HEAD origin/main` now equals `origin/main`'s own tip — zero divergence. (Round 1 only needed a `git merge-tree --write-tree` dry-run simulation, since the branch was already a clean fast-forward then; this round required and completed a real, pushed self-rebase.)
**PR:** https://github.com/gastownhall/beads/pull/5792 (`quad341/beads-sec003-contrib:deploy/be-vc1m-gate` → `gastownhall:main`), state OPEN, `headRefOid` = `d0ee74ba9883e54e9ae543a9a14d6f972ac1acb7` (matches freshly-pushed HEAD), `mergeable: MERGEABLE`. Same PR throughout — not reopened.

## Verdict: PASS 7/7 — criterion 3 now has a real, positive CI result; bead remains on `hold:mayor` pending explicit sign-off (see Disposition)

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-0x72: mayor-authorized conditional pass (gm-wisp-kq6kcu). Style clean (gofmt, go vet, golangci-lint incl. Windows cross-lint: 0 issues). Security clean (0 blockers/majors/minors). Unchanged since original review — source commit `e5bdf5945` itself is untouched; only its rebase parent moved. |
| 2 | Acceptance criteria met | PASS | Diff matches the bead's stated theme exactly. Same 4 commits as original evaluation (`e5bdf5945`, `a74d92cb3`, `29538d684`, `394cf3859`), same tree content — only the rebase parent changed. |
| 3 | Tests pass | **PASS** | Real CI, real Docker infra, this round's rebased head `d0ee74ba9`. See "Criterion 3 — resolution" below for the full evidence chain. |
| 4 | No HIGH-severity findings open | PASS | be-0x72 security_findings unchanged: no blockers/majors/minors. Diff content unchanged since original review. |
| 5 | Final branch is clean | PASS | `git status --short` clean on the recreated `deploy/be-vc1m-gate` post-rebase. |
| 6 | Branch diverges cleanly from main | PASS (real self-rebase this round) | `PUSH_REMOTE=headfork attempt_bounded_self_rebase deploy/be-vc1m-gate main` → rc=0, push landed and independently verified. `git merge-base HEAD origin/main` == `origin/main` tip. |
| 7 | Single feature theme | PASS | Same 4 commits, all scoped to the dolt-purge/testutil theme. `assert_deploy_ancestry_scope origin/main e5bdf5945634517355508a7bf62f9d354913ee52 be-eh6 be-pq5` → rc=0 this round — no stray commits, no scope drift. |

## Criterion 3 — empirical CI investigation

### Original investigation (2026-08-15) — FAIL, SKIP only

The bead's hard precondition required a **real CI result** for `TestBenchDBPurgeDoesNotLeak` on this exact commit before criterion 3 could be scored at all — no conformance-audit substitute, no re-run in an already-proven-broken sandbox, no self-granted waiver. That precondition existed because PR #5339 previously failed the gate exactly this way: a conformance audit scored criterion 3 PASS while the actual new test came back as the run's sole SKIP.

The diff-owned test executed in exactly one CI lane at the time (`Test (macos-latest)`), and the result was **SKIP** ("no test Dolt server running") — zero PASS, zero FAIL anywhere in the PR's CI. No PR-triggered lane both kept a live Dolt server and included this test in its selection. This was root-caused as a real CI-wiring gap (tracked as be-aiy5 after a separate red herring, be-zi3i, was closed) — not a defect in the diff itself. Per the bead's own hard precondition, this was reported as an unresolved FAIL and escalated to mayor rather than improvised around.

### Resolution (2026-08-21) — PASS, real result obtained

The CI-wiring gap was fixed and shipped upstream: be-aiy5's fix was reviewed as be-w90n (verdict PASS) and shipped as PR #5836, which merged into `gastownhall/beads:main` on 2026-08-20, adding a new `test-server-storage-full` job (16 shards, displayed in the Actions UI as "Test (Server Dolt Full Suite N/16)") to `.github/workflows/pr-risk.yml` — specifically to cover this class of test against a real, live Dolt server.

Getting that new job to actually execute against this PR required rebasing `deploy/be-vc1m-gate` onto post-#5836 `origin/main` and re-pushing so CI would pick up the new job definition. That in turn was blocked by an unrelated infra bug: `attempt_bounded_self_rebase`'s push step was hardcoded to the disabled `origin` remote instead of honoring `$PUSH_REMOTE` (be-902xz, fixed by commit `f195bbdbe`) — and that fix itself had not yet landed on `gc-management` main despite an earlier claim that it had (be-z3iuv). be-z3iuv was independently re-verified and closed this session (fresh `grep` on the canonical script plus `git merge-base --is-ancestor f195bbdbe HEAD` against `gc-management` main's current tip, not trust in the earlier claim or in this bead's own stale notes), which finally cleared the path.

With the blocker gone, `deploy/be-vc1m-gate` was recreated from the raw reviewed SHA and self-rebased for real (see header) — the first successful push of this branch since the saga began 2026-08-15. That triggered a fresh CI run (`gastownhall/beads` Actions run `32510110772`, "PR Risk", triggered by the push to `d0ee74ba9`).

Direct evidence, gathered after the run reached `conclusion: success`:

- All 16 `test-server-storage-full` shards passed; zero failures across the shard set.
- `TestBenchDBPurgeDoesNotLeak` ran in shard 4/16 (job `96860162658`, job-level `conclusion: success`). Verbose log, verbatim:
  ```
  === RUN   TestBenchDBPurgeDoesNotLeak
  --- PASS: TestBenchDBPurgeDoesNotLeak (24.90s)
  ```
  No `PAUSE`/resume, no nested subtests, no SKIP anywhere near it — it ran serially to completion in 24.90s (consistent with genuine purge + leak-detection work, not a stub) and passed outright.
- Zero `--- FAIL` or `panic:` lines anywhere in shard 4/16's full log.
- This is an explicit, positive, verbose-mode PASS for the exact diff-owned test, on real CI/Docker infrastructure, at the exact commit being gated (`d0ee74ba9`, which is a straight rebase of the reviewed tree `e5bdf5945` — no source-line changes). Not a substitute, not an inference from job-level success alone, not a re-run in a sandbox. This is the class of evidence mayor's round-42 ruling required.

## Branch recreation (this round)

Local `deploy/be-vc1m-gate` had been silently reset to `origin/main` by a between-session freshen, sitting at a stale, unusable snapshot (`d38ac728b581c8595fae36344ecca68830c7f3b5`). Remote `headfork` still held the old pre-fix tip (`60fe7dec990d6f34d37e256ad014cfb8bbe7ddfd` — the same SHA recorded as `headRefOid` throughout rounds 1–71, and the commit that carried the original, now-superseded copy of this gate file). Recreated via `resolve_deploy_branch_target be-vc1m e5bdf5945634517355508a7bf62f9d354913ee52`, re-ran `assert_deploy_ancestry_scope` fresh (rc=0), then re-attempted the self-rebase — which succeeded this time now that be-z3iuv is resolved.

## Push target

`origin` denies push (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`, confirmed via dry-run this round, rc=128); `headfork` accepts — confirmed by the successful push landing this round.

## Merge authority

`gastownhall/beads` is contributor-only for this rig — no rig agent has merge access. Per established precedent (be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3, be-2ym7w, be-g4zox, and this bead's own prior rounds), the deployer's job ends at the open, verified PR regardless of gate outcome. No merge-request is routed to mayor/mpr.

## Disposition

All 7 criteria now PASS, including criterion 3 with a real, positive, unambiguous CI result — the exact evidence mayor's round-42 ruling required ("no substitute, no waiver... a SKIP is not a PASS"). The PR (#5792) is open, current, and verified.

This bead carries `hold:mayor` (set explicitly by mayor's round-42 ruling, given the PR #5339 history of a wrongly-accepted substitute on this exact criterion) and `human`. Both predate this round and were placed deliberately, not as routine process. Per that standing hold, this gate result is being reported to mayor for explicit sign-off rather than treated as a routine autonomous close — the deployer is not lifting a hold mayor personally imposed, even with a clean result in hand. See mail to mayor for the full report.

## Criterion 3 — correction (2026-08-24, PR #5792 review round)

**The criterion-3 PASS recorded above was real but evidentially empty, and is
retracted as evidence.** It is left in place above rather than edited out,
because what happened is the point.

Review of PR #5792 (bee-ghosttrack, 2026-08-21) established that
`TestBenchDBPurgeDoesNotLeak` **could not fail** at `d0ee74ba9`. Its leak probe
ran `find / -xdev …` inside the Dolt container, but the image declares
`VOLUME /var/lib/dolt`, so the data dir is a separate mount and `-xdev` stops
the walk before reaching it. The probe returned `""` on every run, the entry
count was always `0`, and the assertion was always `0 > 0`. Confirmed
empirically in a live container:

```
stat -c "%d %n" / /var/lib/dolt        ->  84 /
                                           37 /var/lib/dolt
find / -xdev -maxdepth 6 -type d -name .dolt_dropped_databases   ->  (nothing)
find /       -maxdepth 6 -type d -name .dolt_dropped_databases   ->  /var/lib/dolt/.dolt_dropped_databases
```

with the directory demonstrably present. The reviewer independently confirmed
the consequence by deleting the `DOLT_PURGE_DROPPED_DATABASES` call from
`dropBenchDB` and watching the test still pass.

So the shard-4/16 `--- PASS ... (24.90s)` above is a genuine CI result that
distinguishes nothing: it reads identically whether PURGE works or is absent
entirely. Mayor's round-42 standard ("a SKIP is not a PASS") has a sibling that
this gate missed — **a PASS that cannot fail is not evidence either**. The
24.90s runtime was read at the time as "consistent with genuine purge +
leak-detection work, not a stub"; it was in fact the five create/cleanup cycles
doing real work while the measurement of them was inert. Wall time is not
evidence of a live assertion.

### What the criterion now rests on

Fixed under this review round (commits `fdd1ee319`, `05afbb406`, `56a98552f`,
`33b09eb85`, `675b0a8a6`). Criterion 3 rests on a demonstrated red/green, not
on a green alone:

| Condition | Result |
|---|---|
| Head as fixed | `--- PASS: TestBenchDBPurgeDoesNotLeak (20.60s)` |
| `DOLT_PURGE_DROPPED_DATABASES` removed from `dropBenchDB` | `--- FAIL` — `dolt_dropped_databases grew from 0 to 5 across 5 setup/cleanup cycles` |
| `-xdev` re-introduced into the probe (fault injected into the probe itself) | `--- FAIL (2.03s)` — `the probe cannot observe a leak, so this test could not fail and proves nothing about dropBenchDB` |

The third row is the durable part: the test now carries an in-band positive
control that drops a database without purging and requires the count to rise
before it will assert the count stays flat. A future regression of this exact
class fails loudly on every run instead of passing silently, so criterion 3
does not depend on anyone re-running this experiment by hand.
