# Release Gate: be-3b4e — `bd show` text view labels `created_by` as "Owner:"

- **Deploy bead:** be-3b4e
- **Review bead:** be-r0za (closed, verdict: pass)
- **Builder branch (provenance only):** builder/be-ss66
- **Repo:** gastownhall/beads
- **Deploy commit:** `0929476954e3bec6516d12d1e8e792bd9375209a`
- **Deploy branch:** `deploy/be-3b4e-gate` (cut from the commit above)
- **Base:** origin/main @ `7505e173f2659ba6e1f955b86d81a4f9e21810ca`
- **Evaluated:** 2026-08-15 by beads/deployer

## Scope

`bd show`'s text view labeled the `created_by` field as "Owner:", which is
misleading — `--json` exposes a distinct `owner` field with a different
value. The fix relabels the text-view line to "Created by:" so it no longer
implies the two fields are the same thing.

Diff, confirmed via `git diff --name-only origin/main...HEAD` (2 files):

- `cmd/bd/show_format.go` — relabels `Owner: %s` → `Created by: %s` in
  `formatIssueMetadata`; updates the adjacent comment.
- `cmd/bd/show_format_metadata_test.go` — adds
  `TestFormatIssueMetadata_CreatedByLabel`, asserting "Created by: alice" is
  present, "Assignee: carol" is present, and "Owner: " is absent, given a
  fixture with distinct `CreatedBy`/`Owner`/`Assignee` values — directly
  proving the two fields are not conflated.

Repo-wide grep for other `"Owner:"` call sites found exactly one other hit,
`cmd/bd/github.go:313`, which formats an unrelated GitHub-repo-config field
— correctly out of scope for this fix.

## Gate criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 6 | Clean divergence from `origin/main` (checked first) | **PASS** | `git log --oneline origin/main..HEAD` shows exactly the 2 expected commits, 0 behind. `assert_deploy_ancestry_scope origin/main HEAD be-3b4e be-ss66` → rc=0 (widened to include be-ss66, the builder bead both commits cite — matches be-3b4e's own documented provenance, `branch: builder/be-ss66`). |
| 1 | Review PASS present | **PASS** | be-r0za status=closed, verdict: pass; `deploy_bead: be-3b4e` / `deploy_commit: 0929476954e3bec6516d12d1e8e792bd9375209a` match exactly. |
| 2 | Acceptance criteria met | **PASS** | Diff matches the bead's title/description exactly: a single mislabeled string, fixed, with a regression test directly guarding the described bug. `uncovered_criteria: none` (independently re-confirmed via the repo-wide `"Owner:"` grep above). |
| 3 | Tests pass | **PASS** | See "Tests run" below — diff-owned test passes; all FAILs independently confirmed pre-existing/environmental, none touch the diff's 2 files. |
| 3b | Policy/lint lane (`make ci-pr-policy`) | **PASS** | See "Policy lane" below. |
| 4 | No unresolved HIGH findings | **PASS** | be-r0za's OWASP-style walkthrough (9 categories) is clean; style findings (gofmt/go vet/golangci-lint) clean. Diff is a 2-line string-literal relabel in a formatting function — trivially low risk, consistent with that assessment. |
| 5 | Clean branch | **PASS** | `git status --porcelain=v1` on the deploy branch is empty aside from 3 pre-existing untracked files belonging to *other* beads' sessions (`release-gates/be-hi97-no-workspace-tests-gate.md`, `release-gates/be-uoat-dolt-diff-export-gate.md`, `scripts/rebase-resolve-lib.sh`) — not staged, not part of this branch's history, deliberately left untouched (see "Branch-integrity incident" below). |
| 7 | Single feature theme | **PASS** | 2 files, 1 production fix + 1 direct regression test, one bug, one label. |

## Branch-integrity incident: unrelated commit found and removed

Before writing this record, `deploy/be-3b4e-gate`'s tip (`28c259392`) did not
match the expected deploy commit. Investigation (`git reflog show
deploy/be-3b4e-gate`) showed the branch had originally been cut correctly at
`0929476954e3bec6516d12d1e8e792bd9375209a`, then had a second commit appended
on top:

```
28c259392 witness: salvage uncommitted work (be-3b4e)
092947695 fix(cmd/bd): label created_by as 'Created by:' instead of 'Owner:'
```

This "witness" commit was not created by any action taken while evaluating
this gate. Its content (`git show --stat`) was exactly the three untracked
files noted above — a `git add -A`-style sweep, almost certainly an
automated session/worktree safety net reacting to a session boundary in this
shared worktree, with no knowledge of which bead each file belonged to.
Left in place, it would have carried two other sessions' internal gate
records (including one with a **FAIL** verdict and detailed internal
investigation notes) into a public upstream PR.

Confirmed no other ref pointed at `28c259392` (`git log --all --oneline`),
then corrected with a plain `git reset` (mixed — index only, no `--hard`, no
working-tree content touched) back to the reviewed SHA:

```
git reset 0929476954e3bec6516d12d1e8e792bd9375209a
```

This is lossless: all three files remain intact on disk as untracked
worktree state, exactly as they were before the witness commit fired, for
their owning sessions to commit under their own beads. `deploy/be-3b4e-gate`
now has exactly the two reviewed commits over `origin/main`, re-confirmed
above under criterion 6.

## Tests run

Independently re-verified rather than trusted from be-r0za's report, with
the diff-owned test resolved **by name**, not inferred from counts.

**Diff-owned test** — `TestFormatIssueMetadata_CreatedByLabel`: **PASS**,
confirmed via targeted verbose run and via absence from every FAIL list
below. be-r0za independently confirms the same result via its own targeted
verbose run.

**Full suite** (`scripts/ci/pr-core.sh`-equivalent, `-race -short -skip
'^TestEmbedded' -p 4 -parallel 4`, default 10m per-package timeout):
88 ok / 5 FAIL packages — `cmd/bd` (timed out: `panic: test timed out after
10m0s`, 600.560s), `internal/beads`, `internal/config`, `internal/formula`,
`internal/metrics`. 0 SKIP. This is one package worse than be-r0za's own
baseline (4 FAIL: `cmd/bd`, `internal/beads`, `internal/config`,
`internal/formula`) — both discrepancies (the `cmd/bd` timeout and the extra
`internal/metrics` FAIL) were investigated to a specific, confirmed root
cause rather than waived:

- **System load at time of run**: `uptime` showed load average 42–52 on
  this shared multi-agent host (multiple concurrent `dolt sql-server`
  processes, concurrent `go test`/`go build` for unrelated packages, other
  active agent sessions) — the immediate suspect for both anomalies.
- **`cmd/bd` timeout**: re-ran in isolation (`./cmd/bd/...` alone, `-timeout
  30m`, no other change). Completed cleanly, **no timeout, no panic** —
  directly confirms transient resource contention, not a diff-caused hang.
  This isolated run surfaced 72 individual `--- FAIL:` tests; every one
  cross-checked by name against be-r0za's documented pre-existing failure
  categories (workspace/worktree, config, migration, backup, hooks-install,
  doctor-health, init/bootstrap, recipe-loading) — full match, zero overlap
  with `show_format.go`, including the same representative test
  (`TestWhereNoWorkspace`) be-r0za used for its own baseline proof.
- **`internal/metrics`**: the full-suite log's apparent failing test names
  for this package (e.g. `TestMigrateToServer_*`, `TestReset_Worktree*`)
  turned out to be my own extraction artifact — `go test -p 4`'s
  concurrent, interleaved output made naive line-based attribution
  unreliable. Re-ran `internal/metrics` alone in isolation: clean `ok`, 0
  failures. Not a real failure, diff-unrelated regardless.
- **`cmd/bd/doctor` flip-flop**: passed in the full-suite run above
  (`ok  5.489s`) but failed in the isolated `cmd/bd` retest
  (`FAIL  5.452s`), the inverse pattern from `internal/metrics`. Resolved
  by direct origin/main baseline reproduction (see below) rather than
  assumption — the pass/fail flip between two runs of *identical* code is
  itself evidence of environment-sensitivity, not a deterministic
  diff-caused regression.

**Origin/main baseline reproduction** (own reproduction, mirroring
be-r0za's methodology — `git worktree add --detach <scratch> origin/main`,
detached HEAD at `7505e173f`, zero diff applied, same `TMPDIR`/`GOTMPDIR`
workaround):

- `TestWhereNoWorkspace` (representative test, matching be-r0za's own
  baseline-reproduction proof): **FAILS identically** on origin/main with
  zero diff.
- `cmd/bd/doctor` full package: **FAILS identically** on origin/main with
  zero diff — `FAIL  4.829s`, with the same 6 test names as the isolated
  retest above (`TestRunDoltHealthChecks_DoltBackendNoServer`,
  `TestCheckFederationRemotesAPI_ServerNotRunning`,
  `TestCheckFreshClone_ServerModeUnreachable`,
  `TestCheckRepoFingerprint_UsesTargetRepoOutsideCWD`,
  `TestCheckBeadsRole_NotConfigured`, `TestCheckBeadsRole_NotGitRepo`). Test
  names (NoServer / ServerModeUnreachable / NotConfigured) indicate
  sensitivity to ambient Dolt-server/config state, consistent with the
  contention diagnosis above. This directly proves the flip-flop is
  pre-existing environmental flakiness, not caused by this diff.

Scratch worktree removed after use (`git worktree remove`).

**Conclusion**: diff-owned test passes; every FAIL package/test is either
independently reproduced against a zero-diff origin/main baseline, or
directly attributable to shared-host contention/output-interleaving with a
confirmed root cause, and none touches either of the diff's 2 files.

## Policy lane (`make ci-pr-policy`)

Initial run failed on "check version consistency":
`.githooks/commit-msg: no 'BEGIN/END BEADS INTEGRATION' marker found`.
Root-caused, not waived: `.githooks/commit-msg` is an untracked gc-rig
session shim ("rewritten on every session start"; confirmed absent from
`origin/main` via `git ls-tree` / `git cat-file -e`), unrelated to the
beads project. `scripts/check-versions.sh` globs `.githooks/*` on the raw
filesystem rather than `git ls-tree`, so it picks up the shim.

Remediated reversibly: moved the shim to scratch, re-ran the check (clean,
rc=0), restored the shim immediately after, confirmed restoration
(executable, correct size). `git status --porcelain=v1` showed no stray
changes before or after. Full `make ci-pr-policy` (check-build-tags,
check-go-install-guidance, check-versions, doc-flags/doc-freshness,
check-testing-short, workapi/frontend boundary check, no-beads-jsonl-changes,
`make api-check`): clean.

## Findings from review (no action required)

From be-r0za: no HIGH or MEDIUM findings across security (9 OWASP-style
categories, all clear) or style (gofmt/go vet/golangci-lint clean).

## Result: PASS

All 7 criteria plus the 3b policy/lint lane pass, independently
re-verified rather than trusted from be-r0za's report. Two genuine
discrepancies between my results and the reviewer's baseline
(`internal/metrics`, `cmd/bd/doctor`) were investigated to specific,
confirmed root causes rather than asserted away. One branch-integrity
incident (an unrelated automated commit) was found and losslessly
corrected before push.

Merge authority: gastownhall/beads is a contributor-only repo (we do not
maintain it — `origin` remote push is disabled,
`DISABLED-upstream-is-fetch-only-push-to-fork-and-PR`). Deployer's job ends
at the opened PR; no `release-gate/deploy-clearance` status posted, no
merge-request routed to mayor. Upstream maintainers own the merge.

## Post-gate history

The criteria walk above examined
`0929476954e3bec6516d12d1e8e792bd9375209a`. Commits landed on
`deploy/be-3b4e-gate` after that SHA, in order:

| Commit | What |
|---|---|
| `f0e8b8f7d` | This gate document. |
| `5f5767c71` | `docs(changelog): record the created_by label change under [Unreleased]` — the one blocking ask from the PR #5800 review. |
| `fd6286ec0` | Merge `origin/main`. The new CHANGELOG entry collided with two entries main had prepended to the same `[Unreleased]` / `### Changed` list; resolved by keeping all three. Without it the PR was no longer cleanly mergeable. |
| this commit | This annotation. |

None of them touches production code, so the criteria walk still describes
the shipped diff (`cmd/bd/show_format.go` plus
`cmd/bd/show_format_metadata_test.go`) exactly, and the **PASS** stands for
the SHA it examined. This section exists so the "exactly the two reviewed
commits over `origin/main`" line in the branch-integrity section above is
not read as a claim about the current head.
