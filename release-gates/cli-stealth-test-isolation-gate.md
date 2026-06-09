# Release Gate: CLI Stealth Test Isolation

Date: 2026-06-09

Repo: `/home/jaword/projects/beads`

Deploy bead: `ga-fa9l1x`

Source bead: `ga-7psli2.1`

Review bead: `ga-4fqrwl`

Base: `origin/main` at `9a1c88b63aee89b091c9db7e5330a48cb4911987`

Reviewed head: `1721632a02db45e90310cb3e9fab647f484f778e`

Feature branch: `fix/cli-stealth-test-isolation`

## Summary

PASS. The reviewed change is a test-only isolation fix in
`cmd/bd/prime_test.go`. It prevents `TestOutputContextFunction` from reading
live stored memories while asserting structural prime output, so local memory
contents such as git-operation text cannot fail the CLI stealth cases.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | Review bead `ga-4fqrwl` is closed with note: `PASS: test-only fix verified`. |
| 2 | Acceptance criteria met | PASS | Source bead `ga-7psli2.1` criteria checked below. Diff is limited to `cmd/bd/prime_test.go` (+25 lines). |
| 3 | Tests pass | PASS | `make build` PASS; focused `go test ./cmd/bd/ -run '^TestOutputContextFunction$' -v` PASS with all 14 subtests; `make test` PASS with total coverage 37.1%. |
| 4 | No high-severity review findings open | PASS | Review notes contain PASS and no HIGH findings. |
| 5 | Final branch is clean | PASS | Clean deploy worktree had no uncommitted or untracked files before adding this gate file; final status checked again after commit. |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main HEAD` completed without conflicts and produced tree `daeeacabfa4c84d6bd1df7cebd9495e1a850b9d0`. |
| 7 | Single feature theme | PASS | Single test-only change in `cmd/bd/prime_test.go`; one subsystem and one behavior: deterministic prime output tests. |

## Acceptance Criteria

| Criterion | Result | Evidence |
|-----------|--------|----------|
| Reproduce or cite the focused `CLI_Stealth` failure and confirm it is independent of SEC-003. | PASS | Source bead `ga-7psli2.1` records the failed SEC-003 gate artifact and confirms the same focused failure on clean `origin/main` `9a1c88b63aee89b091c9db7e5330a48cb4911987`. |
| Produce a separate baseline-blocker fix based on current `origin/main` and scoped to the CLI stealth blocker. | PASS | Reviewed head `1721632a02db45e90310cb3e9fab647f484f778e` changes only `cmd/bd/prime_test.go`; no SEC-003 changes are present. |
| Focused CLI stealth cases no longer fail with `Unexpected text found: git status`. | PASS | `go test ./cmd/bd/ -run '^TestOutputContextFunction$' -v` passed all 14 subtests, including `CLI_Stealth` and `CLI_Stealth_overrides_local-only`. |
| Run the project test gate needed to prove the release blocker is cleared. | PASS | `make test` passed across `./...`; total coverage was 37.1%. |
| Handoff notes record branch, base commit, head commit, diff scope, commands run, and artifact paths. | PASS | Source bead notes and this gate record branch, base, reviewed head, diff scope, test commands, and gate path. |

## Commands

```text
scripts/pr-preflight.sh --search "CLI_Stealth test isolation prime TestOutputContextFunction stubNoMemories" --repo gastownhall/beads
make build
go test ./cmd/bd/ -run '^TestOutputContextFunction$' -v
make test
git merge-tree --write-tree origin/main HEAD
git diff --check origin/main...HEAD
```

## Test Output Summary

```text
make build: PASS
focused TestOutputContextFunction: PASS, all 14 subtests
make test: PASS
Total coverage: 37.1% (profile: /tmp/beads.coverage.out)
```
