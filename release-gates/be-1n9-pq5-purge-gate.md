# Release gate: be-1n9 — be-pq5 PURGE dropped databases (bench leak fix)

**Verdict: PASS.**

Branch: `release/be-1n9-pq5-purge` (stacks on `release/be-nx7-d4v2-indexes`, PR #3662)
Base on origin: `origin/main` @ `0fa5f210`
HEAD: `b1d830bb`

## Stacking

The two be-1n9 commits introduce uses of bench-helpers (`uniqueBenchDBName`, `dropBenchDB`) that are themselves added by be-nx7's commit `e36e6430`. This branch therefore stacks: be-nx7 first, then be-1n9 on top. The PR will list 5 commits ahead of `origin/main` until be-nx7's PR #3662 lands; once it lands, GitHub will automatically rebase this PR's diff to only the 2 be-1n9 commits.

## Commits (be-1n9 portion only)

| # | SHA on `release/be-1n9-pq5-purge` | Source on `quad341/beads:rebase/be-nx7-be-1n9-stack` | Subject |
|---|-----------------------------------|----------------------------------------------------|---------|
| 1 | `f8738dda` | `4e7b2066` | fix(dolt): be-pq5 PURGE dropped databases after DROP |
| 2 | `b1d830bb` | `4fb759f7` | fix(dolt): be-pq5 PURGE dropped databases (production code, follow-up) |

Both cherry-picks clean atop the be-nx7 stack tip (`fb88fa40`).

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Review PASS present | PASS | Reviewer-1 PASS verdict on be-pq5 PURGE content carries through both rebases (per builder note 2026-05-02T17:13Z and 2026-05-02T17:35Z); gemini second-pass disabled per current policy. |
| 2 | Acceptance criteria met | PASS | `dropBenchDB` now `DROP DATABASE … ; PURGE …`; `staleDatabasePrefixes` includes `benchdb_`; `bd dolt clean-databases` issues post-loop PURGE with 60s timeout; new `TestBenchDBPurgeDoesNotLeak` regression test gates the leak. All present in cherry-picked commits. |
| 3 | Tests pass | PASS | Ran `TestBenchDBPurgeDoesNotLeak` against a host scratch dolt sql-server (`dolt 1.86.6`, port 33999, dedicated data-dir under `~/.gotmp`): PASS in 8.0s; the scratch data-dir's `.dolt_dropped_databases/` dir was never created (would have grown to 5 entries without PURGE per reviewer-1's reproduction). Schema package PASS. `go test -tags gms_pure_go -short ./internal/storage/dolt/` shows only the same pre-existing rig-env-leakage failures as on `origin/main`. |
| 4 | No high-severity review findings open | PASS | None outstanding on bead. |
| 5 | Final branch is clean | PASS | `git status` clean (untracked `.gc/`, `.gitkeep` are rig artifacts outside the tree). |
| 6 | Branch diverges cleanly from main | PASS | Cherry-picks atop be-nx7 stack are clean; once be-nx7 lands, this branch will fast-forward to a 2-commit diff against main with no conflicts. |

## Spec deviation (carried from reviewer-1)

The original spec called for `SELECT COUNT(*) FROM dolt_dropped_databases` SQL check; that table/view does not exist in Dolt 1.86.5/1.86.6 (verified empirically). Test counts entries in `.dolt_dropped_databases/` directory via `BEADS_TEST_EXTERNAL_DOLT_DATA_DIR` env var instead. Self-skips when env var unset. Reviewer-1 accepted this deviation; this gate confirmed it under the same Dolt 1.86.6 server in the rig.

## Push target

`PUSH_REMOTE=fork` (origin = `gastownhall/beads` is upstream-not-pushable for this rig user; fork = `quad341/beads` is the cross-repo PR head).
