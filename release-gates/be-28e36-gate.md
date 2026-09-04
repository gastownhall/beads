# Release Gate: be-28e36

**Feature:** Fix: cmd/bd exceeds go test's default 10m package budget in PR Core; pr-core.sh sets no -timeout
**Deploy bead:** be-28e36
**Review bead:** be-d2zml (verdict: pass, closed 2026-09-04)
**Build bead:** be-p5su8
**Deploy commit:** `6784ed6aca206351adb0a241141fb825a219a515`
**Source branch:** `builder/be-p5su8` (provenance only — never a push target)
**Deploy mode:** remote (origin=github.com/gastownhall/beads, fork=github.com/quad341/beads)
**Base ref:** `origin/main` @ `c0d8da42de5fd15c95adac85e342ba4a121da0fb` ("fix(storage/uow): retry transient ping failures during openDB bootstrap (#6003)")

## Pre-flight: already merged?

`gh api repos/gastownhall/beads/commits/6784ed6aca206351adb0a241141fb825a219a515/pulls` → `[]`.
No PR exists for this commit yet. Normal (non-reconciliation) flow applies.

Note for reviewer context (not gate-blocking): PR #6248 ("ci(pr-core): give
both pr-core-wrapper jobs a finite 45m budget above the 25m per-package
deadline", branch `bee/wy-32olgz-pr-core-timeout`, still OPEN) addresses the
same symptom area (pr-core CI timeout budget) via a different mechanism —
a job-level wrapper timeout — rather than this diff's per-package `go test
-timeout` flag. The two are complementary, not duplicative or conflicting:
neither diff touches the same file. Disclosed here for transparency; does
not affect this gate's independent evaluation of be-28e36's own diff.

## Criterion 6 — Branch diverges cleanly from BASE_REF (evaluated first)

**PASS.** `git merge-base --is-ancestor origin/main 6784ed6aca2` → rc=0.
origin/main (c0d8da42d) is a direct ancestor of the deploy commit:

```
c0d8da42d (origin/main tip)
 → ea3da6a6a  test(ci): red — cmd/bd exceeds go test's default 10m package
               budget in PR Core; pr-core.sh sets no -timeout (refs be-p5su8)
 → 6784ed6ac  fix(ci): green — cmd/bd exceeds go test's default 10m package
               budget in PR Core; pr-core.sh sets no -timeout (refs be-p5su8)
```

Strict fast-forward descendant, zero divergence. No self-rebase needed.

## Criterion 1 — Reviewer PASS verdict present

**PASS.** be-d2zml, `verdict: pass`, `close_reason: pass`, `deploy_bead:
be-28e36`, `deploy_commit: 6784ed6aca206351adb0a241141fb825a219a515` — exact
match to this deploy's commit.

## Criterion 2 — Acceptance criteria met

**PASS.** be-p5su8's root cause and fix spec: `scripts/ci/pr-core.sh` ran
`go test ... ./...` with no `-timeout`, inheriting Go's default 10m
per-package budget; `cmd/bd` (721 files, ~2697 tests) runs 602-604s under
`-race`, intermittently tripping that budget on unrelated PRs (proven via a
docs-only PR failing identically — not a PR-specific regression). Fix adds
an explicit `-timeout 20m` with a headroom comment.

be-d2zml independently reverified all 4 done-when items (not accepted on the
builder's word alone):

1. Explicit `-timeout` above the 10m default present in `pr-core.sh` ✓
2. Comment explains cmd/bd's near-budget runtime ✓
3. Shellcheck clean on new/changed lines (3 pre-existing SC1091 infos found
   elsewhere, confirmed byte-identical pre- and post-change, out of scope) ✓
4. Commit message records the 602-604s baseline + budget headroom claim,
   independently corroborated against `.github/workflows/pr.yml`'s
   `pr-core-wrapper` job (confirmed no competing `timeout-minutes` override)
   and this gate's own `cmd/bd` timing (below) ✓

Deployer's own spot-check of the diff confirms the described change exactly:

```diff
+# cmd/bd (721 files, ~2700 tests) runs 602-604s under -race — within
+# seconds of go test's 10m default per-package budget, which intermittently
+# trips this job on unrelated PRs (a docs-only PR failed identically).
+# Make the budget explicit with real headroom.
 ci_time "pr-core go test" -- \
-    go test -p "$GO_TEST_PKG_PARALLEL" -parallel "$GO_TEST_PARALLEL" -race -short -skip '^TestEmbedded' ./...
+    go test -p "$GO_TEST_PKG_PARALLEL" -parallel "$GO_TEST_PARALLEL" \
+        -race -short -timeout 20m -skip '^TestEmbedded' ./...
```

## Criterion 3 — Tests pass (required CI-equivalent command)

**PASS.** Ran `make ci-pr-core` at DEPLOY_SHA in the foreground (the actual
required PR test lane: `go test -p 4 -parallel 4 -race -short -timeout 20m
-skip '^TestEmbedded' ./...`). Exit 0, every package `ok`, runtime 465s.

This independently corroborates — not merely trusts — be-d2zml's own re-run
(507s wall, `cmd/bd` 295.727s, 97 packages ok / 23 "no test files", 0 FAIL,
0 SKIP): this gate's own run measured `cmd/bd` at 289.262s and
`internal/storage/embeddeddolt` at 246.132s, both consistent in magnitude
and both well inside the new 20m budget.

Diff-owned test file: `scripts/ci_pr_core_test.go` (only new `_test.go` file
in the diff). Independently re-confirmed by direct package result, not just
the aggregate run:

| Test | Package | Result |
|---|---|---|
| `TestPRCoreGoTestHasExplicitTimeoutAboveDefaultBudget` | `github.com/steveyegge/beads/scripts` | PASS — package `scripts` ok in 7.315s |

test_counts: full-suite 0 FAIL, 0 SKIP, diff-owned test 1/1 PASS. No
diff-owned SKIP, so no waiver needed. waiver_ref: none.

## Criterion 3a — Pre-existing-failure attribution

**N/A — not needed.** This gate's own full-scope run (above) shows 0 FAIL,
matching the reviewer's independent 0 FAIL. No failing test exists to
attribute, so the non-diff-owned-gate-failure four-clause protocol does not
apply this round.

## Criterion 3b — Policy/lint lane (part of criterion 3, not optional)

**PASS**, after excluding a confirmed pre-existing local-environment false
positive.

`make ci-pr-policy` fails solely at "check version consistency":
`.githooks/commit-msg: no 'BEGIN/END BEADS INTEGRATION' marker found`. This
is the already-tracked, pre-existing gate-tracker **be-a0dxu** (filed
2026-08-30, predates this run), not something this diff introduces or
touches:

- `git ls-files .githooks/commit-msg` → empty (untracked).
- `git check-ignore -v .githooks/commit-msg` → matched by
  `/home/jaword/projects/beads/.git/info/exclude:40` — a **local-only**,
  machine-specific exclude rule, never present in a clean checkout or in CI.
- File content: a gc-management "commit-gate shim... installed by
  worktree-setup.sh (be-xug guardrail A)... rewritten on every session
  start" — session-orchestrator machinery, not beads-repo content.
- `git log -- .githooks/commit-msg` → empty, zero commit history in this repo.
- All 4 attribution clauses satisfied: not diff-owned (diff never touches
  `.githooks/**`), tracked bead predating the run (be-a0dxu), not caused by
  the diff (direct proof: untracked + gitignored local artifact, present
  regardless of diff content), no path overlap.

Reversibly excluded the shim to confirm the lane is otherwise fully clean
(sha256 backed up first, moved aside, restored immediately after,
byte-identical checksum match reverified both before and after the run):

```
check build-tag policy .......... PASS (100 files scanned)
check go install guidance ....... PASS
check version consistency ....... PASS
build bd for docs checks ........ PASS
check doc flags .................. PASS
check doc freshness .............. PASS
check testing.Short boundaries ... PASS
check workapi frontend boundary .. PASS
check no .beads/issues.jsonl changes . PASS
check openapi spec gate (api-check) .. PASS
```

(One unrelated informational WARN surfaced under "check doc flags" —
pre-existing legacy-SQLite-reference mentions in docs unrelated to this
diff's files; not a failure, does not affect PASSED summary.)

`make ci-pr-lint` with `BD_LINT_NEW_FROM_MERGE_BASE=origin/main` (matching
`.github/workflows/pr.yml`'s actual `pr-lint-wrapper` job): gofmt clean;
golangci-lint 0 issues (both native and windows cross-lint). The two
warning lines about unresolvable file paths under sibling `builder/`
worktrees are cross-worktree cache/path noise from other agents' sessions,
not findings against this diff.

policy_lane: `make ci-pr-policy` (PASS, shim-attributed) + `make ci-pr-lint`
(PASS), both independently re-run by deployer this round.

## Criterion 3c — CI-config diff gets its own lane's first real run

**PASS.** This diff modifies `scripts/ci/pr-core.sh` itself — the pr-core
CI lane's own script, not merely a test that asserts facts about it. This
gate's criterion-3 run (`make ci-pr-core`, above) is the lane's first real
post-change execution: it invokes the actual modified script end-to-end
(not just `TestPRCoreGoTestHasExplicitTimeoutAboveDefaultBudget` in
isolation), confirming the new `-timeout 20m` flag is live in the real
invocation path and that the lane still completes successfully with it
(465s, exit 0, 0 FAIL) — not merely that a static-text meta-test about the
script passes.

## Criterion 4 — No high-severity review findings open

**PASS.** be-d2zml: `style_findings: none` (gofmt/go vet clean; shellcheck
findings pre-existing and outside the diff's hunk). `security_findings:
none` — diff is a CI script flag addition (static `-timeout 20m`, no
untrusted-input interpolation) plus a meta-test that reads a fixed in-repo
relative path; no injection vector, no auth/secrets/PII surface, no new
dependencies. `bd` search across be-d2zml/be-p5su8/be-28e36 notes surfaces
no separate finding beads.

## Criterion 5 — Feature branch clean (no uncommitted changes)

**PASS.** `git status --short` at DEPLOY_SHA: zero output, fully clean.
Reverified after the criterion-3b shim move/restore dance (byte-identical
checksum both times) — the working tree returned to exactly this state.

## Criterion 7 — Single feature theme

**PASS.** `git diff --stat origin/main...6784ed6ac`: 2 files, 78
insertions(+), 1 deletion(-), no `.claude/**` paths:

- `scripts/ci/pr-core.sh` (+7/-1) — the actual fix: explicit `-timeout 20m`
  plus headroom comment.
- `scripts/ci_pr_core_test.go` (new file, +72) — meta-test asserting the
  flag stays present.

One coherent theme: give the pr-core lane's `go test` invocation an
explicit, adequate timeout, and a test that keeps it that way. No unrelated
changes riding along.

## Overall verdict: PASS

All 7 criteria (plus 3a/3b/3c) clear. Proceeding to cut the isolated
`deploy/be-28e36-gate` branch from `6784ed6aca206351adb0a241141fb825a219a515`
(already the current HEAD of this branch) and open the PR against
`gastownhall/beads`. Per this repo's contributor-only status and established
precedent (be-vc1m, be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3,
be-km2kg), the job ends at the opened PR — no merge-request will be routed
to mayor and no deploy-clearance status will be posted; merge authority
belongs to the upstream maintainers.
