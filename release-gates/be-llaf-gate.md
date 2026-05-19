# Release Gate: be-llaf — fix(import): sync issue_prefix from config.yaml after import

**PR**: https://github.com/gastownhall/beads/pull/4029
**Branch**: `fix/be-llaf-sync-prefix-on-import` (quad341/beads)
**Date**: 2026-05-19
**Deployer**: beads/deployer

## Gate Result: PASS

| # | Criterion | Evidence | Result |
|---|-----------|----------|--------|
| 1 | Review PASS present | be-rev-5e50248 PASS (2026-05-19); be-70xe PASS (duplicate, closed) | ✅ PASS |
| 2 | Acceptance criteria met | See below | ✅ PASS |
| 3 | Tests pass | CI run 26114120267 — all 30 checks PASS (ubuntu, macOS, Windows smoke, Embedded Dolt cmd 1–20, Storage, lint, fmt, doc freshness, Nix, upgrade smokes) | ✅ PASS |
| 4 | No high-severity findings | be-rev-5e50248: "None blocking" — one INFO (best-effort silent success for sync op). be-70xe: "None blocking" — same pattern. | ✅ PASS |
| 5 | Final branch is clean | `git status` — clean; only `.gc/` and `.gitkeep` untracked (rig artifacts, not repo content) | ✅ PASS |
| 6 | Branch diverges cleanly from main | 2 commits ahead of `origin/main`; `git merge-tree` shows 0 conflicts | ✅ PASS |

## Acceptance Criteria Verification (be-llaf)

**Bug**: After `bd import` from PG→Dolt, `config` table retains stale `issue_prefix` (`bd`), causing new issues to get wrong prefix instead of `be-`.

| Criterion | Verified by | Result |
|-----------|-------------|--------|
| `bd import` syncs `issue_prefix` from `config.yaml` when DB value differs | `cmd/bd/import.go:265–277`: reads `config.GetString("issue-prefix")`, compares to DB, calls `SetConfig` + `CommitWithConfig` when mismatch | ✅ |
| Uses `CommitWithConfig`, not `Commit` (bypasses GH#2455 config-table skip) | Code: `_ = store.CommitWithConfig(ctx, "bd import: sync issue_prefix from config.yaml")` | ✅ |
| No-op when already in sync | Guarded by `dbPrefix != yamlPrefix` check | ✅ |
| No-op when `config.yaml` has empty `issue-prefix` | Guarded by `yamlPrefix != ""` check | ✅ |
| Integration test covers stale-prefix scenario | `TestEmbeddedImport/prefix_sync` in `cmd/bd/import_embedded_test.go` | ✅ |
| `go test ./...` passes | CI run 26114120267 all PASS | ✅ |

## Commits

| SHA | Description |
|-----|-------------|
| `5e50248f7` | fix(import): sync issue_prefix from config.yaml after import (be-llaf) |
| `d7be13164` | test(import): add TestEmbeddedImport_PrefixSync for be-llaf prefix sync |
