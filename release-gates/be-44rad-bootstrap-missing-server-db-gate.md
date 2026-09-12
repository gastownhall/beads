# Release gate — Fix: bd bootstrap can also silently create an empty server-mode database (be-cy41)

- **Builder bead (CLOSED):** be-cy41 — same root cause as be-5up5, but in the
  `bd bootstrap` path rather than `bd init`.
- **Deploy bead:** be-44rad
- **Review bead:** be-i0ivt — verdict **PASS**, commit `beabf716f`
- **Commit:** `beabf716f431af85788ca8f9393a9507caeab004` — 2 commits over
  `origin/main`'s review-time base (`7857719f3` red, `beabf716f` green), 2
  files, +247/-37
- **Branch:** `builder/be-cy41` (provenance only, per bead prose — not a push
  target); deploy branch `deploy/be-44rad-gate` cut from `beabf716f` and
  pushed to `headfork` (`quad341/beads-sec003-contrib`, confirmed current
  GitHub identity of the fork — see "Push target" below)
- **Evaluated:** 2026-09-12 by beads/deployer

## Scope

`bd bootstrap` (server-mode) could silently create an empty server-mode
database when `cfg.ProjectID` was set but no local shadow directory existed —
the existing-project-detection logic short-circuited on the absent shadow dir
without live-probing the actual configured server. Same root cause class as
be-5up5 (which fixed the analogous `bd init` path), but a distinct code path
(`cmd/bd/bootstrap.go`) with no file overlap — confirmed this is not a
duplicate of PR #5791 (be-5up5's fix, touching `cmd/bd/init.go`/
`init_guard.go` only) via `gh pr view 5791 --json files`.

Diff, confirmed via `git diff --stat 91aeeb0..beabf716f`:

- `cmd/bd/bootstrap.go` (+153/-?) — restructures `existingBootstrapDBPlan` to
  live-probe when `cfg.ProjectID != ""` even with no local shadow dir; adds
  `probeBootstrapServerDB` helper; `executeInitAction` now calls a new guard
  `refuseServerModeInitForExistingProject` first; adds
  `bootstrapMissingServerDBRefusal`, a non-destructive refusal message
  pointing at `bd doctor`/`bd dolt status`/`bd backup restore`.
- `cmd/bd/bootstrap_test.go` (+131) — two new regression tests, one per bug.

Both files independently read in full and confirmed to match be-cy41's
5-bullet exit_contract; no unrelated refactors.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| Pre-flight | Not already merged | **PASS** | `gh api repos/gastownhall/beads/commits/beabf716f/pulls` empty; keyword/head-branch searches surfaced only PR #5791 (be-5up5, sibling fix, different files — not a duplicate). |
| 1 | Review PASS present | **PASS** | be-i0ivt records `verdict: pass` on commit `beabf716f`, full style/security/spec write-up, zero blocker/major findings. |
| 2 | Acceptance criteria met | **PASS** | be-cy41's 5-bullet exit_contract independently re-checked directly against the diff (not just the reviewer's assertion): both new tests exercise the two named bugs via mock injection; TDD red→green confirmed (`7857719f3` → `beabf716f`); pre-existing bootstrap/init suites unaffected; diff scope is exactly the 2 files above. |
| 3 | Tests pass | **PASS** | Full documented suite independently re-run at `beabf716f`: 97/97 packages `ok`, 0 FAIL, 0 SKIP, 0 panics, exit 0. Both diff-owned tests independently re-run and confirmed PASS by exact name. See "Tests run" below. |
| 3a | Pre-existing-failure attribution | **N/A** | No diff-owned SKIP or FAIL occurred on this independent run — nothing to attribute. (The build bead's own preflight had reported failures, but those were traced by the reviewer to a guessed, undocumented test invocation — see "Tests run" below — not reproduced here under the documented command.) |
| 3b | Policy/lint lane | **PASS** | `make ci-pr-policy` gets a genuinely clean pass after moving aside the known untracked `.githooks/commit-msg` session shim (see below); restored immediately after. |
| 3c | CI-config lane | **N/A** | Diff touches no CI configuration (`git diff --stat` confirms only `cmd/bd/bootstrap.go` + `cmd/bd/bootstrap_test.go`). |
| 4 | No unresolved HIGH findings | **PASS** | Zero blocker/major findings. One pre-existing, non-diff-introduced informational note (error surfacing via `%v` from an unchanged helper — traced to standard driver errors, no credential leakage). See "Findings" below. |
| 5 | Clean working tree | **PASS** | `git status --short` at `beabf716f` shows only the pre-existing, unrelated untracked `release-gates/be-fkmhv-gate.md` scratch file already present in this shared worktree; nothing staged/unstaged from this diff. |
| 6 | Clean divergence from `origin/main` | **PASS** | `git merge-tree $(git merge-base origin/main beabf716f) origin/main beabf716f` — no conflict markers; `origin/main`'s new commits since review-time base (`91aeeb0` → current tip `e21eaf00a`) touch neither `cmd/bd/bootstrap.go` nor `cmd/bd/bootstrap_test.go`. No self-rebase needed. |
| 7 | Single feature theme | **PASS** | 2-file diff, one bug-fix theme (server-mode bootstrap DB-existence probing), both files serve that theme directly. |

## Push target

Generic origin/fork recipe would pick `fork` (`quad341/beads`, listed first,
succeeds dry-run). Verified against live evidence instead of the naming
convention: `gh api repos/quad341/beads` and `gh api
repos/quad341/beads-sec003-contrib` both resolve to `full_name:
"quad341/beads-sec003-contrib"` (`fork` is a stale pre-rename URL that
GitHub redirects) — `parent: "gastownhall/beads"` confirmed on both. Two
real recent org PRs (#5791, #5774) both show `headRepository.full_name:
"quad341/beads-sec003-contrib"`. **PUSH_REMOTE=headfork**, consistent with
prior precedent (be-y1jo and others) and this repo's established pattern.

## Tests run on release branch (independent re-verification)

Full documented suite (`TEST_COVER=1 ./scripts/test.sh`, hermetic
`beads_test_env_enter`, `DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock`,
`TESTCONTAINERS_RYUK_DISABLED=true`, `TMPDIR`/`GOTMPDIR` unset), run at
`beabf716f`, independently by the deployer — not trusted from the reviewer's
report alone, though it converges exactly with it:

- **97/97 packages `ok`, 0 FAIL, 0 SKIP, 0 panics, exit 0**
  (`scratchpad/be-44rad-full-suite-test.log`). `cmd/bd` itself: 286.411s (real
  execution, not a cache hit — consistent with the reviewer's own 210–438s
  range for that package).

Diff-owned tests, re-run scoped and verbose for explicit per-name
confirmation (`scratchpad/be-44rad-diffowned-tests.log`):

| Test | Result |
|---|---|
| `TestDetectBootstrapAction_NoLocalShadowDirStillLiveChecksExisting` | PASS (0.00s) |
| `TestExecuteInitAction_ServerModeExistingProjectMissingDBRefuses` | PASS (0.00s) |

2/2 diff-owned tests green. This matches the reviewer's own independent
re-run at the same head commit exactly (their `diff_tests_executed` field
records identical PASS/name/duration values), obtained via a wholly separate
invocation — convergent evidence, not the same evidence read twice.

The build bead's own preflight (be-cy41) had reported 11 packages / 38
sub-tests failing, but the reviewer traced this to a guessed, undocumented
`go test ./...` invocation missing the 25-minute timeout floor,
`TESTCONTAINERS_RYUK_DISABLED`, and hermetic env setup that `scripts/test.sh`
provides — not a real pre-existing condition. This deployer's own from-
scratch full-suite run at the same commit independently confirms all 97
packages clean, corroborating that attribution rather than taking it on
trust.

### Policy/lint lane (criterion 3b)

`make ci-pr-policy` initially fails one sub-check: `.githooks/commit-msg`
missing `BEGIN`/`END BEADS INTEGRATION` markers, tripping version-consistency.
Independently verified, not taken on trust:

```
git ls-files --error-unmatch .githooks/commit-msg   # NOT TRACKED
git check-ignore -v .githooks/commit-msg             # matched by local .git/info/exclude:40
git diff --stat 91aeeb0..beabf716f -- .githooks/     # empty — diff never touches .githooks/
```

`.githooks/commit-msg` is an untracked, git-ignored per-session shim (not a
repository file), installed by `worktree-setup.sh` and regenerated every
session start — root-caused previously in be-jygq (closed, declining a
standing-tracker bead in favor of the reversible workaround below; that
closure is itself the "tracker that exists" for this condition, per
criterion 3a/3b's attribution clause). Rather than only attributing-and-
proceeding on the red result, got a genuinely clean pass — stronger evidence,
per precedent:

```
mv .githooks/commit-msg <scratch>/commit-msg.shim.aside
make ci-pr-policy   # exit 0, every sub-check PASS including version consistency
mv <scratch>/commit-msg.shim.aside .githooks/commit-msg   # restored
git status --porcelain .githooks/   # empty after restore
```

Full clean run also covers doc-flags, doc-freshness, `testing.Short()`
boundaries, workapi frontend boundary, `.beads/issues.jsonl` no-changes, and
the OpenAPI spec gate — all PASS.

## Findings from review (no action required)

Zero blocker/major findings. `gofmt`, `golangci-lint` (scoped to this diff via
`BD_LINT_NEW_FROM_MERGE_BASE`), `go vet`, and both pure-Go/cgo build-tag
targets all clean. One minor/informational, non-diff-introduced note: an
unchanged helper (`checkBootstrapServerDB`) surfaces its error via `%v` in
both the pre-existing and new refusal messages; traced to standard
`go-sql-driver/mysql` errors that don't echo DSN/password, so no
credential-leakage vector — and out of scope for this diff regardless since
the helper itself is untouched.

## Verdict

**PASS** — all applicable criteria pass (3a and 3c are N/A; 3b passes on a
genuinely clean re-run after setting aside a known, independently-verified,
non-diff-related environmental shim). Cutting isolated deploy branch
`deploy/be-44rad-gate` from `beabf716f431af85788ca8f9393a9507caeab004`,
pushing to `headfork`, and opening a PR against `gastownhall/beads:main`.

**gastownhall/beads merge-authority carve-out:** this is a contributor-only
repository (`origin` push disabled; upstream is fetch-only). Per deployer
protocol, the job ends at the open PR — no merge-request routed to
mayor/mpr, no deploy-clearance status posted. Merge belongs to upstream
maintainers.
