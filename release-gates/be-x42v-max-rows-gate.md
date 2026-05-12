# Release gate — `BEADS_MAX_ROWS` defensive row cap (be-x42v.1 + be-x42v.2)

- **Builder beads (CLOSED):**
  - be-x42v.1 — Storage foundation (`IssueFilter.MaxRows` + `ErrTooManyRows` + `searchTableInTx` integration)
  - be-x42v.2 — CLI wiring (`--max-rows` flag, `BEADS_MAX_ROWS` env, error formatting, exit codes, CHANGELOG)
- **Review beads:**
  - be-xf0o — Verdict **PASS** (Opus 4.7 reviewer) on commit `3900c1bdc`
  - be-2p12 — Verdict **PASS** (Opus 4.7 reviewer) on commit `dea537c56`
- **Commits shipped:** `3900c1bdc` (storage foundation) then `dea537c56` (CLI wiring), 23 files, +519/-30 over `origin/main` da73b7511
- **Branch:** `feat/be-x42v.1-max-rows-foundation` on `fork` (`quad341/beads`)
- **Evaluated:** 2026-05-12 by beads/deployer

## Scope

Adds an opt-in process-level row-count safety net to `bd`'s storage and CLI:

- Flag `--max-rows N` on `bd list`, `bd ready`, `bd dep tree`,
  `bd find-duplicates`, `bd graph`.
- Env var `BEADS_MAX_ROWS=N` honored on the five above plus `bd lint`,
  `bd doctor-conventions`, `bd doctor-pollution` (env-only per designer §4).
- Explicit opt-out (`MaxRows=0`, `MaxRowsSource=""`) wired at every
  `IssueFilter` constructor in `bd cleanup`, `bd gc`, `bd export`,
  `bd export --auto`, `bd migrate-issues`, `bd jira`.
- Storage: `internal/types.IssueFilter.MaxRows`/`MaxRowsSource`,
  `*issueops.ErrTooManyRows{Found, Cap, Source}`, exported helpers
  `EffectiveSearchLimit` and `EnforceMaxRowsCap`. Cap evaluated after the
  `issues + wisps` merge in `SearchIssuesInTx`; `searchTableInTx` issues
  `LIMIT cap+1` for bounded over-fetch. `WorkFilter.MaxRows` mirror with
  `GetReadyWorkInTx` enforcement.
- `bd dep tree` applies the cap at the CLI layer (post-walk `TreeNode`
  count) because tree walks don't flow through `IssueFilter`.
- `bd config show` lists `BEADS_MAX_ROWS` via a new standalone-env
  collector.
- `CHANGELOG.md` Unreleased / Added entry; `CONTRIBUTING.md` storage
  filter convention note on the explicit opt-out rule.

Default behavior unchanged: `MaxRows=0` is byte-identical to today on
every code path.

**Explicit deferrals (documented in the bead, not gate failures):**
- PG-backend cap parity (PG package is on the long-running
  `postgres-backend` feature branch; helpers exported for reuse).
- Validator coverage for non-list command paths (filed as be-u8z9, routed
  to validator).
- Validator coverage for the 10 designer §5 CLI scenarios (be-x42v.3,
  still open).
- Opt-out command test gates (be-x42v.4 validator).

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-xf0o (foundation) recorded `Review verdict: PASS` on 2026-05-12 11:27; be-2p12 (CLI) recorded `Review verdict: PASS` on 2026-05-12 12:28. Both single-pass (Opus 4.7); gemini second-pass currently disabled. |
| 2 | Acceptance criteria met | **PASS** | All acceptance items in both bead descriptions checked against code (see "Acceptance" below). PG-backend cap is explicitly deferred per the foundation bead. |
| 3 | Tests pass | **PASS** | See "Tests run on release branch" below. |
| 4 | No high-severity review findings open | **PASS** | Zero HIGH findings across both reviews. Three info-level findings from be-xf0o, all non-blocking and either justified-improvements or heads-up notes for follow-up validator/CLI work. |
| 5 | Final branch is clean | **PASS** | `git status` on the detached checkout shows only worktree-scaffolding untracked paths (`.gc/`, `.gitkeep`) which are never staged. |
| 6 | Branch diverges cleanly from main | **PASS** | `git merge-base --is-ancestor origin/main HEAD` confirms HEAD descends from `origin/main` (two commits on top of da73b7511; no rebase needed). |

## Acceptance (per be-x42v.1)

| Criterion | Status | Evidence |
|---|---|---|
| `IssueFilter.MaxRows int` + `MaxRowsSource string` with godoc | ✓ | `internal/types/types.go`; defaults zero/empty preserve today's behavior at every call site. |
| `*issueops.ErrTooManyRows{Found, Cap, Source}` typed error with `Error()` formatting | ✓ | `internal/storage/issueops/errors.go`; both Source-set and Source-empty branches covered by `TestErrTooManyRows_Error_With{Source,outSource}`. |
| `searchTableInTx` integration: LIMIT cap+1 when `MaxRows>0`; cap check returns `ErrTooManyRows` on overage | ✓ | `internal/storage/issueops/search.go`; routes through `EffectiveSearchLimit` helper. |
| Cap check applied after issues+wisps merge in `SearchIssuesInTx` | ✓ | Two call sites: early-return ephemeral path and post-merge path. Strictly better than the spec's pseudocode (handles union overflow when neither leg individually exceeds cap). |
| Storage happy-path test (`TestSearchIssues_MaxRows_Exceeded_ReturnsErrTooManyRows`) passes with `errors.As` Found/Cap/Source assertion | ✓ | Three sub-cases PASS. |
| Default `MaxRows=0` byte-identical to today | ✓ | `EffectiveSearchLimit(*,0)` returns the original limit; `EnforceMaxRowsCap(*,0,*)` returns nil. |
| PG-backend equivalent | **Deferred** (documented) | PG package not on `origin/main`; helpers exported for reuse when PG support lands. |

## Acceptance (per be-x42v.2)

| Criterion | Status | Evidence |
|---|---|---|
| `--max-rows int` flag on `bd list`, `bd ready`, `bd dep tree`, `bd find-duplicates`, `bd graph` | ✓ | `grep addMaxRowsFlag cmd/bd/` shows the five expected call sites. |
| Help text matches designer §2.2 verbatim | ✓ | `bd list --help` shows the prescribed text including "Hard upper bound … exit code 2 … 0 disables (the default). Overrides BEADS_MAX_ROWS for this invocation." |
| `BEADS_MAX_ROWS` env on the 5 above plus `bd lint`, `bd doctor-conventions`, `bd doctor-pollution` | ✓ | `resolveMaxRowsEnvOnly` wired in `cmd/bd/lint.go`, `doctor_conventions.go`, `doctor_pollution.go`. |
| Opt-out sites set explicit `MaxRows=0`, `MaxRowsSource=""` | ✓ | `grep -l 'MaxRows: *0' cmd/bd/{cleanup,gc,export,export_auto,migrate_issues,jira}.go` → all six present. |
| Error format: exit 2; two-line stderr; stdout empty; no ANSI | ✓ | Reviewer behavioral verification (designer §5 scenarios) on a fresh 10-issue DB matches the spec exactly; `handleMaxRowsError` formats from the typed `Found`/`Cap`/`Source` fields, not `err.Error()` passthrough. |
| `--max-rows -1` → exit 1, "must be non-negative" | ✓ | Reviewer-verified. |
| `WorkFilter.MaxRows` + `MaxRowsSource` added; `GetReadyWorkInTx` uses `EffectiveSearchLimit`/`EnforceMaxRowsCap` | ✓ | `internal/types/types.go` + `internal/storage/issueops/ready_work.go`. |
| `bd dep tree` cap at CLI layer (TreeNode count) | ✓ | `cmd/bd/dep.go` applies cap post-walk; appropriate since tree walks don't use `IssueFilter`. |
| `bd config show` lists `BEADS_MAX_ROWS` | ✓ | New `collectStandaloneEnvEntries` collector in `cmd/bd/config_show.go`. |
| `CHANGELOG.md` Unreleased / Added entry | ✓ | Present. |
| `CONTRIBUTING.md` storage-filter-conventions section | ✓ | Present; documents the explicit `MaxRows=0` opt-out rule for new commands. |

## Tests run on release branch

| Test | Result | Notes |
|------|--------|-------|
| `go build -tags gms_pure_go ./cmd/bd/` | success | clean build. |
| `go test -tags gms_pure_go ./internal/storage/issueops/ ./internal/types/` | PASS | targeted change packages. |
| `go test -tags gms_pure_go ./internal/storage/embeddeddolt/ ./internal/storage/schema/` | PASS | downstream consumers of `SearchIssuesInTx`. |
| `go test -tags gms_pure_go -v -run 'TestEffectiveSearchLimit\|TestEnforceMaxRowsCap\|TestSearchIssues_MaxRows\|TestErrTooManyRows' ./internal/storage/issueops/` | PASS (all sub-tests) | 7-case `TestEffectiveSearchLimit`, three `TestEnforceMaxRowsCap_*`, three `TestSearchIssues_MaxRows_Exceeded_ReturnsErrTooManyRows` sub-cases, two `TestErrTooManyRows_Error_*`. |
| `go vet -tags gms_pure_go ./internal/storage/issueops/ ./internal/types/ ./cmd/bd/` | clean | no output. |
| `golangci-lint run --build-tags gms_pure_go ./cmd/bd/... ./internal/storage/issueops/... ./internal/types/...` | 0 issues | matches reviewer's lint result. |
| `bd list --help` smoke (--max-rows flag presence + spec text) | matches §2.2 | verified locally on this branch. |

The reviewer's pre-existing rig-isolation failure
(`internal/storage/dolt/TestApplyConfigDefaults_*` env-leak from
`GC_DOLT_PORT=28231`) is documented as unrelated and reproducible on
`origin/main`. Not a blocker — those tests do not exercise the changed
code paths.

## Findings from reviews (no action required)

From be-xf0o (foundation review):

- **F1 (info):** Cap-placement moved from `searchTableInTx` (per spec
  pseudocode) up to `SearchIssuesInTx` post-merge. Strictly better — the
  union of issues+wisps legs can exceed the cap when neither leg does.
  Accepted as a justified improvement over the spec.
- **F2 (info):** `ErrTooManyRows.Error()` wording diverges from designer
  §3.2 (`"too many rows: %d found, %s=%d exceeded"`) and emits
  `"search returned %d rows, exceeding %s cap of %d"` instead. Not
  blocking because the CLI bead be-x42v.2 owns the user-visible message
  and formats it from the typed `Found`/`Cap`/`Source` fields (verified
  in `handleMaxRowsError`), not from `Error()` passthrough.
- **F3 (info):** `doltTransaction.SearchIssues` (the Transaction-interface
  variant in `transaction.go:222`) has its own implementation that
  ignores `filter.MaxRows`. No production caller in `cmd/bd` uses
  `tx.SearchIssues` today (grep confirmed). Validator bead be-x42v.3 to
  exercise this path or file a follow-up to wire the cap.

From be-2p12 (CLI review): no findings open.

## Push target

`origin` (`gastownhall/beads`) denies push to `quad341` (deployer's
GitHub identity); `fork` (`quad341/beads`) accepts. PR opens cross-repo
against `gastownhall/beads:main` with head
`quad341:feat/be-x42v.1-max-rows-foundation`.

## Verdict

**PASS** — push the gate-file commit to `fork`, open PR with both
commits.
