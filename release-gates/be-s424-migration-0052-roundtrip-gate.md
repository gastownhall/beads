# Release gate — Restore migration 0052 (formerly 0033) round-trip test (be-s424)

- **Builder bead (CLOSED):** be-auu.5 — restore migration_0033_test.go
  (renumbered 0052) round-trip test (up/down/up, ~2K rows), recovering test
  coverage lost to a silent rebase-corruption casualty on the be-auu epic.
- **Deploy bead:** be-s424
- **Review bead:** be-d9xm — verdict **PASS**, recorded on commit
  `961aa8cddb4034fb7742f7ec74e4b6297a84f5c8`
- **Commits:** `bd8e1e6352457186420fc0cb931f6e00815e0f3a` (RED, test-only) then
  `961aa8cddb4034fb7742f7ec74e4b6297a84f5c8` (GREEN, empty verification
  commit — no production code change), 1 file cumulative over `origin/main`
- **Branch:** `builder/be-auu.5` (pushed to `fork`, i.e. `quad341/beads`);
  deploy branch `deploy/be-s424-gate` cut from the reviewed SHA
- **Evaluated:** 2026-08-15 by beads/deployer

## Scope

Restores test coverage for migration 0052 (renumbered from 0033 across
several rebases: 0033→0043→0048→0052), a composite-index migration
(`idx_issues_status_updated_at` + `idx_issues_defer_until`) that already
shipped correctly in production via PR #3662 (commit `0268ba894`). The
original up/down/up round-trip test over ~2K rows was a silent casualty of
an earlier clean-auto-merge rebase (documented in the be-auu epic's
extraction findings), discovered and flagged as follow-up by the be-1ubq
reviewer during an unrelated schema-perf extraction. This bead recovers that
coverage: adds `internal/storage/dolt/migration_0052_test.go` (352 lines)
with `TestMigration0052_RoundTrip` (up→down→up over 2K rows) and
`TestMigration0052_ExplainCapture` (EXPLAIN-verified query-plan assertions
for two representative queries, confirming the D4v2 indexes are load-bearing,
not just present). No production code changes — the underlying migration is
already correct and live; this is pure test-restoration.

Diff scope, confirmed via `git diff --name-only origin/main...HEAD` (1 file):

- `internal/storage/dolt/migration_0052_test.go` — new test file, +352/-0

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-d9xm records `verdict: pass` on commit `961aa8cddb4034fb7742f7ec74e4b6297a84f5c8` (matches deploy_commit exactly), with full security/style/spec write-ups. |
| 2 | Acceptance criteria met | **PASS** | be-auu.5's AC names the pre-renumbering filename `migration_0033_test.go`; the review bead documents the 0033→0052 renumbering and confirms `TestMigration0052_RoundTrip` fully covers the stated round-trip/2K-row requirement under the canonical hermetic test entrypoint. No uncovered criteria. |
| 3 | Tests pass | **PASS** | Independently re-run with real podman/Dolt containers (not trusted from the reviewer's report) — see "Tests run" below. 4/4 PASS, 0 FAIL, 0 SKIP, matching the reviewer's own reported counts exactly. |
| 4 | No unresolved HIGH findings | **PASS** | Zero HIGH (and zero MEDIUM) findings. `security_findings: none` (diff is one additive test file, all SQL parameterized or fixed-literal, no production code touched). `style_findings: none` (gofmt/golangci-lint/go vet all clean per review; independently re-confirmed below). |
| 5 | Clean working tree | **PASS** | `git status` on the evaluated commit shows no staged/unstaged changes to tracked files (only pre-existing, unrelated untracked scratch files in the worktree, never staged). |
| 6 | Clean divergence from `origin/main` | **PASS** | `git merge-base --is-ancestor origin/main 961aa8cdd` succeeds; HEAD is exactly 2 commits (RED + GREEN) ahead of a freshly-fetched `origin/main`, 0 behind. No rebase needed. |
| 7 | Single feature theme | **PASS** | Exactly one file touched (`migration_0052_test.go`), no unrelated changes riding along. |

## Tests run on release branch (independent re-verification)

Static checks, independently re-run:

| Check | Result |
|---|---|
| `go build ./...` | clean, rc=0 |
| `go vet ./...` | clean, rc=0 |
| `gofmt -l` on the diff file | clean, 0 files listed |

Diff-owned tests (`internal/storage/dolt/migration_0052_test.go`), run with
real podman/Dolt containers — `DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock
TESTCONTAINERS_RYUK_DISABLED=true BEADS_TEST_ENV_RUN_DOLT=1 GOFLAGS=-count=1
./scripts/test.sh -v -run 'TestMigration0052' ./internal/storage/dolt/...`,
matching the reviewer's own documented methodology:

| Test | Result | Duration |
|---|---|---|
| `TestMigration0052_RoundTrip` | PASS | 33.04s |
| `TestMigration0052_ExplainCapture` | PASS | 18.69s |
| `.../bd_stale_(status_IN_+_updated_at_<_cutoff)` | PASS | — |
| `.../bd_ready_deferred-parents_(defer_until...)` | PASS | — |

4/4 PASS on the first attempt, matching the reviewer's reported 4 PASS / 0
FAIL / 0 SKIP exactly — no discrepancy requiring flake-triage re-runs
(contrast with be-uoat, where a first-pass mismatch against the reviewer's
report triggered a 3x-rerun investigation; no such mismatch occurred here).

## Findings from reviews (no action required)

From be-d9xm: no HIGH or MEDIUM findings. No carried-forward informational
items. One documentation note: `ExplainCapture`'s test comments cite an
unresolvable spec id ("be-eei", not found in `bd search` or repo docs) —
informational only, not a gating claim, self-contained documentation.

## Process notes

- `assert_deploy_ancestry_scope` (referenced by the deployer protocol as a
  required pre-branch-cut check) was not found defined anywhere in
  `scripts/rebase-resolve-lib.sh` or elsewhere in the repo — confirmed via a
  repo-wide grep. Its documented checks (no `.claude/**` paths in the commit
  range; commits cite accepted bead IDs) were performed manually instead:
  both commits in range cite `be-auu.5` (closed, accepted builder bead), and
  the cumulative diff touches zero `.claude/**` paths. Recommend filing a
  follow-up bead to add this function to the shared lib so future deploys
  don't need to reconstruct the check by hand.
- `PUSH_REMOTE` resolved to `fork` (`quad341/beads`), confirmed via a live
  dry-run push (not assumed from precedent) — the same remote the builder
  branch itself used. The `headfork`/`quad341/beads-sec003-contrib`
  rename-redirect concern noted in the be-uoat gate record does not block a
  normal `git push` to `fork`; that concern may be specific to PR
  base-repo resolution rather than git push/fetch.

## Verdict

**PASS** — all 7 criteria pass. Deploy branch `deploy/be-s424-gate` cut from
`961aa8cddb4034fb7742f7ec74e4b6297a84f5c8`, pushed to `fork`
(`quad341/beads`), PR opened against `gastownhall/beads:main`. Per the
repo-authority carve-out (gastownhall/beads is a repo we contribute to, not
maintain), the deployer's responsibility ends at the open PR — merge belongs
to upstream maintainers.
