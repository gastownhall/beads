# PR #752 Chaos Testing Review

**PR**: https://github.com/steveyegge/beads/pull/752
**Author**: jordanhubbard
**Bead**: bd-kx1j
**Status**: Under Review

## Summary

Jordan proposes adding chaos testing and E2E test coverage to beads. The PR:
- Adds 4849 lines, removes 511 lines
- Introduces chaos testing framework (random corruption, disk space exhaustion, NFS-like failures)
- Creates side databases for testing recovery scenarios
- Adds E2E tests tracking documented user scenarios
- Brings code coverage to ~48%

## Key Question from Jordan

> "Is this level of testing something you actually want with the current pace of progress?
> It comes with an implied obligation to update and add to the tests as well as follow
> the CICD feedback in github (very spammy if your tests don't pass!)"

## Files Changed (Major Categories)

### Chaos/Doctor Infrastructure
- `cmd/bd/doctor_repair_chaos_test.go` (378 lines) - Core chaos testing
- `cmd/bd/doctor/fix/database_integrity.go` (116 lines) - DB integrity fixes
- `cmd/bd/doctor/fix/jsonl_integrity.go` (87 lines) - JSONL integrity fixes
- `cmd/bd/doctor/fix/fs.go` (57 lines) - Filesystem fault injection
- `cmd/bd/doctor/fix/sqlite_open.go` (52 lines) - SQLite open handling
- `cmd/bd/doctor/jsonl_integrity.go` (123 lines) - JSONL checks
- `cmd/bd/doctor/git.go` (168 additions) - Git hygiene checks

### Test Coverage Additions
- `internal/storage/memory/memory_more_coverage_test.go` (921 lines) - Memory storage tests
- `cmd/bd/cli_coverage_show_test.go` (426 lines) - CLI show command tests
- `cmd/bd/daemon_autostart_unit_test.go` (331 lines) - Daemon autostart tests
- `internal/rpc/client_gate_shutdown_test.go` (107 lines) - RPC client tests
- Various other test files

### Bug Fixes Discovered During Testing
- `internal/storage/sqlite/migrations/021_migrate_edge_fields.go` - Major migration fix
- `internal/storage/sqlite/migrations/022_drop_edge_columns.go` - Column cleanup
- `internal/storage/sqlite/migrations_template_pinned_regression_test.go` - Regression test

## Tradeoffs

### Costs
1. **Maintenance burden**: Must keep coverage above 48% (or whatever threshold is set)
2. **CI noise**: Failed tests = spam until fixed
3. **Velocity tax**: Every change needs test updates
4. **Complexity**: Chaos testing framework itself needs maintenance

### Benefits
1. **Robustness validation**: Proves beads can recover from corruption
2. **Bug discovery**: Already found migration bugs (021, 022)
3. **Confidence**: If chaos tests pass, beads is more robust than feared
4. **Documentation**: E2E tests document expected user scenarios
5. **Regression prevention**: Future changes caught before release

## Initial Assessment

**Implementation Quality: HIGH**

The chaos testing code is well-structured. Key observations:

### What the Chaos Tests Actually Cover

From `doctor_repair_chaos_test.go`:

1. **Complete DB corruption** - Writes "not a database" garbage, verifies recovery from JSONL
2. **Truncated DB without JSONL** - Tests graceful failure when no recovery source exists
3. **Sidecar file backup** - Ensures -wal, -shm, -journal files are preserved during repair
4. **Repair with running daemon** - Tests recovery while daemon holds locks
5. **JSONL integrity** - Malformed lines, re-export from DB

Each test:
- Uses isolated temp directories
- Builds a fresh `bd` binary for testing
- Uses "side databases" (separate from real data)
- Has proper cleanup

### Bug Fixes Already Discovered

The PR includes fixes for bugs found during testing:
- Migration 021/022: `pinned` and `is_template` columns were being clobbered
- Regression test added to prevent recurrence

### Test Coverage Structure

Tests are organized by build tags:
- `//go:build chaos` - Chaos/corruption tests (run separately)
- `//go:build e2e` - End-to-end CLI tests
- Regular unit tests - No build tag required

This means chaos tests only run when explicitly requested, not on every `go test`.

---

## Deep Analysis (Ultrathink)

### The Core Question

Is the testing worth the ongoing maintenance cost?

### Argument FOR Merging

1. **Beads is more robust than feared**. If Jordan got these tests passing, it means:
   - `bd doctor` actually recovers from corruption
   - JSONL/DB sync is working correctly
   - Migration edge cases are handled

   This validates the core design: SQLite + JSONL + git backstop.

2. **Bugs already found**. The migration 021/022 bugs are exactly the kind of subtle
   issues that would cause data loss in production. Finding them now is worth something.

3. **Build tag isolation**. Chaos tests won't slow down regular development:
   ```bash
   go test ./...                    # Normal tests only
   go test -tags=chaos ./...        # Include chaos tests
   go test -tags=e2e ./...          # Include E2E tests
   ```

4. **48% coverage is a floor, not a target**. The PR doesn't enforce maintaining 48%.
   Jordan is asking: "Is this level worth it?" We can always add more later, or let
   coverage drift if priorities change.

5. **Documentation value**. E2E tests document expected user scenarios. When an AI agent
   asks "what should happen when X?", the tests provide executable answers.

### Argument AGAINST Merging

1. **Velocity tax is real**. Every behavior change needs test updates. This is especially
   painful during rapid iteration phases.

2. **CI noise**. Failed tests block merges. With multiple agents working, flaky tests
   become coordination bottlenecks.

3. **Framework maintenance**. The chaos testing framework itself (side databases, build
   tags, test helpers) becomes another thing to maintain.

4. **False confidence**. Tests passing doesn't mean beads is production-ready. It means
   tested scenarios work. Edge cases not covered still fail silently.

### The Real Question: What Phase Are We In?

**If beads is still in "rapid prototype" phase**: The testing overhead is premature.
Focus on features, fix crashes as they happen, lean on git backstop.

**If beads is approaching "reliable tool" phase**: Testing is essential. Multi-agent
workflows amplify bugs. Corruption during a 10-agent batch is expensive.

**Current reality**: Beads is being dogfooded seriously. Multiple agents, real work,
real data loss when things break. We're closer to "reliable tool" than "prototype."

### ROI Calculation

**Cost of NOT testing**: When corruption happens:
- Agent loses context (30-60 min recovery)
- Human has to debug (variable, often 15-60 min)
- Trust erosion (hard to quantify)

**Cost of testing**:
- Review this PR (1-2 hours, one time)
- Update tests when behavior changes (5-15 min per change)
- Fix flaky tests when they appear (variable)

If corruption happens once a month, testing ROI is marginal.
If corruption happens weekly (or with each new feature), testing pays for itself.

---

## Recommendation

**MERGE WITH MODIFICATIONS**

### Why Merge

1. The implementation quality is high
2. Bugs already found justify the effort
3. Build tag isolation minimizes velocity impact
4. Beads is past the prototype phase

### Suggested Modifications

1. **No hard coverage threshold in CI**. Let coverage drift naturally. The value is in
   the chaos tests catching corruption, not in hitting a percentage.

2. **Chaos tests optional in CI**. Run chaos tests on release branches, not every PR.
   This reduces CI noise during active development.

3. **Clear ownership**. Jordan should document how to add new chaos scenarios. Future
   contributors need to know when to add vs skip tests.

### Decision Framework for User

If you answer YES to 2+ of these, merge:
- [ ] Are you dogfooding beads for real work?
- [ ] Has corruption caused you to lose time in the last month?
- [ ] Do you expect multiple agents using beads concurrently?
- [ ] Is beads approaching a "v1.0" milestone?

If you answer NO to all, defer the PR until beads stabilizes.
