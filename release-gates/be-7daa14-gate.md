# Release Gate: be-7daa14 — feat(init): auto-configure contributor routing on fork detect

**PR**: https://github.com/gastownhall/beads/pull/4028
**Branch**: `fix/be-7daa14-fork-detection-output` (quad341/beads)
**Date**: 2026-05-19
**Deployer**: beads/deployer

**Maintainer update**: 2026-05-23, original PR branch updated with a maintainer
merge from current `upstream/main` plus follow-up fix/test commits. The merge
keeps the current disabled-by-default auto-export behavior from `main`, and the
follow-up fix places `autoConfigureForkContributor` before the initial
`store.Commit` / `store.Close` so routing config is written to an open store and
included in the initial Dolt commit.

## Gate Result: PASS

| # | Criterion | Evidence | Result |
|---|-----------|----------|--------|
| 1 | Review PASS present | be-rev-169eb4 PASS (2026-05-18) | ✅ PASS |
| 2 | Acceptance criteria met | See below | ✅ PASS |
| 3 | Tests pass | CI run 26009117468 all checks PASS; maintainer update adds `BEADS_TEST_EMBEDDED_DOLT=1 go test -tags gms_pure_go ./cmd/bd -run 'TestEmbeddedInit/fork_auto_contributor' -count=1` PASS | ✅ PASS |
| 4 | No high-severity findings | be-rev-169eb4: "None blocking" — N1/N2/N3 are non-blocking observations about output text deviations from the design mockup; design accepted them as semantically correct | ✅ PASS |
| 5 | Final branch is clean | `git status` — clean; only `.gc/` and `.gitkeep` untracked (rig artifacts) | ✅ PASS |
| 6 | Branch diverges cleanly from main | Original PR conflicted with current `main` around the auto-export block; maintainer merge resolved the conflict and preserved current `main` semantics | ✅ PASS |

## Acceptance Criteria Verification (be-7daa14 + be-0ccf34 design spec)

| Scenario | Expected output | Code path | Result |
|----------|-----------------|-----------|--------|
| Happy path (fork, first run) | `▶ Fork detected — configuring contributor routing` + `upstream: <url>` + 3–4 `✓` lines + opt-out hint | `autoConfigureForkContributor`: `isFork=true`, `existing=""`, `!quiet` block at function end | ✅ |
| Opt-out (`--role=maintainer` on fork) | `⚠ Fork detected (upstream: <url>) / Contributor routing skipped / To set up later: bd init --contributor` | `roleFlag == "maintainer"` branch | ✅ |
| Re-init (already configured) | `⚠ Fork detected (upstream: <url>) / already configured → <path> / Skipping auto-setup` | `existing != ""` branch | ✅ |
| CI / `--quiet` | Silent (no fork block) | All output guarded by `!quiet` | ✅ |
| No new lipgloss styles | Only `ui.RenderAccent`, `ui.RenderPass`, `ui.RenderWarn` used | Code review confirmed | ✅ |
| All existing init tests pass | CI run 26009117468 all PASS | CI evidence | ✅ |
| `bd init` wires `autoConfigureForkContributor` | Called in `init.go` before initial `store.Commit` / `store.Close` so store-backed config writes succeed | Maintainer update verified | ✅ |
| Suppress output in non-interactive mode | Second commit `32d3eb41e` adds `nonInteractive` guard | Code review confirmed | ✅ |
| Fork auto-routing regression coverage | Temp git fork with `upstream`, non-interactive quiet init, read back `routing.mode`, `routing.contributor`, `sync.remote`, and `beads.role` | `TestEmbeddedInit/fork_auto_contributor` | ✅ |

## Commits

| SHA | Description |
|-----|-------------|
| `0a5a16eaa` | feat(init): auto-configure contributor routing on fork detect (be-7daa14) |
| `32d3eb41e` | fix(init): suppress autoConfigureForkContributor output in non-interactive mode |
| `7038872a9` | chore: release gate PASS for be-7daa14 |
| `a60e62c7a` | Merge upstream/main into PR 4028 |
| `0772de4cb` | test(init): cover fork auto contributor routing |
| `979e039b4` | fix(init): run fork auto routing before store close |
