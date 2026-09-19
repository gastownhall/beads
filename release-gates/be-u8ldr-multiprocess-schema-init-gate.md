# Release gate — be-u8ldr (multiprocess dolt schema-init retry + test isolation fix)

**Date:** 2026-08-20
**Deployer:** beads/deployer
**Bead (deploy):** be-u8ldr — needs-deploy: Fix: multiprocess tests INSERT without issues.description an (from:be-4oah9)
**Source bead (build):** be-kafb — tdd_green fa825dcbd6a9cf389c42e5ab3b5a5b2f58aafcf9
**Review bead (round 1):** be-yzowm — closed, request-changes → superseded by round 2/3 rework
**Review bead (round 2/3):** be-4oah9 — closed, close_reason "pass", `verdict: PASS`, `review_round: 3`
**Source commit:** `e2a7bbb90bcd1077bd785236a4915c899ed059d8` — "fix(dolt): remove dead decoy-listener test code, use retry-safe schema init under 10-way contention" (refs be-4oah9, be-yzowm, be-kafb)
**Provenance branch:** `builder/be-kafb` — provenance only, not a push target (shared builder branch)
**Deploy branch (isolated, cut from source commit above):** `deploy/be-u8ldr-gate`
**Base:** `origin/main` @ `d38ac728b581c8595fae36344ecca68830c7f3b5` ("fix(httpapi): make beads_dir and repo_root optional in ContextResponse (#5757)")
**Merge-base:** `ed382cbdb89cf7ba42b020e4927575dbf27e102e`
**Merge-tree simulation:** `git merge-tree --write-tree origin/main e2a7bbb90b` → tree `90320eb53df70b48ab8014c0ae231e673d0991ba`, exit 0, **zero conflict markers**
**Ancestry scope check:** `assert_deploy_ancestry_scope origin/main e2a7bbb90b be-u8ldr be-4oah9 be-yzowm be-kafb` → exit 0 (both commits between merge-base and source commit cite an accepted bead id; no `.claude/**` paths touched)

## Pre-flight: already merged/PR'd?

`DEPLOY_MODE=remote` (origin and fork both resolve to github.com URLs). No PR URL recorded in be-u8ldr. Checked `gh api repos/gastownhall/beads/commits/e2a7bbb90bcd1077bd785236a4915c899ed059d8/pulls` → `[]` (no PR exists for this SHA). Checked `git merge-base --is-ancestor e2a7bbb90b origin/main` → not an ancestor (not already merged). Clean to proceed with the normal flow.

## Verdict: PASS

## Criteria walk

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main e2a7bbb90b` succeeds, single merged tree, no conflicts. Source commit is 28 commits behind current `origin/main`, but none of those 28 touch `internal/storage/dolt/initschema_multiprocess_test.go` (`git log --oneline SHA..origin/main -- <path>` → empty). No self-rebase needed. |
| 1 | Review PASS present | PASS | be-4oah9 closed by beads/reviewer, close_reason "pass". Round-3 notes: `style_findings` all clean (gofmt/build/vet×2), `security_findings` — diff is a pure subset of round-1's already-walked 9-point OWASP review, zero new attack surface, no go.mod/go.sum change. `verdict: PASS`, `review_round: 3`. |
| 2 | Acceptance criteria met | PASS | Original three-part bug (title: "INSERT without issues.description and report OK on zero writes; dolt init has no identity"): (a) `issues.description/design/acceptance_criteria/notes` are `TEXT NOT NULL` with no default — INSERT now includes them (verified against `internal/storage/schema/migrations/0001_create_issues.up.sql` in be-yzowm round 1); (b) write-count mismatch now `os.Exit(1)`s instead of silently reporting OK (confirmed independently by round-2 reviewer); (c) `dolt init --name/--email` fix verified against baseline failure "empty ident name not allowed" (be-yzowm round 1). All three fixed and untouched by this round's diff — round 3 closes the sole remaining gap, the diff-owned test's own susceptibility to ambient dolt-server port/lock state. |
| 3 | Tests pass (documented CI-equivalent command, real counts) | PASS | **Independently re-run by this deployer** (not just trusting reviewer's report) at the exact source commit, disposable scratch worktree, `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true go test -race -tags=integration,gms_pure_go -timeout=200s -count=1 -v -run '<pattern>' ./internal/storage/dolt/...`. `TestMultiProcessSchemaInit_DoltVerify`: PASS×2 (clean env 8.95s; ambient `BEADS_DOLT_SERVER_PORT=28231` set, replicating round-1/round-2's adversarial condition, 8.33s). Adjacent regression suite (`TestMultiProcessSchemaInit`, `TestHelperMultiStore`, `TestMultiStoreConcurrent_{InProcess,Subprocess,CloseIsolation}`): PASS×5. **Totals: 7 PASS / 0 FAIL / 0 SKIP** across both runs. `gofmt -l`, `go build ./...`, `go vet -tags=integration,gms_pure_go ./internal/storage/dolt/...`: all clean, exit 0. |
| 3a | Pre-existing-failure attribution | N/A | Not invoked — zero failures observed, nothing to attribute. |
| 3b | Policy/lint lane | PASS | `make ci-pr-policy` at source commit: exit 0. Doc-freshness, testing.Short() boundaries, workapi frontend boundary, `.beads/issues.jsonl` diff check, and openapi spec gate (incl. `go test ./internal/httpapi/...`) all reported PASS/succeeded; zero FAIL/ERROR lines in the full log. |
| 4 | No HIGH-severity findings open | PASS | Both round-2 BLOCKING findings (dead decoy-listener test code; migration-lock-timeout FAIL) were root-caused and fixed in round 3, independently re-verified structurally correct by the round-3 reviewer (not just trusted from builder's claim). `waiver_ref: none needed`. `bd dep tree be-u8ldr` / `bd dep tree be-4oah9`: no open child/dependent beads. |
| 5 | Feature branch / working tree clean | PASS | `git status --short` at the deployer worktree checked out to the source commit: empty (clean). |
| 7 | Single feature theme | PASS | Full PR diff `git diff --stat origin/main...e2a7bbb90b`: 2 files (`internal/storage/dolt/initschema_multiprocess_test.go`, `internal/storage/dolt/multistore_multiprocess_test.go`), 26 insertions(+), 5 deletions(-), across 4 commits (fa825dcbd tdd-green, dfdfa0440/d7233eb38 round-2 red/green rework, e2a7bbb90 round-3 fix). One coherent theme throughout: the bug title's own three symptoms (INSERT missing NOT NULL columns, zero-writes-reported-OK, dolt-init-missing-identity) plus the round-2/3 hardening of the diff-owned test's own reliability under ambient dolt-server state. No scope creep, no unrelated files. |

## Hand-off

- `DEPLOY_MODE=remote`. Push target: **`headfork`** (`https://github.com/quad341/beads-sec003-contrib.git`) — **not** `origin` (`gastownhall/beads`; push disabled, fetch-only) and **not** `builder/be-kafb` (provenance only, a shared builder branch other beads may still commit to). `fork` (`quad341/beads.git`) is stale post-rename; `headfork`/`prhead` are the current remotes for this contributor rig.
- PR: cross-repo `quad341-sec003-contrib:deploy/be-u8ldr-gate` → `gastownhall:main`. Run `gc beads-contributor pre-pr-check` before opening and clear every `[BLOCK]`.
- **gastownhall/beads is upstream-only for this rig** (contributor relationship, not maintainer). Per role instructions, job ends at opening the PR: do not merge, approve, or dismiss reviews there; never run `maintainer-pr-review`/mpr against it. Record the gate result + PR URL, close the bead, stand down.
