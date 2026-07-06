# Release Gate: be-drsfc2 — schema: print migration name to stderr on apply

**Result: GATE PASS**
**Date:** 2026-06-13
**Deployer:** beads/deployer
**Branch under review:** `fix/be-drsfc2-migration-progress` (3 feature commits ahead of origin/main)
**Branch tip:** `875d586a4febf7946d26f2a864889a91f05560b3`
**Target:** origin/main (`e8ae7a291`)
**PR:** https://github.com/gastownhall/beads/pull/3914
**Deploy bead:** be-soq0

---

## Criteria Checklist

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | **PASS** | beads/reviewer (Claude Sonnet 4.6) PASS at `875d586a4` — be-hlo7 bead notes, 2026-06-13. |
| 2 | Acceptance criteria met | **PASS** | See below. All criteria verified by reviewer against current tip. |
| 3 | Tests pass | **PASS** | `go test ./internal/storage/schema/...` → 62+ PASS, 0 FAIL (1.291s). CI Gate Required: PASS (both runs). See below. |
| 4 | No high-severity review findings open | **PASS** | Reviewer: "No blockers." One informational note (PR sequencing with #3919) is non-blocking. |
| 5 | Final branch is clean | **PASS** | `git status` clean. |
| 6 | Branch diverges cleanly from main | **PASS** | `git merge --no-commit --no-ff origin/main` → "Already up to date." Zero conflicts. MERGEABLE per GitHub. |
| 7 | Single feature theme | **PASS** | All commits address one behavior: print migration name to stderr when a TTY is attached. Simpler, terminal-gated sibling of PR #3919. |

---

## Criterion 2: Acceptance Criteria

| Spec | Code | Verdict |
|------|------|---------|
| Print migration name to stderr on apply | `fmt.Fprintf(stderr, "migrating schema: %s\n", ...)` per migration | ✅ |
| Only when stderr is a TTY (CI/pipe-friendly) | `defaultStderr()` uses `golang.org/x/term` to check; returns `io.Discard` when not a TTY | ✅ |
| `migrationSource` passed through `runMigrations` | Commit `81f1c7f79` adds `src migrationSource` param; previously always used `mainSource` | ✅ |
| Coverage: emission and line count | `TestRunMigrationsStderrOutput` verifies 1 line per migration | ✅ |
| Coverage: respects `src` argument | `TestRunMigrationsUsesProvidedSource` proves `runMigrations` uses the provided source | ✅ |

---

## Criterion 3: Test Run

```
go test -v -count=1 ./internal/storage/schema/...
...
=== RUN   TestRunMigrationsStderrOutput
--- PASS: TestRunMigrationsStderrOutput (0.00s)
=== RUN   TestRunMigrationsUsesProvidedSource
--- PASS: TestRunMigrationsUsesProvidedSource (0.00s)
PASS
ok  	github.com/steveyegge/beads/internal/storage/schema	1.291s
```

`go build ./internal/storage/schema/...` — PASS (via CI Build Artifacts: PASS)
`go vet ./internal/storage/schema/...` — PASS
CI Gate / Required — PASS (PR #3914, both runs: 27475151206 + 27475151232)

---

## Commits in PR

| Commit | Message |
|--------|---------|
| `8b939d27c` | schema: print migration name to stderr when running (human UX only) |
| `81f1c7f79` | fix(schema): pass migrationSource to runMigrations, not just mainSource |
| `875d586a4` | schema: suppress migration progress when stderr is not a TTY |

---

## Sequencing Note

PR #3919 (`rebase/be-ldr-2026-05-12`) also modifies the same `runMigrations` extraction site with a richer implementation (adds progress timing and large-rig warning). Both PRs independently PASS. Mayor/mpr must merge one first; the second will need a rebase. Recommended merge order: either works — #3914 is a simpler subset; #3919 is a superset. If #3919 merges first, #3914 may become redundant (superset already includes the name-to-stderr behavior).
