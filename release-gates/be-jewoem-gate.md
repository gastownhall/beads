# Release Gate: be-jewoem + bd-umbf — reference-aware prune + contributor namespace isolation

**PR**: https://github.com/gastownhall/beads/pull/4023  
**Branch**: `feat/be-jewoem-be-u2mw2x-reference-aware-prune` (quad341/beads)  
**Gate first evaluated**: 2026-05-17 (be-jewoem/be-u2mw2x scope)  
**Gate updated**: 2026-05-18 (bd-umbf Children 1-3 added to branch)

---

## Criterion 1: Review PASS

| Scope | Reviewer | Verdict | Date |
|-------|----------|---------|------|
| be-jewoem/be-u2mw2x reference-aware prune | beads/reviewer | **PASS** | 2026-05-17 |
| bd-umbf Children 1-3 (be-c7696f, be-3c5a0f, be-bbj) | beads/reviewer | **request-changes** | 2026-05-17 |

Source: bead `be-xbwp` notes (be-jewoem/be-u2mw2x scope) — "REVIEW VERDICT: pass"  
Source: bead `be-72b53f` notes (bd-umbf scope) — "REVIEW VERDICT: request-changes"

bd-umbf blocker resolution status (as of 2026-05-18):
- B1 (doc freshness CI fail): **RESOLVED** — commit e7fbf9b37 regen'd CLI docs; CI now PASS
- B2 (ubuntu-latest CI fail): **RESOLVED** — commit 11d232215 fixed build; CI now PASS
- B3 (missing tests): **PENDING** — needs-tests beads filed for validator (TestBdInit_ForkAutoContributor*, TestMigratePersonal_*)
- B4 (no transaction on delete path): **RESOLVED** — commit 11d232215 separates copy/delete phases; DeleteIssues batches delete

**→ ON HOLD** (bd-umbf B3 pending validator; be-jewoem/be-u2mw2x scope PASS)

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

**Latest CI run** (run 26009209774) — includes bd-umbf commits be787e4b5, 9ed33b68d, 11d232215, e7fbf9b37:

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
| Test (ubuntu-latest) | PASS |
| Test (macos-latest) | PASS |
| Test (Windows smoke) | PASS |
| Test Nix Flake | PASS |
| Upgrade smokes (v1.0.0–v1.0.4) | PASS |

**→ PASS** (all CI green as of 2026-05-18)

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

Merge base with `origin/main`: `c72581c8b` (Merge pull request #4017 from coffeegoddd/db/schema-lock — already merged to main).

Branch is 7 commits ahead of main (3 be-jewoem/be-u2mw2x + 1 gate + 3 bd-umbf fix/docs commits). No conflicts with origin/main.

**→ PASS**

---

## Summary

| Criterion | Result |
|-----------|--------|
| 1. Review PASS | **ON HOLD** — bd-umbf B3 (tests) pending validator |
| 2. Acceptance criteria | **PASS** (be-jewoem AC-6 deferred to be-zkzw; bd-umbf AC pending tests) |
| 3. Tests pass | **PASS** (CI all green) |
| 4. No HIGH findings | **PASS** |
| 5. Branch clean | **PASS** |
| 6. Clean divergence from main | **PASS** |

**Overall: ON HOLD — awaiting validator to write TestBdInit_ForkAutoContributor* and TestMigratePersonal_* tests (needs-tests beads filed 2026-05-18). All CI green; other blockers resolved.**
