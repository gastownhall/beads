# Release gate — In-tree A/B harness for SearchCountsSQL query shapes (be-qm6fb)

- **Builder bead (CLOSED):** be-qm6fb — in-tree A/B benchmark harness that
  times named `SearchCountsSQL` shape-renderers against a seeded corpus on
  both the issues and wisp planes, asserts row-set identity before reporting
  timings, and prints EXPLAIN driver-filter reference counts. Requested by
  upstream maintainer `@bee-ghosttrack` on `gastownhall/beads#5339` as the
  gate any future `SearchCountsSQL` shape attempt (join-form bounds,
  temp-table materialization, labels-only bound) must clear before review.
- **Deploy bead:** be-xbycd
- **Review beads:**
  - be-cbnts — round 1, verdict **REQUEST CHANGES**, commit `4ae35a1ea355`
  - be-px3ke — round 2, verdict **PASS**, commit `4894e3c3ba297fc4c6cc38d42ab591efc92f54e2`
- **Commit (pre-rebase, as reviewed):** `4894e3c3ba297fc4c6cc38d42ab591efc92f54e2`
- **Commit (post-rebase, as deployed):** `94630463430967e9216315bcd9fec0468e8876bc`
  — `origin/main` advanced mid-gate (PR #5806, "perf(export): incremental
  auto-export via dolt_diff", merged), so this branch was rebased via the
  deployer's bounded self-rebase exception. Zero path overlap between #5806
  and this diff, confirmed before attempting; rebase was trivial
  (fast-forward-eligible), `--force-with-lease` pushed to `headfork`. See
  "Rebase" below.
- **Branch:** `builder/be-qm6fb` (provenance only); deploy branch
  `deploy/be-px3ke-gate` cut from `4894e3c3ba297fc4c6cc38d42ab591efc92f54e2`,
  rebased onto current `origin/main`, and pushed to `headfork`
  (`quad341/beads-sec003-contrib`) — `origin` (`gastownhall/beads`) has push
  disabled by design on this contributor rig.
- **Evaluated:** 2026-08-22 by beads/deployer

## Scope

New code only, exactly as be-qm6fb specified: the production function under
test, `internal/storage/sqlbuild/counts.go`'s `SearchCountsSQL`, is untouched
(read-only per the bead's own constraint). Diff scope, confirmed via
`git diff --stat origin/main...HEAD` (13 files, 1216 insertions, 0 deletions):

- `BENCHMARKS.md` — doc section: what the harness is, when to run it
- `internal/storage/sqlbuild/bench/shapes.go` (+`shapes_test.go`) — named
  shape-renderer registry; main's shape wrapping `SearchCountsSQL`
- `internal/storage/sqlbuild/bench/rowset.go` (+`rowset_test.go`) — row-set
  fetch + `CompareRowSets` identity check (order-insensitive)
- `internal/storage/sqlbuild/bench/explain.go` (+`explain_test.go`) — EXPLAIN
  PLAN driver-filter reference-count extraction
- `internal/storage/sqlbuild/bench/alternate.go` (+`alternate_test.go`) —
  strict round-robin alternating-round runner
- `internal/storage/sqlbuild/bench/stats.go` (+`stats_test.go`) — per-shape
  timing summary (min/max/mean/spread)
- `internal/storage/sqlbuild/bench/fixture_test.go` — corpus seeding
  (issues + wisps planes, ~5k/~20k rows, DDL via `schema.MigrateUp`)
- `internal/storage/sqlbuild/bench/harness_test.go` — `TestSearchCountsHarness`
  suite: `SetupSuite`, null self-check (both planes), EXPLAIN
  reference-count check, `quoteLiteral` helper + `TestQuoteLiteral`

All 13 files serve the one harness feature; no unrelated files touched —
criterion 7 (single feature theme) holds.

### Round 1 → round 2: what changed

Round 1 (be-cbnts) FAILed on first live-container execution: `SetupSuite`
aborted seeding the issues plane (`Error 1105: Field 'description' doesn't
have a default value` — `issues.description` is `NOT NULL` with no default,
unlike `wisps.description DEFAULT ''`, so the asymmetry wasn't visible from
code-symmetry reasoning alone). Because `SetupSuite` failed on the first seed
batch, 3 of 6 Done-when criteria were entirely unexercised. Round 1 also
flagged a MAJOR security finding: `quoteLiteral()` naively wraps a string in
single quotes with no escaping, spliced as literal SQL text (not a bound
param) — not exploitable today (only ever called with a hardcoded const) but
a SQL-injection-shaped footgun in a harness explicitly meant to be extended.

Round 2 (`bae2d53b5` red / `946304634` green, pre-rebase `6f23b49cb`/
`4894e3c3b`) fixed all of it: `insertMainRows` now supplies the 4 missing
NOT-NULL-no-default columns; `insertDependencyRows` (discovered mid-fix)
now derives `dependencies.id` via the existing `internal/storage/depid.New`;
`TestExplainReferenceCount_Reported` (discovered mid-fix) switched to
`EXPLAIN FORMAT=TREE` after tabular EXPLAIN's literal-text `"NULL"` in the
numeric rows-estimate column made the driver reject the row before `Scan`;
`quoteLiteral` now doubles embedded single quotes (`TestQuoteLiteral` guards
it) with a doc comment warning it's for trusted, in-test literals only.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-px3ke close reason: "Review PASS (round 2, commit 4894e3c3ba297fc4c6cc38d42ab591efc92f54e2). No blockers found across spec/style/security passes." `verdict: pass` recorded in notes. |
| 2 | Acceptance criteria met | **PASS** | All 6 of be-qm6fb's Done-when items re-checked by reviewer against the round-2 run and recorded MET (harness completes end-to-end; null self-check shows no meaningful difference on either plane; row-set identity asserted inside the self-check itself, not just unit-covered; skip-clean behavior unchanged/re-confirmed by inspection; served Dolt version printed; BENCHMARKS.md doc line present). Independently spot-checked the diff-vs-spec mapping myself; no gaps found. |
| 3 | Tests pass | **PASS** | Independently re-run on the rebased HEAD (`94630463430967e9216315bcd9fec0468e8876bc`), not trusted from the reviewer's report. See "Tests run" below. |
| 3b | Policy/lint lane | **PASS** (documented exception) | `make ci-pr-policy` FAILs on one known, pre-existing, diff-unrelated check. `check-testing-short.sh` (directly relevant — this diff adds 6 new test files) independently re-run and PASS. See below. |
| 4 | No unresolved HIGH findings | **PASS** | Round 1's one MAJOR (`quoteLiteral` SQL-injection-shaped footgun) is fixed in round 2 and independently call-site-traced by the reviewer: both call sites pass only a hardcoded const, no attacker-reachable input exists anywhere in the package. One informational, non-blocking residual noted (a trailing-backslash edge case under backslash-escaping SQL modes) — not exploitable at either existing call site, scoped by the new doc comment. |
| 5 | Clean working tree | **PASS** | `git status --short` on `94630463430967e9216315bcd9fec0468e8876bc` clean before this gate file was added; clean again after committing it (see Verdict). |
| 6 | Clean divergence from `origin/main` | **PASS** | `git merge-base --is-ancestor origin/main HEAD` succeeds post-rebase — 4 commits ahead, 0 behind. Required one bounded self-rebase mid-gate; see "Rebase" below. |
| 7 | Single feature theme | **PASS** | All 13 files serve the one in-tree A/B benchmark-harness feature; production file `counts.go` untouched. See "Scope" above. |

### Ancestry-scope check: widened to 3 sibling beads, re-verified post-rebase

`assert_deploy_ancestry_scope origin/main HEAD be-px3ke be-qm6fb be-cbnts` →
**rc=0 (PASS)**.

Widening beyond `be-px3ke` alone was required: 2 of the 4 commits
(round-2 red/green) cite `be-cbnts` in their trailers, not `be-qm6fb` or
`be-px3ke` directly. Confirmed via `bd show be-cbnts` that this is genuinely
the round-1 review bead for this exact same feature (request-changes verdict
that produced the round-2 rework these commits contain) before widening —
not a blanket override, per the function's own documented guidance to name
only ids "you have actually confirmed belong to this deploy."

This check was run twice: once pre-rebase (PASS, against the original
4-commit range) and again just now against the post-rebase HEAD, since
rebase rewrites every commit hash in the range and a stale PASS shouldn't be
assumed to carry over silently. Both passed identically — the rebase was a
clean replay that preserved all commit-message trailers verbatim.

## Rebase (criterion 6 recovery)

`origin/main` advanced from `3641fcf8f` to `893e42cda` (PR #5806 merged)
between the initial fetch and the criterion-6 recheck, flipping criterion 6
to FAIL. Handled via the deployer's sanctioned bounded-self-rebase exception:

1. Confirmed zero path overlap between PR #5806's changed files and this
   diff's 13 files before attempting anything (rebase-safety precondition,
   not assumed).
2. `PUSH_REMOTE=headfork` exported; `attempt_bounded_self_rebase
   deploy/be-px3ke-gate main` → rc=0 (trivial, no conflicts).
3. `BEFORE_SHA=4894e3c3ba297fc4c6cc38d42ab591efc92f54e2`,
   `AFTER_SHA=94630463430967e9216315bcd9fec0468e8876bc`, `--force-with-lease`
   pushed to `headfork`, local/remote SHAs confirmed matching post-push.
4. Audit trail recorded on be-xbycd's notes at the time of the rebase.

## Tests run on release branch (independent re-verification)

Diff-owned tests, run against a real container (not mocked, not a self-skip):

```
cd .gc/worktrees/beads/deployer
DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock \
TESTCONTAINERS_RYUK_DISABLED=true BEADS_TEST_ENV_RUN_DOLT=1 TEST_VERBOSE=1 \
./scripts/test.sh ./internal/storage/sqlbuild/bench/...
```

Container evidence (not taken on trust): testcontainers connected to the
podman socket, created+started container `34129cb6ac14` for
`dolthub/dolt-sql-server:2.2.0`, and logged `served Dolt version: 8.0.31
(pinned image: dolthub/dolt-sql-server:2.2.0)`.

| Result | Count |
|---|---|
| PASS | 20 |
| FAIL | 0 |
| SKIP | 0 |

`ok  	github.com/steveyegge/beads/internal/storage/sqlbuild/bench	20.735s`

All 20 top-level tests PASS, including `TestSearchCountsHarness` (the
previously-blocked suite, all 3 subtests — `TestExplainReferenceCount_Reported`,
`TestNullSelfCheck_IssuesPlane`, `TestNullSelfCheck_WispsPlane` — now
genuinely run and PASS) and the new `TestQuoteLiteral`. This independently
reproduces the reviewer's round-2 claim of 20/20 PASS, 0 FAIL, 0 SKIP, now
on the post-rebase commit rather than the originally-reviewed one — same
result, different HEAD, confirming the rebase introduced no regression.

Additional independent checks on the rebased HEAD:

| Check | Result |
|---|---|
| `go build ./...` | clean, rc=0 |
| `go vet ./...` | clean, rc=0 |
| `gofmt -l` on the 12 diff `.go` files | clean, 0 files listed |

### Policy/lint lane (criterion 3b)

`make ci-pr-policy` → FAIL, root cause: `.githooks/commit-msg` missing
`BEGIN`/`END BEADS INTEGRATION` markers, tripping the version-consistency
sub-step (`scripts/check-versions.sh`). This is a known, previously
root-caused environmental gap (documented precedent: be-y1jo, be-g3iz8, and
others) — `.githooks/commit-msg` is a git-ignored per-session shim, not a
tracked repository file, regenerated every session start and excluded via
`.git/info/exclude`. Independently re-verified rather than taken on
precedent alone:

```
git diff --name-only origin/main...HEAD -- .githooks/commit-msg   # empty — untouched by this diff
git diff origin/main -- .githooks/commit-msg                       # empty — byte-identical to origin/main
grep -n "BEADS INTEGRATION" .githooks/commit-msg                   # no hits — gap is real
```

All other named checks in `check-versions.sh` passed (all 7 version-pinned
files — MCP `pyproject.toml`/`__init__.py`, Claude/Codex `plugin.json`,
Claude `marketplace.json`, npm `package.json`, MCP `uv.lock` — and 5 of 6
`.githooks/*` marker files all show `1.2.2` clean). `check-build-tags.sh` and
`check-go-install-guidance.sh` (the two checks preceding it) both PASS.

`pr-policy.sh` runs under `set -euo pipefail`, so this one failure aborted
the script before its remaining checks (doc-flags, doc-freshness,
`testing.Short()` boundaries, workapi frontend boundary, `.beads/issues.jsonl`
guard, OpenAPI spec gate) executed. Rather than assume those would have
passed, each was independently dispositioned:

- `check-testing-short.sh` — **directly relevant**, since this diff adds 6
  new test files. Run standalone: `testing.Short() usage is limited to
  approved runtime/stress/large-fixture skips.` — **PASS**, rc=0.
- Doc-flags, doc-freshness, workapi-frontend-boundary, `.beads/issues.jsonl`
  guard, OpenAPI spec gate — all scoped to paths this diff does not touch
  (confirmed via `git diff --name-only origin/main...HEAD | grep -E
  '^(docs/|internal/workapi/|\.beads/issues\.jsonl|internal/httpapi/spec/)'`
  → empty). Not independently re-run; out of scope by file-path, same
  evidentiary bar the be-y1jo precedent applied to its own out-of-scope
  checks.

## Findings from reviews (no action required)

Zero HIGH findings across either round. Round 1's one MAJOR (`quoteLiteral`
SQL-injection-shaped footgun) is resolved in round 2 — see "Round 1 → round
2" above. Two minor, non-blocking informational items carried from round 1
(fixed-constant table names in `fixture_test.go`, fixed-prefix DB names in
`CREATE`/`DROP DATABASE` in `harness_test.go`) remain noted for
injection-checklist completeness only; both have provably no exploit path
(no attacker-reachable input). One new informational item from round 2
(`quoteLiteral`'s fix doesn't neutralize a trailing-backslash edge case) is
non-blocking for the same reason — no call site passes anything but a fixed
constant, and the new doc comment scopes the helper to trusted input.

## Verdict

**PASS** — all 7 criteria pass (3b passes with a documented,
independently-verified pre-existing-and-unrelated exception, plus a direct
re-run of the one sub-check this diff's own content makes relevant). Cutting
isolated deploy branch `deploy/be-px3ke-gate` from
`94630463430967e9216315bcd9fec0468e8876bc` (post-rebase), pushing to
`headfork`, and opening a PR against `gastownhall/beads:main`.

**gastownhall/beads merge-authority carve-out:** this is a contributor-only
repository (`origin` push disabled; upstream is fetch-only). Per deployer
protocol, the job ends at the open PR — no merge-request routed to
mayor/mpr, no deploy-clearance status posted. Merge belongs to upstream
maintainers.
