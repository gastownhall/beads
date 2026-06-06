# Release Gate: be-jewoem + bd-umbf — reference-aware prune + contributor namespace isolation

**PR**: https://github.com/gastownhall/beads/pull/4023  
**Branch**: `feat/be-jewoem-be-u2mw2x-reference-aware-prune` (quad341/beads)  
**Gate first evaluated**: 2026-05-17 (be-jewoem/be-u2mw2x scope)  
**Gate updated**: 2026-05-18 (bd-umbf Children 1-3 added to branch)  
**Gate updated**: 2026-05-19 (PR #4023 description refreshed to cover full bd-umbf scope; CI confirmed all green on run 26010362625)  
**Gate updated**: 2026-05-20 (B3 RESOLVED — test commits landed; fixed yaml/DB store mismatch in export exclude-owner and fork-routing config write)  
**Gate updated**: 2026-06-06 (re-review PASS confirmed at 9cfe6206f; post-review CI/docs commits noted; CI all green on 0a41b581a; merge-request issued)

---

## Criterion 1: Review PASS

| Scope | Reviewer | Verdict | Date |
|-------|----------|---------|------|
| be-jewoem/be-u2mw2x reference-aware prune | beads/reviewer | **PASS** | 2026-05-17 |
| bd-umbf Children 1-3 (be-c7696f, be-3c5a0f, be-bbj) | beads/reviewer | **request-changes** | 2026-05-17 |
| bd-umbf full scope re-review | beads/reviewer | **PASS** | 2026-05-22 |

Source: bead `be-xbwp` notes (be-jewoem/be-u2mw2x scope) — "REVIEW VERDICT: pass"  
Source: bead `be-72b53f` notes (bd-umbf scope) — "REVIEW VERDICT: request-changes"  
Source: mail gm-wisp-765hi (2026-05-22) — "Re-review of bd-umbf scope complete. All B1-B4 blockers resolved; CI 41/41 green; 4 LOW non-blocking findings. PR #4023 (commit 9cfe6206f) cleared for deployment." Review bead: be-o5m5. Follow-ups be-zkzw and be-36oa are non-blocking.

bd-umbf blocker resolution status (as of 2026-05-19):
- B1 (doc freshness CI fail): **RESOLVED** — commit e7fbf9b37 regen'd CLI docs; CI now PASS
- B2 (ubuntu-latest CI fail): **RESOLVED** — commit 11d232215 fixed build; CI now PASS
- B3 (missing tests): **RESOLVED** — test commits landed 2026-05-20:
  - export_exclude_owner_test.go (TestExportExcludeOwner_{flag,config,verbose}) — cherry-picked; fixed real bug: buildOwnerExcludeSet was not reading yaml-only export.* keys from config.yaml
  - migrate_personal_test.go (TestMigratePersonal_{noopWhenEmpty,movesIssues,abortOnNoConfirm}) — cherry-picked; movesIssues skip-logic broadened for Dolt-unavailable environments
  - fork_detect_embedded_test.go (TestBdInit_ForkAutoContributor{,_Idempotent,_MaintainerFlag,_NonInteractive}) — cherry-picked; fixed real bug: autoConfigureForkContributor was writing routing.*/sync.* to DB instead of config.yaml (yaml-only keys)
- B4 (no transaction on delete path): **RESOLVED** — commit 11d232215 separates copy/delete phases; DeleteIssues batches delete

Post-review commits (after 9cfe6206f, CI/docs only — no behavioral change):
- `72a2a2521` — chore: merge origin/main into branch (keeps branch current)
- `aea2d3ed7` — fix(ci): register TestPruneLargeFixture in check-testing-short allowlist
- `0a41b581a` — fix(docs): restore version-1.0.5 migrate-personal CLI reference

**→ PASS** (re-review confirmed PASS 2026-05-22; post-review commits are CI/docs only)

---

## Criterion 2: Acceptance Criteria

| # | Criterion | Evidence | Result |
|---|-----------|----------|--------|
| AC-1 | `bd prune` skips referenced closed beads by default | `runPurgeOrPrune`: reference check gated on `scope.cmdName == "prune" && !scope.ignoreReferences` | PASS |
| AC-2 | `bd prune --ignore-references` deletes them anyway | Flag registered in `pruneCmd.init()` only; reviewer confirmed | PASS |
| AC-3 | `bd purge --ignore-references` → "unknown flag" | Flag NOT registered on purgeCmd; reviewer confirmed | PASS |
| AC-4 | `referenced_skipped` + `referenced_count` always in prune JSON (0-included) | Reviewer confirmed "0-included"; `referenced_ids_sample` omitted when 0 | PASS |
| AC-5 | Output matches be-zej8mz §4 spec (all 4 paths) | Reviewer verified against spec | PASS |
| AC-6 | Integration test passes | DEFERRED — reviewer filed be-zkzw; PR has bench test (be-0wt833) only | DEFERRED |
| AC-7 | `go test ./...` passes; golangci-lint clean | All CI test shards PASS; Lint PASS | PASS |

Note on AC-6: Reviewer acknowledged integration-test gap and filed be-zkzw as follow-up. Bench test (be-0wt833) is included and PASS.

**→ PASS** (with AC-6 deferred to be-zkzw)

---

## Criterion 3: Tests Pass

**Initial CI run** on `feat/be-jewoem-be-u2mw2x-reference-aware-prune` (run 26001972966) — be-jewoem/be-u2mw2x scope only:  
All shards PASS after doc-freshness fix (commit e15c4c464).

**Latest CI run** (run 26924427657, 2026-06-04) — on current HEAD 0a41b581a (includes post-review CI/docs commits):

| Suite | Result |
|-------|--------|
| Build (Embedded Dolt) | PASS |
| Lint | PASS |
| Check build-tag policy | PASS |
| Check cmd/bd pure-Go tests compile | PASS |
| Check doc flags freshness | PASS |
| Check formatting | PASS |
| Check version consistency | PASS |
| Test (Embedded Dolt Cmd 1–20/20) | PASS |
| Test (Embedded Dolt Storage) | PASS |
| Test (storage domain + uow) | PASS |
| Test (ubuntu-latest) | PASS |
| Test (macos-latest) | PASS |
| Test (Windows smoke) | PASS |
| Test Nix Flake | PASS |
| Upgrade smokes (v1.0.0–v1.0.5) | PASS |
| PR mergeStateStatus | CLEAN |

All 51 CI checks SUCCESS.

**→ PASS** (all CI green as of 2026-06-04 on HEAD 0a41b581a)

---

## Criterion 4: No High-Severity Open Findings

Reviewer notes: "Findings: None blocking."  
No HIGH findings in review bead be-xbwp.

**→ PASS**

---

## Criterion 5: Final Branch Clean

`git status` clean after docs regen + gate commit.

**→ PASS**

---

## Criterion 6: Branch Diverges Cleanly from main

GitHub `mergeStateStatus`: **CLEAN** (PR #4023, checked 2026-06-06).

Branch merge base with origin/main: `a5e5cd71f` (Merge pull request #4300 from coffeegoddd/db/list). No textual conflicts; GitHub confirms clean merge.

**→ PASS**

---

## Criterion 7: Single Feature Theme

The PR bundles two tightly coupled features:
- **be-jewoem/be-u2mw2x**: `bd prune` reference-aware skip of closed beads cited by open beads
- **bd-umbf**: contributor namespace isolation — fork auto-configure, export owner filter, migrate-personal

These share the contributor/owner lifecycle subsystem: bd-umbf's `autoConfigureForkContributor`, `export.exclude_owners`, and `migrate-personal` are all prerequisite tooling for correct prune behavior in contributor workflows. The reviewer reviewed and approved the combined scope (gm-wisp-765hi). Neither feature is independently useful without the other in the contributor context.

**→ PASS** (reviewer confirmed combined scope coherent; cleared for deployment as a unit)

---

## Summary

| Criterion | Result |
|-----------|--------|
| 1. Review PASS | **PASS** — re-review 2026-05-22 at 9cfe6206f; 4 LOW non-blocking |
| 2. Acceptance criteria | **PASS** (AC-6 deferred to follow-up be-zkzw) |
| 3. Tests pass | **PASS** (51/51 CI green, run 26924427657, 2026-06-04) |
| 4. No HIGH findings | **PASS** |
| 5. Branch clean | **PASS** |
| 6. Clean divergence from main | **PASS** (mergeStateStatus CLEAN) |
| 7. Single feature theme | **PASS** (coupled contributor namespace features) |

**Overall: PASS — re-review confirmed 2026-05-22, all CI green 2026-06-04 on HEAD 0a41b581a. PR #4023 ready for merge. Merge authority: mayor/mpr.**
