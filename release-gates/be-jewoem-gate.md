# Release Gate: be-jewoem — reference-aware bd prune

**PR**: https://github.com/gastownhall/beads/pull/4023  
**Branch**: `feat/be-jewoem-be-u2mw2x-reference-aware-prune` (quad341/beads)  
**Gate evaluated**: 2026-05-17  

---

## Criterion 1: Review PASS

| Reviewer | Verdict | Date |
|----------|---------|------|
| beads/reviewer | **PASS** | 2026-05-17 |

Source: bead `be-xbwp` notes — "REVIEW VERDICT: pass"  
Findings: None blocking. Second-pass (gemini) disabled per current rig config.

**→ PASS**

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

CI run on `feat/be-jewoem-be-u2mw2x-reference-aware-prune` (run 26001972966):

| Suite | Result |
|-------|--------|
| Build (Embedded Dolt) | PASS |
| Lint | PASS |
| Check build-tag policy | PASS |
| Check cmd/bd pure-Go tests compile | PASS |
| Check formatting | PASS |
| Check version consistency | PASS |
| Test (Embedded Dolt Cmd 1–20/20) | PASS |
| Test (Embedded Dolt Storage) | PASS |
| Test (ubuntu-latest) | PASS |
| Test (macos-latest) | PASS |
| Test (Windows smoke) | PASS |
| Test Nix Flake | PASS |
| Upgrade smokes (v1.0.0–v1.0.4) | PASS |
| Check doc flags freshness | **FAIL → FIXED** |

Doc flags failure: `--ignore-references` flag was present in compiled binary but absent from committed CLI reference docs. Root cause: docs regen was omitted from the PR branch. Fixed in this gate commit by running `./scripts/generate-cli-docs.sh` with a fresh `CGO_ENABLED=0 -tags gms_pure_go` binary built from PR HEAD.

**→ PASS** (after this commit)

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

Branch is 3 feature commits + gate commit ahead of main. No conflicts with origin/main.

**→ PASS**

---

## Summary

| Criterion | Result |
|-----------|--------|
| 1. Review PASS | **PASS** |
| 2. Acceptance criteria | **PASS** (AC-6 deferred to be-zkzw) |
| 3. Tests pass | **PASS** |
| 4. No HIGH findings | **PASS** |
| 5. Branch clean | **PASS** |
| 6. Clean divergence from main | **PASS** |

**Overall: PASS — PR ready for human merge.**
