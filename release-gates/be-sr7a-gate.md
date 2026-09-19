# Release Gate: be-sr7a — cmd/bd capture helpers leak os.Stdout on t.Fatal, poisoning later tests with EPIPE

- **Deploy bead:** be-sr7a
- **Review bead:** be-1kxq (verdict: pass)
- **Builder bead / branch (provenance only):** be-leuf / `builder/be-leuf`
- **Repo:** gastownhall/beads
- **Deploy commit:** `c0b7f865b28dbbcc658f85c9e8b1219c7fcef725`
- **Deploy branch:** `deploy/be-sr7a-gate` (cut from the commit above)
- **Base:** origin/main @ merge-base of the deploy commit (zero divergence — see criterion 6)
- **Evaluated:** 2026-08-15 by beads/deployer

## Scope

`cmd/bd`'s stdout-capture test helpers leaked the real `os.Stdout` when a
captured subtest called `t.Fatal`: the helper's deferred restore never ran
because `t.Fatal` unwinds via `runtime.Goexit`, not a normal return, so the
process-level `os.Stdout` stayed pointed at the (now-closed) capture pipe.
Later tests in the same process then wrote to a closed pipe and failed with
EPIPE ("broken pipe"), a flaky-looking failure with no relation to the test
that actually broke.

Diff, confirmed via `git diff --stat origin/main..c0b7f865b` (14 files, all
`cmd/bd/*_test.go` — test-only, no production code changed):

- `bca2d94e0` (red): adds a regression test that reproduces the leak.
- `c0b7f865b` (green): fixes the capture helper to restore `os.Stdout` via
  `t.Cleanup` instead of a plain `defer`, so restoration runs even when
  `t.Fatal` calls `runtime.Goexit` — plus the associated helper/assertion
  test-file updates.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 6 | Clean divergence from `origin/main` (checked first) | **PASS** | `origin/main` is literally the merge-base of `c0b7f865b` — zero commits of divergence, no self-rebase needed. |
| 1 | Review PASS present | **PASS** | be-1kxq verdict: pass; reviewed commit matches `c0b7f865b28dbbcc658f85c9e8b1219c7fcef725` exactly. |
| 2 | Acceptance criteria met | **PASS** | All 8 "Done-when" items from be-leuf cross-referenced against the diff and this gate's own independent test run (see below); reviewer's `diff_review` independently reported 100% match, no deviations, no scope creep. |
| 3 | Tests pass | **PASS** | See "Tests run" below. |
| 3b | Policy/lint lane (`make ci-pr-policy`) | **PASS** | See "Policy lane" below. |
| 4 | No unresolved HIGH findings | **PASS** | be-1kxq reports no security findings, no HIGH findings. Diff is test-only (capture-helper lifecycle fix), consistent with that assessment. |
| 5 | Clean branch | **PASS** | `git status --porcelain=v1` on `deploy/be-sr7a-gate` is empty aside from 3 pre-existing untracked files belonging to *other* sessions' work (`release-gates/be-hi97-no-workspace-tests-gate.md`, `release-gates/be-uoat-dolt-diff-export-gate.md`, `scripts/rebase-resolve-lib.sh` — a known-crippled duplicate of the canonical library used to evaluate this gate) — not staged, not part of this branch's history, deliberately left untouched for their owning sessions. |
| 7 | Single feature theme | **PASS** | 14 files, all `cmd/bd/*_test.go` — one bug (stdout-leak-on-Fatal), one fix, test-file-only. `assert_deploy_ancestry_scope origin/main c0b7f865b be-sr7a be-leuf` → rc=0 (both commits cite `be-leuf`, the builder bead this deploy bead's own description names as source). |

## Tests run

Independently re-verified rather than trusted from be-1kxq's report, with
diff-owned tests resolved **by name**.

Command (matching the builder/reviewer's own documented, justified
invocation for this package — a real Dolt server isn't available in this
sandbox):

```
DOCKER_HOST=... TESTCONTAINERS_RYUK_DISABLED=true BEADS_TEST_SKIP=dolt \
  scripts/test.sh -v -count=1 ./cmd/bd/...
```

Result: **2389 PASS, 0 FAIL, 844 SKIP, 0 panics.** All 844 skips are
Dolt-backend tests skipped via `BEADS_TEST_SKIP=dolt` (no Dolt server in
this sandbox) — environmental, not diff-owned; none are silent, all are
explicit `--- SKIP:` lines tied to the same documented, justified cause. All
5 packages under `./cmd/bd/...` report `ok`: `cmd/bd` (137.0s),
`cmd/bd/doctor` (1.7s), `cmd/bd/doctor/fix` (0.4s), `cmd/bd/protocol`
(9.5s), `cmd/bd/setup` (0.1s).

**Diff-owned tests** (the actual regression coverage for this fix), each
confirmed **PASS** by name:

- `TestCaptureCycleStdoutRestoresOnFatal` — PASS
- `TestZZStdioNotLeaked` — PASS
- `TestRunSyncCommandExitCodeMapping` — PASS, all 3 subtests: `ok`,
  `conflict`, `retries-exhausted` — PASS

**Direct proof the leak is fixed** (done-when item 8): `grep -ci "broken
pipe"` and `grep -ci "EPIPE"` on the full verbose log both return **0** —
no downstream test observed a closed-pipe write anywhere in the run.

**Out-of-scope, informational only** (not part of this fix's acceptance
criteria, explicitly not gating): `TestDepCyclesNamesTheMembersItCannotDescribe`
and `TestDepCyclesSaysNothingIsWrongForACleanWorkspace` — both also PASS,
noted for completeness only.

## Policy lane (`make ci-pr-policy`)

Initial run failed on "check version consistency":
`.githooks/commit-msg: no 'BEGIN/END BEADS INTEGRATION' marker found` —
the exact same failure signature already root-caused during the be-3b4e
gate (see `release-gates/be-3b4e-gate.md`, "Policy lane"). Re-confirmed
rather than assumed from that precedent: `.githooks/commit-msg` is not
tracked on `origin/main` or anywhere in `git log --all`, is listed in
`.git/info/exclude`, and its own header identifies it as a gc-rig session
shim ("installed by worktree-setup.sh ... rewritten on every session
start"). `scripts/check-versions.sh` globs `.githooks/*` on the raw
filesystem rather than via `git ls-tree`, so it picks up this local
untracked shim when run inside a gc-managed worktree — unrelated to this
diff and unrelated to `origin/main`'s actual tracked content. Real
GitHub Actions CI checks out a clean tree without this shim, so the
upstream required check is unaffected.

Remediated reversibly, same method as the be-3b4e precedent: moved the
shim to scratch, re-ran the full policy lane (clean, rc=0), restored the
shim immediately after, confirmed restoration (executable, correct
content, `git status --porcelain=v1` empty before and after). Full `make
ci-pr-policy` (check-build-tags, check-go-install-guidance,
check-versions, build-docs-binary, doc-flags, doc-freshness,
check-testing-short, workapi frontend boundary, no-beads-jsonl-changes,
`make api-check`): clean, exit 0.

## Result: PASS

All 7 criteria plus the 3b policy/lint lane pass, independently
re-verified rather than trusted from be-1kxq's report. The one policy-lane
hiccup encountered (`.githooks/commit-msg`) was root-caused (a local
gc-rig worktree artifact unrelated to this diff or to `origin/main`, and
already characterized identically by the be-3b4e precedent) and remediated
reversibly rather than waived or worked around silently.

Merge authority: gastownhall/beads is a contributor-only repo (we do not
maintain it — `origin` remote push is disabled,
`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`). Deployer's job ends
at the opened PR; no `release-gate/deploy-clearance` status posted, no
merge-request routed to mayor. Upstream maintainers own the merge.
