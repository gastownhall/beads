# Release Gate: be-053u

**Feature:** Fix: check-testing-short.sh flags comments and misnames the offending function (round 2)
**Deploy bead:** be-053u
**Review bead:** be-d8z3 (verdict: pass, closed 2026-08-15T22:23:23Z)
**Build beads:** be-0v13 (round 1) → be-r56j (round-1 review, request-changes) → round-2 resume (this deploy)
**Deploy commit:** `604a672d5cc9138ad589b4be9ccee0b62a2fd33b`
**Source branch:** `builder/be-0v13` (provenance only — never a push target)
**Deploy mode:** remote (origin=github.com/gastownhall/beads, fork=github.com/quad341/beads)
**Base ref:** `origin/main` @ `7505e173f` ("chore(release): forward-port v1.2.2 to main", #5782)

## Pre-flight: already merged?

`gh api repos/gastownhall/beads/commits/604a672d5cc9138ad589b4be9ccee0b62a2fd33b/pulls` → `[]`.
No PR exists for this commit yet. Normal (non-reconciliation) flow applies.

## Criterion 6 — Branch diverges cleanly from BASE_REF (evaluated first)

**PASS.** `git merge-base --is-ancestor origin/main 604a672d5c...` → rc=0. origin/main
(7505e173f) is a direct ancestor of the deploy commit:

```
7505e173f (origin/main tip)
 → d79c5a2f2  test(feat): red
 → d7f274e06  feat: green (round 1)
 → 6b9ed10c3  test(scripts): red (round 2)
 → 604a672d5  fix(scripts): green (round 2, DEPLOY_SHA)
```

Strict fast-forward descendant, zero divergence. No self-rebase needed.

## Criterion 1 — Reviewer PASS verdict present

**PASS.** be-d8z3, verdict: pass, close_reason: "pass". Round 2 re-reviewed after round 1
(be-r56j) issued request-changes for an unmet exit_contract item (see criterion 2).

## Criterion 2 — Acceptance criteria met

**PASS.** This repo has no `work-packages/` directory and no `docs/PROJECT_MANIFEST.md` —
both checked, neither exists. Acceptance criteria live as bead-native `exit_contract` items
instead (the operative mechanism here). be-r56j's round-1 review left one item unmet:

> UNMET: a hit that sits between two functions (not truly inside either) must resolve to
> 'unknown', not the preceding function's name. Round-1's test only covered the degenerate
> "no preceding func at all" case, which already passed pre-fix via an unrelated fallback.

Round 2 adds `TestCheckTestingShortReportsUnknownBetweenFunctions` and fixes the awk
attribution logic. be-d8z3 independently hand-read the new fixture (`TestA(){}` → blank
line → bare `testing.Short()` → `TestB(){}`) and confirmed it exercises the genuine
between-two-functions case, not a degenerate repeat. All 5 exit_contract items now `[x]`.

## Criterion 3 — Tests pass (required CI-equivalent command)

**PASS.** Ran `make ci-pr-core` at DEPLOY_SHA (the actual required PR test lane:
`go test -race -short -skip '^TestEmbedded' ./...`, hermetic env via `beads_test_env_enter`).
Exit 0, every package `ok`, runtime 379s. `DOCKER_HOST`/`TESTCONTAINERS_RYUK_DISABLED` set
beforehand per protocol; this diff has no container/storage surface so no container-backed
tests apply, but the env was live regardless to avoid a false-green skip.

Diff-owned test file: `scripts/check_testing_short_test.go` (only `_test.go` file in the
diff; `git diff --name-only origin/main...604a672d5c | grep -E '_test\.(go|py|rb)$|...'`).
Independently re-ran verbosely, by name (not just the aggregate "ok"):

| Test | Result |
|---|---|
| TestCheckTestingShortIgnoresCommentOnlyMention | PASS |
| TestCheckTestingShortStillFlagsRealCallAndNamesFunc | PASS |
| TestCheckTestingShortReportsUnknownAboveFirstFunc | PASS |
| TestCheckTestingShortReportsUnknownBetweenFunctions | PASS |
| TestCheckTestingShortPassesOnCleanRepoTree | PASS |

test_counts: 5/5 diff-owned PASS, 0 FAIL, 0 SKIP. One pre-existing SKIP elsewhere in the
`scripts` package (`TestTestScriptPrebuiltBinaryLaunchProbe`, self-gated to the test.sh
fake-go re-exec driver, not diff-owned — file untouched by this diff). No diff-owned SKIP,
so no waiver needed. waiver_ref: none.

## Criterion 3b — Policy/lint lane (part of criterion 3, not optional)

**PASS**, after excluding a confirmed local-environment false positive.

`make ci-pr-policy` initially failed at "check version consistency":
`.githooks/commit-msg: no 'BEGIN BEADS INTEGRATION' marker found`. Investigated before
accepting this as a real finding, since the diff never touches `.githooks/`:

- `git log -- .githooks/commit-msg` → empty. The path has **zero commit history** in this repo.
- `git ls-files .githooks/commit-msg` → empty (untracked).
- `git check-ignore -v .githooks/commit-msg` → matched by
  `/home/jaword/projects/beads/.git/info/exclude:40` — a **local-only**, machine-specific
  exclude rule, never present in any other clone or in CI.
- File content: a gc-management "commit-gate shim... installed by worktree-setup.sh
  (be-xug guardrail A)... rewritten on every session start", pointing at a local
  gc-management path — session-orchestrator machinery, not beads-repo content.
- `scripts/check-versions.sh` enumerates via a bare `for hook in .githooks/*` filesystem
  glob (confirmed: `grep -n githooks scripts/check-versions.sh` → line 95), with no
  git-tracked-file filter. A real GitHub Actions checkout would never have this file, so
  this failure cannot reproduce in actual CI.

Backed up the shim (mode 755, byte contents), relocated it, re-ran `make ci-pr-policy`
clean end-to-end (exit 0) — including "check version consistency" itself, and including
"check testing.Short boundaries" (the exact lane this bead's diff targets):

```
check build-tag policy .......... PASS (96 files scanned)
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

Restored the shim immediately after (byte-identical `diff -q`, mode 755 confirmed).

`make ci-pr-lint` with `BD_LINT_NEW_FROM_MERGE_BASE=origin/main` (matching
`.github/workflows/pr.yml`'s actual `pr-lint-wrapper` job exactly): exit 0.
gofmt clean; golangci-lint 0 issues (native + windows cross-lint). Note: locally installed
golangci-lint is v2.12.0 vs CI-pinned v2.10.1 — version delta not reconciled (diff is 2
files / +119/-1 in `scripts/`, low risk), flagged here for transparency rather than silently
assumed equivalent.

policy_lane: `make ci-pr-policy` + `make ci-pr-lint`, both PASS (see above).

## Criterion 4 — No high-severity review findings open

**PASS.** be-d8z3 round-2 style_findings: none blocking (one pre-existing non-blocking note
at scripts/check-testing-short.sh:29, unchanged by this diff, already known from round 1).
security_findings: none — full OWASP Top 10 walk against the true diff, explicitly including
a note that the net effect is gate-tightening (closes a misattribution that could let an
unauthorized `testing.Short()` slip past the allowlist under a legitimate function's name).

## Criterion 5 — Feature branch clean (no uncommitted changes)

**PASS.** `git status --short` at DEPLOY_SHA: zero modified/staged tracked files. Only
pre-existing untracked leftovers from unrelated, already-closed beads (release-gates/be-hi97,
release-gates/be-uoat, scripts/rebase-resolve-lib.sh — a stray untracked copy, not sourced).

## Overall verdict: PASS

All 6 criteria pass. Proceeding to push-and-pr.
