# Release gate — be-a57h7 (Review: Fail loudly when testcontainers Ryuk is disabled — silent config turns every killed test run into a permanent container leak (from:be-gr470))

**Date:** 2026-09-13
**Deployer:** beads/deployer
**Bead (deploy):** be-a57h7
**Source bead:** be-gr470 — review verdict PASS; build bead be-ovg86; source branch `builder/be-ovg86`
**Source/deploy commit:** `53fe94f16a11676139d25c260bb47009c06f9883` (base `f56632adcfabed7da6ed0aabe4e760066b472c46` = origin/main, confirmed via `git merge-base`, no drift since review)
**Branch:** `deploy/be-a57h7-gate` (cut from `53fe94f16a11676139d25c260bb47009c06f9883`)
**Push target:** `headfork` (`quad341/beads-sec003-contrib`); origin push disabled by design (contributor fork setup)
**PR:** (recorded below once opened)

## Verdict: 7/7 PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | `git merge-base origin/main 53fe94f16a1...` = `f56632adcfabed7da6ed0aabe4e760066b472c46`, exactly matching be-gr470's recorded `base_commit`. Re-fetched origin/main fresh at gate time — still `f56632adc...`, no drift, no self-rebase needed. Pre-flight: `gh api repos/gastownhall/beads/commits/53fe94f16a1.../pulls` = `[]` (no existing or already-merged PR carries this SHA); `git merge-base --is-ancestor 53fe94f16a1... origin/main` fails (not yet merged). |
| 1 | Review PASS present | PASS | be-gr470 verdict: `pass`. `uncovered_criteria: none`. |
| 2 | Acceptance criteria met | PASS | All 3 Done-when items independently confirmed by be-gr470 via named subtests in the diff's own test file (`internal/testutil/ryuk_test.go`): unmissable signal naming resolved $HOME when Ryuk disabled (`TestCheckRyukDisabled_DisabledFailsLoudly`), silent when enabled (`TestCheckRyukDisabled_EnabledIsSilent`), config-probe-only / no docker daemon needed (`TestResolvedHome_ReadsHomeEnv`, `TestCheckRyukDisabled_AllowUnreapedOptOut`) — all 4 PASS, independently re-run by the reviewer at the green SHA. |
| 3 | Tests pass (full-scope) | PASS (attributed) | Independent deployer full-suite re-run at the exact deploy SHA — see "Criterion 3" below. |
| 4 | No open HIGH review findings | PASS | be-gr470 `style_findings`: none (gofmt/go vet clean on all 4 changed files). `security_findings`: none (all 9 categories walked explicitly — injection, auth, sensitive-data, XXE/SSRF, access control, misconfig, XSS, vulnerable deps, logging). One `spec_gap_finding` recorded but explicitly non-blocking (a doc-update gap across 9 unrelated `release-gates/*.md` docs that mention the older `TESTCONTAINERS_RYUK_DISABLED` workaround); already has its own tracked follow-up, be-dw8hx. |
| 5 | Clean working tree | PASS | `builder/be-ovg86` = `53fe94f16a11676139d25c260bb47009c06f9883` on `fork`/`headfork`/`prhead` (all three remotes agree). `git status --porcelain` clean at that SHA in the gate-test worktree. |
| 7 | Single feature theme | PASS | 4 files, +125/-0, all under `internal/testutil/`, single theme (fail loudly via a `checkRyukEnabled()` guard run at harness init): `container_provider.go` (+3, wires the guard in), `ryuk.go` (+56, new — the guard + `resolvedHome()` helper), `ryuk_test.go` (+59, new — covers it), `testdoltserver.go` (+7, wires the guard in). No unrelated changes. |

## Criterion 3 — full-suite test evidence

`test_cmd: BEADS_TEST_SHARED_SERVER=1 DOCKER_HOST="unix:///run/user/$(id -u)/podman/podman.sock" TESTCONTAINERS_RYUK_DISABLED=true BEADS_ALLOW_UNREAPED_TESTCONTAINERS=1 ./scripts/test.sh` (= `make test`), full `./...` scope, run independently by the deployer at the exact deploy SHA `53fe94f16a11676139d25c260bb47009c06f9883`. be-gr470's own run was scoped `affected-package`, not full-suite; per `test-evidence-integrity.md.tmpl` a prior role's test run does not carry over to satisfy this criterion — this is the deployer's own independent run.

- `test_cmd_scope: full-suite`
- 97 packages total: 94 `ok`, 3 `FAIL`. `EXIT_CODE=1` (honest, not swallowed — matches the 3 real failures below). 0 diff-owned SKIPs.
- Full log: `/tmp/claude-1000/-home-jaword-projects-gc-management--gc-worktrees-beads-deployer/b8387e28-c742-4d65-a606-d76b96a8682e/scratchpad/be-a57h7-full-test.log` (703 lines).
- Diff-owned tests (the 4 in `internal/testutil/ryuk_test.go`): package `internal/testutil` is `ok` in this run — consistent with be-gr470's independent re-run of the same 4 tests.
- The 3 failing packages, each attributed pre-existing per `non-diff-owned-gate-failure.md.tmpl`'s 4-clause test (all four required clauses satisfied; clause 3 proof = BASE-REF REPRODUCTION, the "(d)" alternative, dispositive since the condition reproduces at base):

```
failure_attribution: TestConfigValidateReadOnlyIsHermetic -> be-apqua
  clause 1 (not diff-owned): PASS — cmd/bd, no overlap with the diff's internal/testutil files
  clause 2 (tracked bead): PASS — no existing gate-tracker covered this condition (checked be-9ogs6 + 5 others via `bd list --label gate-tracker`; be-9ogs6's mechanism is ambient ~/.beads walk-up, a different root condition). Fresh gate-tracker be-apqua created: labels [gate-tracker, test-hermeticity], dep discovered-from:be-a57h7.
  clause 3 (not caused by diff), proof (d) BASE-REF REPRODUCTION: byte-identical failure reproduced at BASE_REF f56632adc via ./scripts/test.sh with matching env (BEADS_TEST_SHARED_SERVER=1 leaks an ambient BEADS_DOLT_SERVER_PORT even through the isolation wrapper) — dispositive, proven pre-existing.
  clause 4 (path overlap): PASS unconditionally — zero overlap (cmd/bd vs internal/testutil).

failure_attribution: TestEnvVarOverrides/invalid_port_env_var_falls_through_to_config -> be-s0af7
  clause 1 (not diff-owned): PASS — internal/configfile, no overlap with the diff's internal/testutil files
  clause 2 (tracked bead): PASS — pre-existing tracker be-s0af7 cited; sighting comment recorded on it this gate.
  clause 3 (not caused by diff), proof (d) BASE-REF REPRODUCTION: same subtest failed identically at HEAD (53fe94f16a1) and reproduced identically at BASE_REF (f56632adc) via ./scripts/test.sh with matching env — dispositive.
  clause 4 (path overlap): PASS unconditionally — zero overlap.

failure_attribution: TestResolveServerMode_HostInferredExternal, TestResolveServerMode_EnvHostBeatsEmbeddedMetadata -> be-wbyau
  clause 1 (not diff-owned): PASS — internal/doltserver, no overlap with the diff's internal/testutil files
  clause 2 (tracked bead): PASS — pre-existing tracker be-wbyau cited; sighting comment recorded on it this gate.
  clause 3 (not caused by diff), proof (d) BASE-REF REPRODUCTION: both tests failed identically at HEAD and reproduced identically at BASE_REF via ./scripts/test.sh, ~0.009s both times — confirms BEADS_TEST_SHARED_SERVER=1 itself sets the ambient BEADS_DOLT_SERVER_PORT this bead's mechanism describes. Dispositive.
  clause 4 (path overlap): PASS unconditionally — zero overlap.
```

`skip_justification`: no diff-owned SKIPs; none observed in this run.

`criterion_3b` (policy/lint lane): N/A — no policy/lint-lane-specific files touched by this diff.
`criterion_3c` (CI-config diff): N/A — diff touches no CI configuration (`.github/workflows/**`, `Makefile`, `scripts/ci/**`); confirmed via the diffstat (4 files, all under `internal/testutil/`).

## Merge authority

`gastownhall/beads` is contributor-only for this rig — no rig agent has merge access. Per established precedent (be-gd3v, be-79jh, be-39ss, be-pp7e, be-r3ysh, be-krza3, be-vc1m, be-7q688, be-6iglh/be-0l89e, be-c8kgv, be-1wwre, be-3vzut, be-fkmhv, be-caep7, be-guyc7, be-8sfdy, be-u9scu), the deployer's job on PASS ends at the open, verified PR. No merge-request is routed to mayor.

## Disposition

**7/7 PASS.** Cutting isolated branch `deploy/be-a57h7-gate` from `53fe94f16a11676139d25c260bb47009c06f9883`, pushing to `headfork`, and opening the PR against `gastownhall/beads` main. No `PR-DESCRIPTION` block or `gc.pr_ping` metadata present on be-a57h7 — standard PR body, no maintainer-notes section, no ping required.
