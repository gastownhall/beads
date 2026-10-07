# Release gate — shard test-server-storage-full so prebuilt-binary runs land in the right package dir (be-gepv)

- **Builder bead:** be-gepv — fix `.github/scripts/server-storage-test-shard.sh`
  so the prebuilt-binary branch `cd`s into `internal/storage/dolt` before
  exec, matching the go-test fallback branch's working directory.
- **Deploy bead:** be-23f3
- **Review bead:** be-6yo1 — verdict **PASS**, recorded on commit
  `15c6d0a7d08b8fa03e4f2a827adf23f81e55de4d`
- **Commits:** `2cb3d8f2845b023741d65571827267a0a5323ce4` (tdd_red) then
  `15c6d0a7d08b8fa03e4f2a827adf23f81e55de4d` (tdd_green), 2 commits over the
  prior deploy tip `9caf412234fa71dc1564e8f25df6fc3be52d9e4b`
- **Branch:** `builder/be-gepv` (provenance only, not a push target)
- **Evaluated:** 2026-08-18 by beads/deployer

## Scope deviation: extends PR #5836 instead of a fresh isolated branch

be-gepv's own bead text is explicit that this fix targets an already-open
PR: *"This lands on PR #5836, branch `deploy/be-pp7e-gate`. ... So this fix
belongs ON that PR, not on main."* This matches established precedent —
be-extn's prior fix (sharding `test-server-storage-full`'s 1126-test job to
stop timing out) was already pushed directly onto the same
`deploy/be-pp7e-gate` branch rather than spawning a new isolated
branch+PR. be-23f3's boilerplate description text ("cut a fresh ISOLATED
deploy branch... open the PR") is the generic single-bead template; it is
superseded here by the bead's own first-party provenance note and the
directly-applicable precedent.

Accordingly, this gate evaluates be-gepv's own incremental diff against the
existing `deploy/be-pp7e-gate` tip, not a full rebuild of PR #5836's
7-commit history (already separately gated: be-pp7e/be-aiy5/be-extn covered
the prior commits). Ancestry scope formally validated via
`assert_deploy_ancestry_scope`:

```
assert_deploy_ancestry_scope 9caf412234fa71dc1564e8f25df6fc3be52d9e4b \
  15c6d0a7d08b8fa03e4f2a827adf23f81e55de4d be-gepv be-6yo1        # rc=0
assert_deploy_ancestry_scope origin/main \
  15c6d0a7d08b8fa03e4f2a827adf23f81e55de4d \
  be-gepv be-yy25 be-pp7e be-aiy5 be-extn be-6yo1 be-w90n          # rc=0
assert_safe_push_target "deploy/be-pp7e-gate"                      # rc=0
```

## Scope

be-gepv's own increment, confirmed via
`git diff --stat 9caf412234f..15c6d0a7d` (2 files, 86 insertions, 0
deletions, no `.claude/**` paths):

- `.github/scripts/server-storage-test-shard.sh` — 4-line fix: adds a
  comment and `cd internal/storage/dolt` immediately before the
  `exec "$STORAGE_BINARY"` call, on the prebuilt-binary branch only.
- `scripts/ci_workflow_test.go` — new test
  `TestServerStorageShardScriptRunsPrebuiltBinaryFromPackageDir` (82
  lines) asserting the fix's exact line-ordering invariants: discovery-grep
  before `cd`, `cd` between the prebuilt-binary guard and its exec, `cd`
  absent from the go-test fallback branch.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-6yo1 records verdict PASS on commit `15c6d0a7d08b8fa03e4f2a827adf23f81e55de4d` — exact match to this deploy's commit. |
| 2 | Acceptance criteria met | **PASS** | be-6yo1's `spec_findings.tests_green: true`, confirmed via `TEST_COVER=1 ./scripts/test.sh`; be-gepv's Done-when checklist (4 items) independently re-confirmed below. |
| 3 | Tests pass | **PASS** | Diff-owned package re-run by deployer, real PASS (see below), matching reviewer's own record. |
| 3a | Pre-existing-failure attribution | **N/A — attributed** | `make ci-pr-lint` repo-wide fails solely on 3x gosec G602 in `backend/conformance/{cycle_detector_contract.go:494,importer_contract.go:381,relations_contract.go:672}` — tracked by already-open **be-vf95**, confirmed byte-identical to `origin/main` (`git log origin/main..HEAD -- backend/conformance/` empty — this PR lineage never touches that directory). `make ci-pr-policy` fails solely on the `.githooks/commit-msg` version-marker check — tracked by already-open **be-jy56**, likewise confirmed untouched by this lineage (`git log origin/main..HEAD -- .githooks/commit-msg` empty). |
| 3b | Policy/lint lane | **PASS** | Diff-scoped `golangci-lint run ./scripts/...` reports **0 issues**. `gofmt` clean (via `make ci-pr-lint`'s own Go-scoped check — "All Go files are properly formatted"; a stray manual `gofmt -l` invocation against the shell script itself is a self-inflicted false alarm, gofmt cannot parse non-Go source). `go vet ./scripts/...` clean. `bash -n` on the shell script clean. |
| 4 | No unresolved HIGH findings | **PASS** | be-6yo1: `style_findings: none`, `security_findings: none` — full OWASP Top-10 walk, minimal diff surface, no injection/auth/data-exposure concerns. |
| 5 | Clean working tree | **PASS** | `git status --porcelain` on the evaluated commit shows no staged/unstaged changes — only the pre-existing, unrelated untracked scratch files already present in this worktree (`release-gates/be-hi97-*.md`, `release-gates/be-k9js-*.md`, `release-gates/be-uoat-*.md`, `scripts/rebase-resolve-lib.sh`), never staged. |
| 6 | Clean divergence from `origin/main` | **PASS** | `git merge-base --is-ancestor origin/main HEAD` succeeds — clean fast-forward relationship. `assert_deploy_ancestry_scope` over the full 7-commit lineage returns rc=0 against the accepted bead-id set (see scope-deviation section above). |
| 7 | Single feature theme | **PASS** | Both files serve the one fix: the shell-script correction plus the one test file whose new case pins the corrected behavior. No unrelated changes riding along. |

## Tests run on release branch (independent re-verification)

Diff-owned package, run via the canonical test runner:

```
./scripts/test.sh ./scripts/...
```

Result: package `github.com/steveyegge/beads/scripts` — **PASS** in 2.439s,
no skips. Includes `TestServerStorageShardScriptRunsPrebuiltBinaryFromPackageDir`
passing for real (not skipped), confirming the line-ordering invariants the
fix establishes.

Static checks, independently re-run:

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./scripts/...` | clean |
| `bash -n .github/scripts/server-storage-test-shard.sh` | clean |
| `golangci-lint run ./scripts/...` (diff-scoped) | clean, 0 issues |
| `make ci-pr-lint` (repo-wide) | FAIL — solely be-vf95 (pre-existing; see criterion 3a) |
| `make ci-pr-policy` (repo-wide) | FAIL — solely be-jy56 (pre-existing; see criterion 3a) |

## Findings from reviews (no action required)

From be-6yo1: no style or security findings. gofmt/`bash -n`/go vet all
clean; minimal diff surface (4-line shell fix + 1 new test); no
injection/auth/data-exposure/etc. concerns identified across the full
OWASP Top-10 walk.

## Verdict

**PASS** — all 7 criteria (plus 3a/3b) clear. This fix extends the already
gated-and-open `deploy/be-pp7e-gate` branch / PR #5836 rather than cutting a
new isolated branch (see scope-deviation section). Per this repo's
contributor-only status, the job ends at the updated PR — no merge-request
will be routed to mayor and no deploy-clearance status will be posted;
merge authority belongs to the upstream maintainers.
