# Release gate — Fix federation cross-tenant isolation violation + panic

- **Builder bead:** be-3c78s — fix `TestFederationDatabaseIsolation` (cross-
  tenant isolation violation) and `TestFederationVersionControlAPIs`
  (unrecovered panic) in `internal/storage/dolt/federation_test.go`.
- **Deploy bead:** be-42tlj
- **Review bead:** be-0vbbs — verdict **PASS**, recorded on commit
  `78e32879084ff412923cef5f6bb49f13ee69488c`
- **Commits:** `7d82a9e46c35b2a9f2ef761ad06644e6719bc28b` (tdd_red, = the
  prior deploy tip, "chore: release gate PASS for be-23f3") then
  `78e32879084ff412923cef5f6bb49f13ee69488c` (tdd_green) — exactly 1 commit
  over the prior deploy tip, confirmed via `git rev-parse HEAD~1`.
- **Branch:** `deploy/be-pp7e-gate` (reused; lands on existing open PR
  #5836, pushed via `prhead` — origin is fetch-only for this rig)
- **Evaluated:** 2026-08-19 by beads/deployer

## Scope deviation: extends PR #5836 instead of a fresh isolated branch

be-3c78s's own bead text ("Landing" section) is explicit: PR #5836 (branch
`deploy/be-pp7e-gate`) is the still-open home for this test suite; push
directly onto that branch per this rig's established deploy-branch-reuse
precedent (be-pp7e → be-23f3 → be-gepv → be-3c78s) rather than opening a new
PR. This matches the identical override already applied and documented in
this same branch's prior gate (`release-gates/be-23f3-gate.md`).

Accordingly, this gate evaluates be-3c78s's own incremental diff against the
existing `deploy/be-pp7e-gate` tip, not a full rebuild of PR #5836's entire
history (already separately gated by the prior commits' own gate records).

## Scope

be-3c78s's own increment, confirmed via `git diff --stat
7d82a9e46c..78e328790` — 1 file, +42/-23, no `.claude/**` paths:

- `internal/storage/dolt/federation_test.go` — `setupFederationStore` gave
  every federated "town" the same literal `Database: "beads"`; since `New()`
  always connects in server mode against the one shared `TestMain`-started
  Dolt server, `Path` alone never isolated the two towns (only `Database`
  does) — root cause of the cross-tenant isolation violation. Also fixed the
  tests' own assumption that `GetIssue` returns `(nil, nil)` on a miss (it
  always returns a wrapped `storage.ErrNotFound`), and
  `TestFederationVersionControlAPIs` hardcoding `Checkout(ctx, "main")`
  instead of capturing its actual `setupTestStore`-assigned starting branch
  — that mismatch, compounded by a discarded `GetIssue` error, was the
  panic's root cause. Zero production code touched.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | **PASS** | be-0vbbs closed, reason "pass", recorded on commit `78e32879084ff412923cef5f6bb49f13ee69488c` — exact match to this deploy's commit. |
| 2 | Acceptance criteria met | **PASS** | be-3c78s's 3 acceptance criteria (isolation test passes, panic test passes with no hidden post-panic failures, security verdict stated) all independently re-confirmed below — not accepted on the builder's or reviewer's word alone. |
| 3 | Tests pass | **PASS** | Both diff-owned tests independently re-verified PASS by direct CI job-log inspection (see below). Other shard failures in the same run are pre-existing and unrelated (3a). |
| 3a | Pre-existing-failure attribution | **N/A — attributed** | 10/16 "Server Dolt Full Suite" shards fail in run `32312105789`, none touching `federation_test.go`. Root catalog: **be-7ch8l** (P1, closed — decomposed into 7 tracked children after finding 14/16 shards failing *before any fix landed*, i.e. proven pre-existing at base ref). Failing-test names pulled directly from shard logs and matched to open siblings: `TestGitRemotePushSkipsUserPrePushHook`/`TestCreateIssueAfterPull` → **be-hsa9t** (open, git-remote-harness cluster); `TestCLIBundleMatchesRuntimeCommittedSchema` → **be-vmzni**/**be-y7ddy** (open, schema-migration cluster); `TestRigIssueIsPersistentButHiddenFromReady` → **be-11ck7** (open, rig issue-type cluster); `TestReclaimRevertsExpiredOnly` → the specific flake be-0vbbs's own review notes already named (lease_test.go, unrelated). All 4 attribution clauses satisfied: not diff-owned (no path overlap with `federation_test.go`), tracked bead-id per cluster, proven pre-existing at base ref (be-7ch8l's original catalog), no path overlap. |
| 3b | Policy/lint lane | **N/A — attributed** | `make ci-pr-policy` fails solely at "check version consistency": `.githooks/commit-msg` missing BEADS INTEGRATION markers. Root cause already tracked and closed as a false positive (**be-jygq**, **be-2q84**, **be-jy56**): the file is a git-untracked, `.git/info/exclude`d local gc-rig session shim (`worktree-setup.sh`), not a real repo file — independently re-confirmed here via `git ls-files` (empty) and `git check-ignore -v` (matches `.git/info/exclude:40`). Reversible repro: moved the shim aside (sha256 backed up), re-ran `make ci-pr-policy` → **exit 0, full PASS**, then restored the shim byte-identical (checksum-verified). Real upstream CI checks out a clean tree without this shim and is unaffected. |
| 4 | No unresolved HIGH findings | **PASS** | be-0vbbs: `style_findings: none`; `security_findings`: full OWASP Top-10 walk plus independent re-derivation of the builder's "not a production issue" claim (4 independent reasons, citing `issueops/get_issue.go`, `federation.go`, and 3 named runtime firewalls in `store.go`). `bd` search across bead notes for be-0vbbs/be-3c78s/be-42tlj surfaces no separate finding beads. |
| 5 | Clean working tree | **PASS** | `git status --short` on the evaluated commit shows only the pre-existing, unrelated untracked scratch files already present in this worktree (`release-gates/be-hi97-*.md`, `release-gates/be-k9js-*.md`, `release-gates/be-p7dzx-*.md`, `release-gates/be-uoat-*.md`, `scripts/rebase-resolve-lib.sh`), never staged, unaffected by the shim move/restore. |
| 6 | Clean divergence from `origin/main` | **PASS** | HEAD is 9 ahead / 15 behind `origin/main` (stale but not a fast-forward). `gh pr view 5836 --json mergeable,mergeStateStatus` reports `mergeable: MERGEABLE`. `git merge-tree $(git merge-base HEAD origin/main) HEAD origin/main` contains **zero** real conflict markers (`grep -c '^<<<<<<<\|^CONFLICT'` = 0). `mergeStateStatus: UNSTABLE` reflects the already-attributed pre-existing shard/policy failures in 3a/3b, not a textual conflict. |
| 7 | Single feature theme | **PASS** | One file, one cohesive fix: federation-test isolation (unique `Database` per town) and the panic it was masking (stale `Checkout` target + discarded error). No unrelated changes riding along. |

## Tests run on release branch (independent re-verification)

Diff-owned tests, confirmed by direct CI job-log inspection (not the
reviewer's summary) via the GitHub Jobs API against run `32312105789`
("PR Risk" workflow, the exact commit `78e32879084ff412923cef5f6bb49f13ee69488c`):

| Test | Shard | Job conclusion | Result |
|---|---|---|---|
| `TestFederationDatabaseIsolation` | 8/16 | success | `--- PASS: TestFederationDatabaseIsolation (7.58s)` — no isolation violation, no other FAIL/panic in this shard's log |
| `TestFederationVersionControlAPIs` | 5/16 | success | `--- PASS: TestFederationVersionControlAPIs (0.33s)` — no panic, no other FAIL/panic in this shard's log (post-panic tail runs to completion) |

Static checks, independently re-run at `78e32879084ff412923cef5f6bb49f13ee69488c`:

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `make ci-pr-policy` (repo-wide) | FAIL — solely the untracked `.githooks/commit-msg` shim artifact (pre-existing, see 3b); **PASS** when the shim is moved aside |

## Findings from reviews (no action required)

From be-0vbbs: no style or security findings against the diff. One narrow,
explicitly out-of-scope secondary observation noted (not a fix target):
`verifyProjectIdentity`'s GH#4637 safety net fails open when `beadsDir` is
empty — real `bd` CLI flows always populate it via `bd init`, so this is a
backward-compat gap, not an active production risk.

## Verdict

**PASS** — all 7 criteria (plus 3a/3b) clear. This fix extends the already
gated-and-open `deploy/be-pp7e-gate` branch / PR #5836 rather than cutting a
new isolated branch (see scope-deviation section). Per this repo's
contributor-only status, the job ends at the updated PR — no merge-request
will be routed to mayor and no deploy-clearance status will be posted;
merge authority belongs to the upstream maintainers.
