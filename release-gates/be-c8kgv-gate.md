# Release gate — be-c8kgv (Fix: TestEnvVarOverrides/BEADS_DOLT_PORT fallback inherits an ambient BEADS_DOLT_SERVER_PORT)

**Date:** 2026-09-02
**Deployer:** beads/deployer
**Bead (deploy):** be-c8kgv — Deploy Review: TestEnvVarOverrides/BEADS_DOLT_PORT_fallback inherits an ambient BEADS_DOLT_SERVER_PORT
**Source bead:** be-k5jdp (build), reviewed by be-3fspd — in_progress, verdict: pass
**Source commit:** `797d4a9e0ba08fc8a42788d91eabe804b2cf693e` (provenance branch `builder/be-k5jdp`, review bead be-3fspd)
**Branch:** `deploy/be-c8kgv-gate` (isolated, cut fresh at the reviewed SHA — never the shared `builder/be-k5jdp` branch)
**Base:** `origin/main` @ `c0d8da42d` ("fix(storage/uow): retry transient ping failures during openDB bootstrap (#6003)")
**Merge-base:** `c0d8da42d` — identical to origin/main tip; clean fast-forward, 0 commits behind
**Merge-tree simulation:** `git merge-tree --write-tree origin/main 797d4a9e0` → tree `4e23cb502`, exit 0, **zero conflicts**

## Verdict: PASS — all 7 criteria clear

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | be-3fspd notes: `verdict: pass`. Style clean (`gofmt -l`: clean; `go vet`: clean). Security clean — full 9-point OWASP walk against the 1-file/+1/-0 diff, all N/A (test-scoped `t.Setenv` env mutation, no production code touched, no secrets, auto-restored by the Go testing stdlib). |
| 2 | Acceptance criteria met | PASS | Sole acceptance criterion — isolate `TestEnvVarOverrides/BEADS_DOLT_PORT_fallback_when_SERVER_PORT_not_set` from an ambient `BEADS_DOLT_SERVER_PORT` — is exercised, passing, and causally proven (see criterion 3). |
| 3 | Tests pass | PASS | Independently re-run by this deployer on the cut branch (not merely trusted from the review bead): `go test ./internal/configfile/... -v` → package-level **52 PASS / 0 FAIL / 0 SKIP**, exit 0 (0.013s); `TestEnvVarOverrides` itself **9/9 subtests PASS**, including the target subtest. Counts match be-3fspd's independently-reported counts exactly — no drift between review and deploy. Causal verification (performed by the reviewer): reverting the fix in a shell with ambient `BEADS_DOLT_SERVER_PORT=28231` reproduces the exact pre-fix failure (`GetDoltServerPort() = 28231, want 3307`); reapplying the fix restores all 9 subtests to PASS. |
| 3a | Pre-existing-failure attribution | N/A | `tests_green: true` — no failing tests to attribute. |
| 3b | Policy/lint lane | PASS | `gofmt -l internal/configfile/configfile_test.go`: clean. `go vet ./internal/configfile/...`: clean (both re-run independently by this deployer). Full `make ci-pr-lint` not re-run for this deploy gate — the reviewer already documented pre-existing, unrelated `gosec G602` findings in `backend/conformance/{cycle_detector_contract,importer_contract}.go`, untouched by this branch (diff is single-file, elsewhere) and out of scope. |
| 3c | CI-config-diff handling | N/A | No `.github/workflows/**` or other CI-config files in the diff — confirmed via `git diff --stat origin/main...797d4a9e0`: 1 file, `internal/configfile/configfile_test.go` only. (That range is the **source commit**, not this PR's head — the head additionally carries this gate record and the review-response commits. No CI-config file appears at any of those SHAs.) |
| 4 | No HIGH-severity findings open | PASS | be-3fspd `security_findings`: none (blocker/major/minor) after the full OWASP walk. No open findings anywhere in the review or independent deploy evidence. |
| 5 | Final branch is clean | PASS | `git status --short` on `deploy/be-c8kgv-gate` at `797d4a9e0`: no tracked-file changes, clean working tree (verified after `go build ./...` and full test run — no stray artifacts). |
| 6 | Branch diverges cleanly from main | PASS | Merge-base equals `origin/main` tip exactly (`c0d8da42d`, 0 commits behind — the cleanest possible base). `git merge-tree --write-tree origin/main 797d4a9e0` succeeds with a single merged tree (`4e23cb502`), exit 0, no conflict markers. |
| 7 | Single feature theme | PASS | `git diff --stat origin/main...797d4a9e0` (the **source commit**, not the PR head): 1 file, `internal/configfile/configfile_test.go`, +1/-0. One theme throughout: add `t.Setenv("BEADS_DOLT_SERVER_PORT", "")` to neutralize an ambient env leak into the `BEADS_DOLT_PORT` fallback subtest. The theme is unchanged at the PR head — see the review-response addendum below for the current file/line accounting. |

## Forward-flagged (non-blocking, out of scope for this deploy)

be-3fspd opened **be-s0af7** to track a related-but-distinct latent bug: the sibling subtest "invalid port env var falls through to config" is exposed to the same ambient-leak bug class via `BEADS_DOLT_PORT` (not `BEADS_DOLT_SERVER_PORT`), but doesn't fail in this environment only because `BEADS_DOLT_PORT` happens to be unset here. Not fixed by this diff; deferred at gate time.

> **Superseded by review.** The PR reviewer ruled the deferral wrong: be-s0af7 is a bead in our own rig, so nothing in *this* repo tracked it, and it is the same one-line fix in the same function as the bug this PR is titled after. It is now fixed here — along with two further subtests carrying the identical shape. See the addendum below.

## Disposition

- **PR opened, not merged.** `gastownhall/beads` is a contributor-only repo for this rig (no push/merge rights) — the deployer's job ends at the open PR regardless of gate outcome. Per the standing `beads-pr-ours-mergeable-is-terminal` rule, no merge-request is routed to mayor for this repo once the PR is open (and CI-green/mergeable); this is terminal from our side, awaiting upstream maintainer merge — no further contributor action.
- Gate is a clean PASS with no waivers, no substitutes, no open blockers. 0 divergence from origin/main, fully independently re-verified by this deployer rather than only trusted from the review bead. The gate evaluated source commit `797d4a9e0` (1 file, +1/-0); the **PR** is larger than that single commit — it also carries this gate record, and now the review-response commits. Current accounting is in the addendum below.

---

## Addendum — review response (2026-09-06, PR #6221)

`bee-ghosttrack` filed CHANGES_REQUESTED on 2026-09-04. Both findings are addressed here.

**Finding 1 (blocking) — the sibling subtest has the same bug and is still red at this head.** Accepted and verified independently before fixing, rather than complied with on assertion. `GetDoltServerPort` (`internal/configfile/configfile.go:509-524`) reads `BEADS_DOLT_SERVER_PORT`, and on an `Atoi` failure falls through to `BEADS_DOLT_PORT` at `:514`. The subtest `invalid port env var falls through to config` sets the first to `"not-a-number"` and never pins the second, so it reads the ambient value:

```
BEADS_DOLT_PORT=28231 go test ./internal/configfile/... -run TestEnvVarOverrides -count=1
  --- FAIL: TestEnvVarOverrides/invalid_port_env_var_falls_through_to_config
      configfile_test.go:1080: GetDoltServerPort() = 28231, want 3308
```

**Two further subtests carry the identical shape**, not flagged in the review and not previously tracked. `GetDoltDatabase` (`configfile.go:560-568`) ranks `BEADS_DOLT_SERVER_DATABASE` above both the config value and the default, and neither `database default` nor `database config value` pins it:

```
BEADS_DOLT_SERVER_DATABASE=ambient_db go test ./internal/configfile/... -run TestEnvVarOverrides -count=1
  --- FAIL: TestEnvVarOverrides/database_default
      configfile_test.go:1121: GetDoltDatabase() = "ambient_db", want "beads"
  --- FAIL: TestEnvVarOverrides/database_config_value
      configfile_test.go:1128: GetDoltDatabase() = "ambient_db", want mydb
```

All three take the same one-line `t.Setenv(<var>, "")` fix already used for the subtest this PR started from, so they are folded in here rather than deferred to a further round trip — the reviewer's own stated rationale for finding 1.

Green under every ambient condition that was red, and under all three at once:

```
BEADS_DOLT_SERVER_PORT=28231 BEADS_DOLT_PORT=28231 BEADS_DOLT_SERVER_DATABASE=ambient_db \
  go test ./internal/configfile/... -run TestEnvVarOverrides -count=1 -v
  --- PASS: TestEnvVarOverrides   9/9 subtests PASS
```

Full package, clean env: `go test ./internal/configfile/... -count=1` → ok. `gofmt -l internal/configfile/`: clean. `go vet ./internal/configfile/...`: clean.

**Finding 2 (nit) — diff accounting.** Correct; the "1 file, +1/-0" phrasing described the *source commit*, not the PR. Criteria 3c and 7 above are now explicitly scoped to `797d4a9e0`, and the Disposition no longer claims it for the PR. Current PR accounting against `origin/main` @ `c0d8da42d`:

| File | Change |
|---|---|
| `internal/configfile/configfile_test.go` | +4/-0 (1 from the source commit, 3 from this review response) |
| `release-gates/be-c8kgv-gate.md` | gate record + this addendum |

The gate verdict itself is unchanged: the diff remains test-only, touches no production code, and the single theme — neutralize ambient env leakage in `TestEnvVarOverrides` — is the same one, now applied completely instead of partially.
