# Release gate — Flaky `TestSemaphoreShedsLoadInsteadOfQueueingForever` (be-qczn)

- **Builder bead (CLOSED):** be-qczn — remove the wall-clock race in the
  final "wait that eventually succeeds" section of
  `TestSemaphoreShedsLoadInsteadOfQueueingForever`, which flaked on
  macOS CI runners (20ms timer vs 5ms sleep, ~15ms margin).
- **Deploy bead:** be-f80c
- **Review bead:** be-u8o4 (CLOSED), verdict **PASS**, recorded on commit
  `a80cedfde2931e6d13739df782ed8fb0a0d36054`
- **Commits:** `70a0f10c493aa0ce2042eb30a2137346683dde2e` (tdd_red) then
  `a80cedfde2931e6d13739df782ed8fb0a0d36054` (tdd_green), 1 file over
  `origin/main`
- **Branch:** `builder/be-qczn` (provenance only; present on `fork`,
  `headfork`/`prhead` — `quad341/beads` GitHub-redirects to its renamed
  location `quad341/beads-sec003-contrib`, which `headfork`/`prhead` name
  directly). Isolated deploy branch `deploy/be-u8o4-gate` cut fresh from
  the commit below and pushed to `headfork` to avoid the rename-redirect
  ambiguity.
- **Evaluated:** 2026-08-15 by beads/deployer

## Scope

Diff scope, confirmed via `git diff origin/main..HEAD --stat` (1 file):

- `internal/httpapi/server_test.go` — 9 insertions, 2 deletions, entirely
  inside `TestSemaphoreShedsLoadInsteadOfQueueingForever`'s final section.

The fix sets `s.semTimeout = 2 * time.Second` immediately before the
final "wait that eventually succeeds" block only — the earlier
tight-timeout shed-check assertions in the same test are unaffected —
and widens the releasing goroutine's sleep from 5ms to 25ms. This matches
option (a) of be-qczn's own suggested fix shapes verbatim ("make the
acquire bound generous, e.g. seconds, for this final section only") and
explicitly avoids the shape the bead flagged as forbidden ("do NOT simply
bump 20ms to a larger constant; that rescales the flake instead of
removing it") — the tight 20ms timeout governing the earlier fast-shed
assertions is untouched; only the final section's own bound is
independently widened to a margin (2s vs 25ms, ~80x) generous enough that
realistic CI scheduling jitter cannot plausibly trip it.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-u8o4 records verdict PASS on commit `a80cedfde2931e6d`, zero style/security findings, full spec evidence. |
| 2 | Acceptance criteria met | **PASS** | Independently checked against be-qczn's 3-item Done-when checklist: (1) final section no longer depends on a *tight* sleep-vs-timeout race — now bound by a generous, section-scoped 2s window per the bead's own preferred fix shape; (2) `event=semaphore_saturated` assertion for the long-wait path is intact (`server_test.go:778`); (3) `GOMAXPROCS=1 go test -count=200 -run '^TestSemaphoreShedsLoadInsteadOfQueueingForever$' ./internal/httpapi` — independently re-run, clean PASS, 9.145s. |
| 3 | Tests pass | **PASS** | `./scripts/test.sh ./internal/httpapi/...` — package `ok`, 488 PASS / 0 FAIL / 0 SKIP (verbose count). Diff-owned test independently soaked 50x under `-race`: clean, 3.455s. |
| 3b | Policy/lint lane | **PASS** | `make ci-pr-policy` — build-tag policy clean (96 files), go-install guidance clean, all 7 git-tracked hook markers match v1.2.2. One non-diff-owned, non-blocking environmental false-positive noted below. |
| 4 | No unresolved HIGH findings | **PASS** | Zero findings (style or security) per be-u8o4; independently confirmed diff is a 1-file, 2-statement change with no new surface. |
| 5 | Clean working tree | **PASS** | `git status --porcelain` on the evaluated commit shows no staged/unstaged changes (only the pre-existing, unrelated untracked `scripts/rebase-resolve-lib.sh` and two other stray `release-gates/*.md` scratch files already present in the worktree, none staged, none part of this diff). |
| 6 | Clean divergence from `origin/main` | **PASS** | `git merge-base --is-ancestor origin/main a80cedfde2931e` succeeds — `origin/main` is a strict ancestor of the deploy commit; HEAD is exactly 2 commits ahead. No self-rebase needed. |
| 7 | Single feature theme | **PASS** | Both commits (tdd_red, tdd_green) touch only `internal/httpapi/server_test.go`, both serving the one flaky-test fix. No unrelated changes riding along. |

## Tests run on release branch (independent re-verification)

| Check | Result |
|---|---|
| `./scripts/test.sh ./internal/httpapi/...` (documented CI-equivalent command) | `ok`, 4.5s, 488 PASS / 0 FAIL / 0 SKIP |
| `go test -v -run 'TestSemaphoreShedsLoadInsteadOfQueueingForever' ./internal/httpapi/...` | 1 PASS, 0.05s |
| `go test -race -count=50 -run 'TestSemaphoreShedsLoadInsteadOfQueueingForever' ./internal/httpapi/...` | clean, 3.455s |
| `GOMAXPROCS=1 go test -count=200 -run '^TestSemaphoreShedsLoadInsteadOfQueueingForever$' ./internal/httpapi` (be-qczn Done-when item 3, literal command) | clean, 9.145s |
| `make ci-pr-policy` | 1 non-diff-owned false positive, see below; otherwise clean |

Package `internal/httpapi` has no testcontainers/Dolt/podman dependency
(confirmed via `grep -rl testcontainers,DOCKER_HOST internal/httpapi/` —
no matches), so no container environment setup was needed for this gate.

### `make ci-pr-policy` non-blocking finding (not diff-owned, not repo-owned)

`check-versions.sh`'s hook-marker check globs every file physically
present in `.githooks/*` on disk (`for hook in .githooks/*`) rather than
git-tracked files only, despite its own header comment scoping it to
"Tracked managed git-hook sections." This deployer worktree has a local,
gitignored `gc-commit-gate-shim` at `.githooks/commit-msg` (installed by
`worktree-setup.sh`, self-documented as "rewritten on every session
start," pointing at a hook script under the `gc-management` pack tree),
which the glob picks up and flags for missing `BEGIN/END BEADS
INTEGRATION` markers it was never meant to carry. Confirmed non-diff-owned
and non-repo-owned: `.githooks/commit-msg` does not exist in `origin/main`
(`git show origin/main:.githooks/commit-msg` → path not found), and this
diff does not touch `.githooks/` at all (`git diff origin/main..HEAD
--stat -- .githooks/` → empty). All 7 actually-tracked hook files' version
markers matched `1.2.2` correctly. Worth a followup bead against
`check-versions.sh` (scope the glob to `git ls-files .githooks/`) but out
of scope for this deploy and does not gate it.

## Findings from review (no action required)

From be-u8o4: zero style or security findings. Diff is a 7-line comment
plus a 1-line test-local field-set inside one existing test function; no
new identifiers, no exported surface, no new I/O or auth/authz paths.
`semTimeout` is a test-only override field — production construction
always sets it unconditionally, confirmed by review against
`internal/httpapi/server.go`.

## Verdict

**PASS** — all 7 criteria clear, including an independent re-verification
of every criterion 2/3 claim rather than trusting the reviewer's evidence
alone. Proceeding to cut `deploy/be-u8o4-gate` from
`a80cedfde2931e6d13739df782ed8fb0a0d36054` and open the PR.

This repo (`gastownhall/beads`) is a contributor-only upstream for this
rig — merge authority belongs to upstream maintainers, not our
mayor/mpr. Per the repo-scoped carve-out, this deploy's job ends at the
open PR: no `gh pr merge`, no `release-gate/deploy-clearance` commit
status, no formal merge-request routing. Gate result is reported to
mayor as an FYI only.
