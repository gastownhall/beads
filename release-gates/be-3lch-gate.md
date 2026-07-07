# Release gate — be-3lch (initConfigForTest auto-opt-out of repo config)

- **Bead:** be-veon (review of build be-3lch / architect be-lspm)
- **Commit shipped:** `30c1ad1c7` (cherry-pick of `6858f98fc` from `fix/be-0d4-postgres-init-guards`)
- **Branch:** `release/be-3lch` off `origin/main`
- **Evaluated:** 2026-05-12 by beads/deployer

## Scope note

The source commit `6858f98fc` is a **+5 LOC test-only** change to
`cmd/bd/test_helpers_pure_test.go`. The helper now calls
`t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")` as its first statement,
so every test that uses `initConfigForTest(t)` gets a clean viper by
default (no silent inheritance of the dogfooded `<repo>/.beads/config.yaml`).
A 3-line doc comment update explains the opt-out.

**Relationship to be-sm6v:** be-sm6v (release/be-sm6v, open PR) adds the
explicit per-test `t.Setenv` + `t.Chdir(t.TempDir())` defense in
`where_cgo_test.go` to preserve bisect signal for the original be-hple
failure. be-3lch generalizes that opt-out into the shared helper so all
seven `initConfigForTest` callers get it for free. The two PRs are
designed to land in order (be-sm6v first); regardless of merge order,
neither change touches production code.

**Architect's deviation handled:** design called for a lockstep edit in
two helper files. be-5t00 (commit `b589f9b63`) already removed the
cgo-tagged duplicate `test_helpers_test.go::initConfigForTest`; the
helper now lives only in `test_helpers_pure_test.go` and a single-file
edit satisfies the design intent.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | beads/reviewer recorded `Verdict: PASS` in be-veon notes (gm-81uq8l, 2026-05-12 08:21). Reviewer verified all 4 architect acceptance criteria, all 7 caller paths, and confirmed no production code touched. Pre-existing flake (`TestResolveWhereBeadsDir_ReturnsEmptyWithoutWorkspace`) reproduces on parent commit, doesn't touch `initConfigForTest`. |
| 2 | Acceptance criteria met | **PASS** | All 4 architect criteria satisfied: (1) `./scripts/test.sh ./cmd/bd/` zero new regressions vs origin/main baseline (16 pre-existing failures, identical on both); (2) `t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")` placed as first statement after `t.Helper()`, before `config.ResetForTesting()` at `cmd/bd/test_helpers_pure_test.go:91`; (3) doc comment above helper updated with the 3-line opt-out hint (lines 85-87); (4) audit gate `TestWhereCommand_UsesConfigPrefixFromSelectedDB` PASSES in isolation. |
| 3 | Tests pass | **PASS** | Full `./scripts/test.sh ./cmd/bd/` on `release/be-3lch`: 16 failures, **identical set** to origin/main baseline (verified by running same command on `da73b7511`). Zero new regressions introduced. See "Tests run" below. |
| 4 | No high-severity review findings open | **PASS** | 0 HIGH findings. Reviewer notes "Findings: none blocking. Releasing to deployer." |
| 5 | Final branch is clean | **PASS** | `git status` on `release/be-3lch` shows nothing modified; only worktree-scaffolding untracked paths (`.gc/`, `.gitkeep`) that are never staged. |
| 6 | Branch diverges cleanly from main | **PASS** | Branch cut via `git checkout -B release/be-3lch da73b7511` (origin/main HEAD at evaluation time); `git cherry-pick 6858f98fc` applied with zero conflicts; `git log origin/main..HEAD` = 1 commit (plus this gate commit). |

## Tests run on release branch

| Test | Result | Notes |
|------|--------|-------|
| `TMPDIR=~/.gotmp GOTMPDIR=~/.gotmp ./scripts/test.sh ./cmd/bd/` (release/be-3lch) | 16 fails | Same failures as origin/main baseline. Failing tests do NOT call `initConfigForTest` — they are pre-existing state-leakage tests touching `$HOME/.beads` and worktree-hooks plumbing. |
| `TMPDIR=~/.gotmp GOTMPDIR=~/.gotmp ./scripts/test.sh ./cmd/bd/` (origin/main baseline `da73b7511`) | 16 fails | Identical failure set, verifying no regression from the be-3lch change. |
| `go test -tags gms_pure_go -run TestWhereCommand_UsesConfigPrefixFromSelectedDB -v ./cmd/bd/` (audit gate, architect criterion #4) | PASS 0.00s | Audit gate confirmed against the new helper. |

## Environment caveats (informational, not gate-blocking)

When run **in isolation** from a gc-rig worktree (where the moduleRoot is
NOT the beads source tree), `TestWhereCommand_ReadsPrefixFromEmbeddedStore`
still reads `issue-prefix: be` via viper's worktree-fallback probe —
which `BEADS_TEST_IGNORE_REPO_CONFIG=1` does not suppress because it only
ignores `<moduleRoot>/.beads/config.yaml`, not the cousin beads dir found
via `git rev-parse --git-common-dir`. This is exactly the trap be-sm6v's
`t.Chdir(t.TempDir())` was designed to close (see be-sm6v gate). In a
clean CI checkout (moduleRoot == beads tree), the helper-level fix from
be-3lch is sufficient. The failing-in-isolation test is also not visible
in the full suite because an earlier test (`TestInit_WithBEADS_DIR_DoltBackend`)
terminates the test binary before the cgo tests run — this is a
pre-existing condition, identical on the baseline.

## Verdict

**PASS** — push to `fork` (origin is locked for quad341; `fork =
quad341/beads`), open PR against `gastownhall/beads:main`.
