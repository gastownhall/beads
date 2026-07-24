# Release gate: be-1he — eliminate 12s `dolt remote -v` slow path

**Verdict: PASS.**

**[maintainer edit 2026-07-23: corrected layer description to match shipped diff — see PR comment. This gate document (and the PR body) previously described a "Layer 1" `repo_state.json` sentinel in `internal/storage/dolt/federation.go`; the actual diff never touched that file. The doc below now reflects only what ships: Layer 2 (`internal/storage/doltutil/remotes.go`) and Layer 3 (`cmd/bd/version_tracking.go`).]**

Branch: `release/be-1he-slow-path-fix`
Base on origin: `origin/main` @ `7e3c7fbbe`
HEAD: `9b2d0b87a`
Source bead: `be-srb` (review of `be-1he`); investigator escalation `gm-cgjm3a`.

## Commit

| # | SHA on `release/be-1he-slow-path-fix` | Source on `quad341/beads:rebase/be-nx7-be-1n9-stack` | Subject |
|---|---------------------------------------|-------------------------------------------------------|---------|
| 1 | `9b2d0b87a` | `777dd60d2` | perf(dolt): be-1he eliminate 12s `dolt remote -v` against multi-db server roots |

Cherry-pick is clean: 2 files, +22/-1 lines (`cmd/bd/version_tracking.go`, `internal/storage/doltutil/remotes.go`). Diff content byte-equivalent to the reviewer-audited source SHA; only commit-graph offsets differ because the original sat on a 6-commit stack.

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | PASS | Reviewer-1 (`beads/reviewer-1`, gm-vjcmu7) PASS verdict on `be-srb` at 2026-05-04T20:38Z. Layer-by-layer audit, OWASP walk, build/vet/lint clean, no request-changes findings. Gemini second-pass disabled per current policy. |
| 2 | Acceptance criteria met | PASS (2 of 3 layers) | Layer 2 2-second `context.WithTimeout` + named const `listCLIRemotesTimeout` at `internal/storage/doltutil/remotes.go:14-17,47-49`; Layer 3 read-only `bd_version` probe before writeable open at `cmd/bd/version_tracking.go:195-205`. **Layer 1 (`repo_state.json` sentinel in `internal/storage/dolt/federation.go`) is not part of this diff** — `federation.go` has no hunk on this branch; the earlier "3 layers" framing was stale from an upstream draft of the stacked branch. Layer 2 and Layer 3 stand on their own merits regardless: `ListCLIRemotes` still shells out to `dolt remote -v` with no timeout on `origin/main` (called from `cmd/bd/doctor/federation.go` and CLI push/pull/fetch remote routing), so Layer 2 still closes a real ~12s-hang risk; Layer 3 still avoids an unnecessary writeable store open when no migration is needed. Out-of-scope items (multi-binary `.local_version` hygiene, upstream dolt fix, `syncCLIRemotesToSQL` semantics, gascity-side fanout) untouched. |
| 3 | Tests pass | PASS | `go test -tags gms_pure_go -count 1 -short` on the cherry-picked branch vs the same command on `origin/main`: failure sets are identical. **No new regressions introduced.** Detail: `internal/storage/doltutil/...` clean (5.0s). `internal/storage/dolt/migrations/...` clean (0.06s). `internal/storage/dolt/...` 5 pre-existing `TestApplyConfigDefaults_*` failures (env leak: $GC_DOLT_PORT=28231 propagates through bash → Go test sees BEADS_DOLT_PORT default), reproduced byte-equal on `origin/main`. `cmd/bd/...` 17 pre-existing failures, reproduced byte-equal on `origin/main`. `cmd/bd/doctor/fix/...` 1 pre-existing `TestFixMissingMetadata_DoltRepair` failure, reproduced on `origin/main`. Version-tracking-targeted run (`-run 'TestAutoMigrate\|TestVersion\|TestCheckVersion'`) PASS in 0.107s. |
| 4 | No high-severity review findings open | PASS | Zero blocking findings. One `info` finding only: bead description doc-drift on line numbers (text says remotes.go:46-48 / const 14-19; actual is 47-49 / 14-17). Annotation only — no behavior change requested. |
| 5 | Final branch is clean | PASS | `git status` clean (untracked `.gc/`, `.gitkeep` are rig artifacts outside the tree). |
| 6 | Branch diverges cleanly from main | PASS | `git log origin/main..HEAD` is exactly 1 commit ahead. Cherry-pick of `777dd60d2` onto `7e3c7fbbe` was clean (no conflicts, no excludes needed). |

## Test environment

- Host: Linux 6.19.14-300.fc44.x86_64, Go from `make build` toolchain.
- `BEADS_DOLT_AUTO_START=0`; `GC_DOLT_PORT=28231` in env (rig's gc dolt server) — drives the 5 `TestApplyConfigDefaults_*` failures on both this branch and `origin/main`.
- TMPDIR/GOTMPDIR pinned to `~/.gotmp` (per /tmp tmpfs 12.5G per-user quota).
- `go vet -tags gms_pure_go ./internal/storage/dolt/... ./internal/storage/doltutil/... ./cmd/bd/...`: clean.
- `go build -tags gms_pure_go`: clean (`make build` succeeded).

## Push target

`PUSH_REMOTE=fork`. `git push --dry-run origin HEAD` returns `403 Permission to gastownhall/beads.git denied to quad341`. Cross-repo PR head: `quad341:release/be-1he-slow-path-fix`.

## Reviewer-deferred follow-ups (already filed)

- `be-bwd` — needs-tests bead routed to `beads/validator`, written and closed: unit tests for the new decision branches (Layer 2 timeout, Layer 3 RO probe — the branch's own history also includes tests for the since-dropped Layer 1, which do not apply here). Branch `tests/be-bwd-3layer-fix` @ `747caf121` carries the test commit; intentionally not folded into this PR per the deployer's "single bead, single commit" discipline. The validator's branch can ship as a separate follow-up PR once this lands, trimmed to match this PR's actual (2-layer) scope.

## Out of scope

- Stale `/home/jaword/go/bin/bd` v1.0.0 vs `~/.local/bin/bd` v1.0.3 multi-binary install — environment hygiene, not a code fix. The 3-layer fix makes the system resilient to stale `.local_version` regardless.
- Upstream dolt CLI bug (12s failure path on non-repo dirs) — lives in the dolt repo. Layer 2's timeout backstops it from this side; no routing-around fix (the earlier-described Layer 1 sentinel) is part of this PR.
- gascity-side `gc mail inbox` 8x bd fanout deduplication — separate handoff to `gascity/builder`, same family of bug as gascity PR #1546.
