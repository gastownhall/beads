# Release Gate: be-xra8 + be-roho — bd preflight --fix mode

**PR**: https://github.com/gastownhall/beads/pull/4054
**Branch**: `feat/be-xra8-be-roho-preflight-fix-mode` (quad341/beads)
**Date**: 2026-05-21
**Deployer**: beads/deployer

## Gate Result: PASS

| # | Criterion | Evidence | Result |
|---|-----------|----------|--------|
| 1 | Review PASS present | be-nebh: PASS — "CI 41/41; no injection vectors; sentinel-hash trick correct; deferred restore handles all early-exit paths" — 2 LOW notes, no blockers | ✅ PASS |
| 2 | Acceptance criteria met | See below | ✅ PASS |
| 3 | Tests pass | CI run 26188217144 — all 41 checks PASS | ✅ PASS |
| 4 | No high-severity findings | be-nebh: no blockers — LOW-1: fixVersionSync Skipped Detail empty when default.nix absent; LOW-2: fixNixHash fallback regex could pick wrong hash if nix format changes | ✅ PASS |
| 5 | Final branch is clean | `git status` — clean; only rig artifacts untracked | ✅ PASS |
| 6 | Branch diverges cleanly from main | 3 commits ahead of `origin/main`; merge state CLEAN | ✅ PASS |

## Acceptance Criteria Verification (be-xra8 + be-roho)

| Criterion | Evidence | Result |
|-----------|----------|--------|
| `fixNixHash()` implemented — sentinel-hash trick | Function at line 634 in `cmd/bd/preflight.go`; writes `sha256-AAA...` sentinel, runs `nix build .#default`, parses `got: sha256-...` from error output | ✅ |
| `fixNixHash()` skips when `go.sum` has no uncommitted changes | Checks both `git diff HEAD -- go.sum` and `git diff --cached -- go.sum` before proceeding | ✅ |
| `fixNixHash()` exits with manual instructions when `nix` not in PATH | `exec.LookPath("nix")` guard | ✅ |
| `fixNixHash()` preserves original file permissions | `os.Stat` before write; restores with `os.Chmod` | ✅ |
| `fixVersionSync()` implemented — reads version from `default.nix`, updates `version.go` | `func fixVersionSync()` at line in `cmd/bd/preflight.go` | ✅ |
| `fixVersionSync()` skips gracefully when `default.nix` absent | `os.Stat(vgoPath)` check; returns nil (not error) when absent | ✅ |
| `--fix` flag registered on `bd preflight` | `func runFixes(jsonOutput bool)` at line 572; wired via `--fix` flag | ✅ |
| CLI docs regenerated (be-laor blocker resolved) | `5459e436f docs(preflight): regenerate website CLI reference for --fix flag` + `08c428165 docs: sync CLI_REFERENCE.md --fix help text` | ✅ |
| No user-controlled path injection | Reviewer confirmed: `exec.Command` args are fixed strings; `planningPath` is directory path only | ✅ |

**Note on LOW-1** (reviewer): `fixVersionSync` Skipped Detail empty when `default.nix` absent — the `Skipped` status is set but `Detail` is not populated. Cosmetic; non-blocking.
**Note on LOW-2** (reviewer): `fixNixHash` fallback regex `got: sha256-...` could pick wrong hash if nix output format changes. Acceptable risk — the regex matches standard nix hash-mismatch error format documented in `pr-audit-nix-vendorhash-drift-pattern` memory.

**Note on tests**: be-b6m9 (tests for `--fix` mode) is in progress with validator on a separate branch; follow-up PR expected. Reviewer accepted this.

## Commits

| SHA | Description |
|-----|-------------|
| `0f88c3828` | feat(preflight): implement --fix mode for vendorHash and version sync |
| `08c428165` | docs: sync CLI_REFERENCE.md --fix help text with live CLI (be-xra8, be-roho) |
| `5459e436f` | docs(preflight): regenerate website CLI reference for --fix flag (be-laor) |
