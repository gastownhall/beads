# Release gate — be-qd2vmt (Dolt-only label-filter ship)

- **Bead:** be-qd2vmt (Ship: Dolt-only cherry-pick of be-ucslk4 label-filter regression fix)
- **Source bead:** be-ucslk4 (label-filter fix) + be-acnquj (JSON truncation hint)
- **Branch:** `release/be-ucslk4-dolt-ship` off `origin/main` (6369ecca7)
- **Commits cherry-picked:**
  - `bfb7f9475` ← `f3a38aa8e` — fix(list): suppress truncation hint in --json mode + lock-in label filter tests (be-acnquj)
  - `963c33e36` ← `67925ad54` — fix(filters): wire LabelPattern/LabelRegex through SQL builder (be-ucslk4)
- **Evaluated:** 2026-05-15 by beads/deployer

## Gate criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | **PASS** | be-c3uv3v (review bead) has verdict PASS in notes; be-fgi5gg (Review: be-acnquj) also PASS |
| 2 | Acceptance criteria met | **PASS** | AC#7 (be-acnquj): `--json` suppresses truncation hint. AC#1–5 (be-ucslk4, Dolt): LabelPattern + LabelRegex wired in `internal/storage/issueops/filters.go` |
| 3 | Tests pass | **PASS** | `go test ./internal/storage/issueops/` → `ok 0.005s` |
| 4 | No high-severity review findings open | **PASS** | be-c3uv3v: 0 HIGH findings; pre-existing test_helpers build conflict documented as pre-existing, not introduced by this ship |
| 5 | Final branch is clean | **PASS** | `git status` clean (no uncommitted changes) |
| 6 | Branch diverges cleanly from main | **PASS** | Both cherry-picks applied with no conflicts; `make build` clean |

## Notes

- Third commit (1f12523af, PG SearchIssues) is **intentionally excluded** — it requires the full PG rollup (be-rhtega) which is gated on mayor's greenlight.
- Pre-existing `cmd/bd` test_helpers build conflict (test_helpers_test.go vs test_helpers_pure_test.go duplicate symbols under `gms_pure_go,dolt_only,cgo=1`) reproduces on parent and is not introduced by these commits. Issueops tests (the affected ACs) are in a separate package and pass cleanly.
- `golangci-lint run ./internal/storage/issueops/ ./cmd/bd/` → 0 issues.

## Verdict: PASS
