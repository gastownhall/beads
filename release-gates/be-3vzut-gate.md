# Release gate — be-3vzut (Fix: TestStartDetachedReapsExitedChild fails on a transient zombie, flaking ~14% and reddening unrelated PRs)

**Date:** 2026-09-04
**Deployer:** beads/deployer
**Bead (deploy):** be-3vzut
**Source bead:** be-5gizx — review verdict PASS (style/security/spec findings: none/none/none); build bead `be-kh65q`; root-caused by investigator bead `be-heifp` (PR #6147 triage)
**Source commit:** `7d53c890623e75e4c2e2748e3eb1167ba39db9f2` (parent `c0d8da42de5fd15c95adac85e342ba4a121da0fb` = origin/main's own tip — zero divergence, no rebase needed)
**Branch:** `deploy/be-3vzut-gate`, cut directly at the reviewed SHA via `resolve_deploy_branch_target be-3vzut 7d53c890623e75e4c2e2748e3eb1167ba39db9f2`
**Push target:** `headfork` (`quad341/beads-sec003-contrib`) — `origin` push-disabled by design (`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`). Pushed and independently verified: `git ls-remote headfork refs/heads/deploy/be-3vzut-gate` → `7d53c890623e75e4c2e2748e3eb1167ba39db9f2`, matches local HEAD exactly.
**PR:** PENDING — to be opened against `gastownhall/beads:main` from `quad341/beads-sec003-contrib:deploy/be-3vzut-gate`

## Verdict: 7/7 — criterion 7 (ancestry scope) via bounded mayor waiver, all others raw PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-5gizx: VERDICT PASS. style_findings: none (gofmt/vet independently re-run clean). security_findings: none (diff is test-only, `internal/metrics/spawn_reap_test.go`; `spawn.go` diff empty — confirmed zero production code touched). spec_findings: root cause and fix independently re-verified by reviewer, not just trusted from builder/investigator reports. |
| 2 | Acceptance criteria met | PASS | Diff matches the bead's stated fix theme exactly: removes the in-loop early-fail check and its now-unused `st` binding, adds a comment documenting the kernel zombie-retention window vs. poll-spacing rationale. Matches investigator's (be-heifp) verbatim patch per be-5gizx's review. |
| 3 | Tests pass | PASS | Full-suite `make test` re-run at the exact deploy SHA this round (see "Criterion 3" below for counts). Diff-owned regression-catch independently re-verified by both reviewer and deployer: reverting `startDetached`'s reap to the pre-GH#5900 path fails 3/3 with "still present after 3s"; reverting back passes clean. Before/after under load: 13/90 → 0/90 failures. |
| 4 | No HIGH-severity findings open | PASS | be-5gizx security_findings: none. Test-only diff, no external input/auth/secrets/PII/injection surface, no OWASP-relevant change. |
| 5 | Final branch is clean | PASS | `git status --short` clean on `deploy/be-3vzut-gate` post-cut. |
| 6 | Branch diverges cleanly from main | PASS | Cut directly from the reviewed SHA, whose parent is already `origin/main`'s current tip (`c0d8da42d`) — confirmed via fresh `git fetch origin main` this round, no upstream movement since the commit was authored. Zero divergence; no rebase required. |
| 7 | Single feature theme / ancestry scope | **WAIVED** (mayor ruling `gm-wisp-9mtr84`) | `assert_deploy_ancestry_scope origin/main 7d53c890623e75e4c2e2748e3eb1167ba39db9f2 be-3vzut be-kh65q be-5gizx be-heifp` → **REFUSED, rc=21**: the sole commit cites none of the accepted bead ids. See "Criterion 7 — waiver" below. |

## Criterion 3 — full-suite test evidence

`test_cmd: make test` (full `./...` scope, `TEST_COVER=1`), run at the exact pushed deploy SHA `7d53c890623e75e4c2e2748e3eb1167ba39db9f2` on `deploy/be-3vzut-gate`. **No `TMPDIR`/`GOTMPDIR` override** — the prior draft's `TMPDIR=~/.gotmp GOTMPDIR=~/.gotmp` note was a stale standing-workaround memory; `bd memories`/`bd remember` confirms it was corrected 2026-08-24 and pointing the Go build cache/tmpdir at a tmpfs path is now actively blocked by a cairn guard (`gocache-on-tmp`, not ackable — a cold cache there is 2-3GB and ENOSPCs the shared fleet). Current guidance is to set neither var; defaults already route correctly. `BEADS_TEST_ENV_RUN_DOLT` correctly left unset: this diff touches only `internal/metrics`, no Dolt code (`engdocs/TESTING.md:73`, `scripts/README.md:27`).

Run twice at that SHA:
1. `make test` — the documented canonical command, verbatim. Exit 0. Zero `FAIL`, zero `panic`, across all 97 `ok` packages (remaining packages have no test files / no statements).
2. `TEST_COVER=1 ./scripts/test.sh -v` — byte-identical invocation (it's what the `make test` Makefile target itself runs) with `-v` added solely to recover real per-test PASS/FAIL/SKIP counts; same `./...` scope, timeout, skip pattern, and env setup as run 1.

- `test_cmd_scope: full-suite`
- `test_counts: 6348 PASS, 0 FAIL, 2104 SKIP`
- `diff_tests_executed: TestStartDetachedReapsExitedChild PASS (0.51s)` — the diff's only changed file is `internal/metrics/spawn_reap_test.go`, and it declares exactly one `func Test*` (confirmed via `git diff --name-only HEAD~1...HEAD` and `grep '^func Test'`). Cross-checked with an isolated `go test -v -run '^TestStartDetachedReapsExitedChild$' ./internal/metrics/...` — 0.51s, PASS, identical result. Every other test in the diff's own package (`internal/metrics`, 43 tests total) also PASSed with none skipped — checked by name, not inferred from the package's bare `ok` line.
- `skip_justification`: all 2104 SKIPs are pre-existing, non-diff-owned, opt-in integration gates — none in `internal/metrics` or any file this diff touches. By reason text: the dominant class (1043) is the documented default `BEADS_TEST_SKIP=dolt` (Dolt-backed tests are opt-in only via `BEADS_TEST_ENV_RUN_DOLT=1`, correctly left unset here). Most of the remainder (~1000) is the same shape one level up — `BEADS_TEST_EMBEDDED_DOLT=1` / `BEADS_TEST_PROXIED_SERVER=1` / `BEADS_TEST_PROXIED_LOCAL=1` opt-in integration suites, plus "Dolt test server/container not available" (no local Dolt fixture running for this plain run). A small tail (<20) is unrelated pre-existing platform/feature gates (`test requires btrfs`, `skipping E2E test: running as test binary`, `UntrackedJSONL removed as part of JSONL removal (bd-9ni.2)`) — none touch `internal/metrics`.
- `ci_lane_run: n/a (no CI-config change in this diff)`
- `failure_attribution: n/a` — zero FAIL anywhere in either run; the non-diff-owned-gate-failure protocol was not needed.

Diff-owned regression-catch independently re-verified by both reviewer and deployer: reverting `startDetached`'s reap to the pre-GH#5900 path fails 3/3 with "still present after 3s"; reverting back passes clean. Before/after under load: 13/90 → 0/90 failures.

## Criterion 7 — waiver

`assert_deploy_ancestry_scope` REFUSED (rc=21): the sole non-merge commit in `origin/main..7d53c890623e75e4c2e2748e3eb1167ba39db9f2` cites none of `[be-3vzut, be-kh65q, be-5gizx, be-heifp]` as a literal substring in its message — it reads as a missing bead-id trailer, not as an unrelated theme riding along (the be-27c shape the check exists to catch). Independently confirmed before escalating:

- Exactly one non-merge commit in the range; its parent is exactly `origin/main`'s tip.
- Its diff (`internal/metrics/spawn_reap_test.go | 15 +++++++++++----`, 1 file, +11/-4) is byte-for-byte the same range be-5gizx's review recorded as evidence (`c0d8da42de..7d53c890c` — "1 files, +11/-4").
- Denylist check (`.claude/**`) clean.

Per this repo's SHA-integrity discipline, the reviewed commit was not amended to add a trailer (that would change its SHA and undermine pushing exactly the reviewed content), and the gate was not self-certified past. Escalated to mayor instead (`gm-wisp-ffo4a5`, thread `thread-32babcba2edf`), holding with the bead claimed and not proceeding to branch/push/PR pending reply.

**Mayor's ruling** (`gm-wisp-9mtr84`, 2026-09-04T04:26:35Z): bounded diff-identity waiver. Full reasoning (verbatim from the ruling):

> The ancestry-scope gate's PURPOSE is anti-contamination — ensure the deploy range equals the reviewed work. You have proven that PURPOSE directly and more strongly than the gate's own mechanism tests it: the sole commit's diff is byte-identical to be-5gizx's reviewed range (spawn_reap_test.go, +11/-4), exactly one non-merge commit, parent is origin/main's tip, denylist clean. The bead-id trailer is a PROXY for "this is the reviewed work"; you have the thing the proxy stands in for. Satisfying a gate's intent by stronger evidence than its proxy is not self-certification.
>
> This waiver is BOUNDED and does not weaken the gate: it applies ONLY because direct diff-identity to the reviewed range is proven. The contamination case the gate exists to catch (an unrelated commit in the range) can never produce that proof, so nothing about this ruling helps a real contamination slip through. Do NOT generalize it to a trailerless commit that lacks diff-identity proof.

Mayor also confirmed the hold-and-escalate decision (rather than amending the commit, or self-certifying) was correct, and that deploying the exact reviewed SHA — rather than amending it to add a trailer, an alternative mayor considered and rejected — preserves the SHA-identity invariant; the be-3vzut/be-5gizx/be-kh65q/be-heifp bead chain already establishes traceability internally, and the bead ids mean nothing to the upstream `gastownhall/beads` maintainers who will review the PR.

**This waiver applies only to this deploy**, on the specific basis of proven diff-identity to be-5gizx's reviewed range. It does not generalize to any future trailerless commit lacking that same proof — per mayor's ruling, those must still be sent back for a trailer or separately escalated on their own evidence.

## Merge authority

`gastownhall/beads` is contributor-only for this rig — no rig agent has merge access. Per established precedent (be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3, be-vc1m [PR #5792], be-7q688 [PR #6003], be-6iglh/be-0l89e [PR #6082], be-c8kgv [PR #6221], be-1wwre [PR #6247]), the deployer's job ends at the open, verified PR regardless of gate outcome. No merge-request is routed to mayor.

## Disposition

7/7 — criterion 7 via bounded, auditable mayor waiver (not a self-granted bypass), all others raw PASS including a real full-suite test run at the exact deploy SHA. Proceeding to PR-open per contributor carve-out; no merge-request routing needed.
