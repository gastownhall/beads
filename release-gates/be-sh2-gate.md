# Release gate — be-sh2 (MigrateUp stderr progress + large-rig warning)

**Date:** 2026-04-24
**Deployer:** beads/deployer-1
**Bead:** be-sh2 — Review: MigrateUp stderr progress + large-rig warning (be-8ja)
**PR:** https://github.com/gastownhall/beads/pull/3462
**Commit under test:** `ea5e78f4` (branch `quad341:fix-be-8ja-rebuild`)
**Base:** `origin/main` @ `ecd2f726` (fix packaging and windows install regressions)

## Verdict: PASS

All six criteria pass. Shippable.

## Criteria

### 1. Review PASS present — PASS

Two PASS verdicts present in bead notes against commit `ea5e78f4` / PR #3462:

- `beads/reviewer` — "Re-review @ ea5e78f4 / PR #3462 — 2026-04-24 — PASS"
- `beads/reviewer-1` — "Re-review verdict — PASS (rebuild on origin/main)"

Both reviewers independently verified bracket placement, cherry-pick
cleanness onto current `origin/main`, and all acceptance criteria.
(Single-pass review stands while gemini-reviewer is disabled; see
deployer prompt.)

### 2. Acceptance criteria met — PASS

Cross-checked on the cherry-picked branch:

- ✓ Each migration emits one `Applying migration NNNN: name…` / `  done (N.Ns)` bracket to stderr via `progressOut io.Writer = os.Stderr`.
- ✓ Bracket wraps the `splitStatements` per-statement loop AND the `schema_migrations INSERT IGNORE` — the specific placement the first deployer FAIL called for, now correct at `internal/storage/schema/schema.go:212-234`.
- ✓ `bd <anything> 2>/dev/null` produces identical stdout (no stdout writes in the migration path).
- ✓ `bd list --json | jq` pipelines unchanged — stderr-only.
- ✓ Large-rig warning is one-shot per MigrateUp: emit sits AFTER the `len(pending) == 0` early return, so zero-pending runs skip the count query entirely.
- ✓ Fresh-install silence: count error suppresses the warning (`emitLargeRigNotice` returns early on `err != nil`).
- ✓ No new external dependencies — `io`, `os`, `time` from stdlib only.
- ✓ No TTY-aware output / no spinners / no color — plain `fmt.Fprintf` to stderr (designer §5).

### 3. Tests pass — PASS

**Local (scratch clone, cherry-picked onto origin-real/main @ ecd2f726):**

- `GOTOOLCHAIN=auto go build ./...` — clean
- `GOTOOLCHAIN=auto go vet ./...` — clean
- `TestEmitLargeRigNotice` — PASS all 5 subtests (fresh_install_table_missing, small_rig_below_threshold, at_threshold_no_warning, one_past_threshold_warns, typical_large_rig). Threshold boundary tight: 10_000 silent / 10_001 warns.
- `TestHumanMigrationName` — PASS, all 5 filename patterns including no-underscore edge case.
- `TestProgressOutDefaultsToStderr` — PASS.
- Full `internal/storage/schema` package — PASS (0.003s).
- Full `./...` suite in clean env: packages touched by this change all PASS. Three unrelated packages (`cmd/bd/doctor`, `internal/beads`, `internal/remotecache`) show env-sensitive failures locally; each fails identically on clean `origin/main` baseline (pre-existing, not introduced by ea5e78f4) and all pass on PR CI.

**PR CI (authoritative):**

All completed checks SUCCESS on https://github.com/gastownhall/beads/pull/3462 at commit `ea5e78f4`:

- Lint, formatting, build-tag policy, doc flags freshness, version consistency
- Test (ubuntu-latest), Test (macos-latest), Test (Windows - smoke)
- Test Nix Flake, Build (Embedded Dolt), Test (Embedded Dolt Storage)
- Test (Embedded Dolt Cmd 1/20 … 20/20) — all 20 shards green
- Cross-Version Smoke: v0.62.0, v0.63.3, v1.0.0, v1.0.1, v1.0.2 → candidate — all green

### 4. No high-severity review findings open — PASS

Both reviewer passes list only non-blocking minor observations:
- `TestProgressOutDefaultsToStderr` asserts non-nil rather than identity to `os.Stderr` — declaration + code review is the real line of defense.
- Per-migration format string not unit-tested at the full string level — covered by upgrade-smoke CI integration.
- `progressOut` is package-level mutable — no concurrent-writer race today, but noted for any future `t.Parallel()` writer-swap test.
- On migration failure the `done` line isn't emitted — acceptable progress UX.

Zero HIGH-severity findings open.

### 5. Final branch is clean — PASS

`git status` on `fix-be-8ja-rebuild` at `ea5e78f4` shows no uncommitted changes in tracked files (only untracked `.gc/`, `.gitkeep` which are deployer scratch state outside the repo's tracked tree). Gate commit adds only this file.

### 6. Branch diverges cleanly from main — PASS

Verified in scratch worktree:
- `git cherry-pick ea5e78f4` onto `origin/main @ ecd2f726` — clean apply, 2 files changed, 160 insertions(+). No conflicts.
- GitHub reports `mergeable: UNKNOWN` on first query (pre-merge calculation), but the reviewer's cherry-pick onto `c6d0cc2f` (2 commits behind current main) was clean and current main at `ecd2f726` adds only: (a) packaging/windows install regressions (#3455), (b) remote sync follow-ups, (c) regression workflow gate test fix, (d) dolt install in regression workflow, (e) `bd dolt pull` fix (#3443), (f) doltserver-connection log verbosity changes. None touch `internal/storage/schema/schema.go` — the only file ea5e78f4 modifies beyond adding `progress_test.go`.

## History note — two prior deployer FAIL passes

This bead went through two earlier deployer runs which both FAILed
criterion #6, each against the ORIGINAL commits `38f2f1e4` + `101ea777`
on the builder's old working branch. Those commits bracketed the
progress emit around the pre-`GH#3363` single-Exec call site and genuinely
conflicted with main's per-statement executor. The builder rebuilt as
`ea5e78f4` with the bracket correctly wrapped around the
`splitStatements` loop AND the `schema_migrations INSERT IGNORE`, opened
PR #3462 from `quad341:fix-be-8ja-rebuild`, and the reviewer re-verified
PASS. This gate run targets `ea5e78f4` specifically and is distinct from
the earlier runs — cherry-pick is now clean.

## PR body

The builder already opened PR #3462 with a body covering the rebuild
rationale and test results. No new PR is being opened. This gate
markdown is the deployer's separate artifact; it travels with the same
branch so the reviewer of the merge can audit criteria evidence.
