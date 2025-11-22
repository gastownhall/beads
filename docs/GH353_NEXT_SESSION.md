# Next Session Prompt: Implement GH #353 Fixes

## Context
We've investigated GH #353 (daemon locking issues in Codex sandbox). Full analysis in `docs/GH353_INVESTIGATION.md`.

**TL;DR:** Users in sandboxed environments (Codex) get stuck with "Database out of sync" errors because:
1. Running daemon has cached metadata
2. `bd import` updates database but daemon never sees it
3. Sandbox can't signal/kill the daemon
4. User is stuck in infinite loop

## Task: Implement Phase 1 Solutions

Implement three quick fixes that give users escape hatches:

### 1. Add `--force` flag to `bd import`
**File:** `cmd/bd/import.go`

**What to do:**
- Add `--force` flag to importCmd.Flags() (around line 692)
- When `--force` is true, ALWAYS update metadata (lines 310-346) even if `created == 0 && updated == 0`
- Print message: "Metadata updated (database already in sync with JSONL)"
- Ensure `TouchDatabaseFile()` is called to update mtime

**Why:** Allows users to manually force metadata sync when stuck

### 2. Add `--allow-stale` global flag
**File:** `cmd/bd/main.go`

**What to do:**
- Add global var: `allowStale bool`
- Add to rootCmd.PersistentFlags(): `--allow-stale` (around line 111)
- Description: "Allow operations on potentially stale data (skip staleness check)"

**File:** `cmd/bd/staleness.go`

**What to do:**
- At top of `ensureDatabaseFresh()` function (line 20), add:
  ```go
  if allowStale {
      fmt.Fprintf(os.Stderr, "⚠️  Staleness check skipped (--allow-stale), data may be out of sync\n")
      return nil
  }
  ```

**Why:** Emergency escape hatch when staleness check blocks operations

### 3. Improve error message in staleness.go
**File:** `cmd/bd/staleness.go`

**What to do:**
- Update the error message (lines 41-50) to add sandbox guidance:
  ```go
  return fmt.Errorf(
      "Database out of sync with JSONL. Run 'bd import' first.\n\n"+
      "The JSONL file has been updated (e.g., after 'git pull') but the database\n"+
      "hasn't been imported yet. This would cause you to see stale/incomplete data.\n\n"+
      "To fix:\n"+
      "  bd import -i .beads/beads.jsonl  # Import JSONL updates to database\n\n"+
      "If in a sandboxed environment (e.g., Codex) where daemon can't be stopped:\n"+
      "  bd --sandbox ready               # Use direct mode (no daemon)\n"+
      "  bd import --force                # Force metadata update\n"+
      "  bd ready --allow-stale           # Skip staleness check (use with caution)\n\n"+
      "Or use daemon mode (auto-imports on every operation):\n"+
      "  bd daemon start\n"+
      "  bd <command>     # Will auto-import before executing",
  )
  ```

**Why:** Guides users to the right solution based on their environment

## Testing Checklist

After implementation:

- [ ] `bd import --force -i .beads/beads.jsonl` updates metadata even with 0 changes
- [ ] `bd import --force` without `-i` flag shows appropriate error (needs input file)
- [ ] `bd ready --allow-stale` bypasses staleness check and shows warning
- [ ] Error message displays correctly and includes sandbox guidance
- [ ] `--sandbox` mode still works as before
- [ ] Flags appear in `bd --help` and `bd import --help`

## Quick Start Commands

```bash
# 1. Review the investigation
cat docs/GH353_INVESTIGATION.md

# 2. Check current import.go implementation
grep -A 5 "func init()" cmd/bd/import.go

# 3. Check current staleness.go
head -60 cmd/bd/staleness.go

# 4. Run existing tests to establish baseline
go test ./cmd/bd/... -run TestImport
go test ./cmd/bd/... -run TestStaleness

# 5. Implement changes (see sections above)

# 6. Test manually
bd import --help | grep force
bd --help | grep allow-stale
```

## Expected Outcome

Users stuck in Codex sandbox can:
1. Run `bd import --force -i .beads/beads.jsonl` to fix metadata
2. Run `bd --sandbox ready` to use direct mode
3. Run `bd ready --allow-stale` as last resort
4. See helpful error message explaining their options

## References

- **Investigation:** `docs/GH353_INVESTIGATION.md`
- **Issue:** https://github.com/steveyegge/beads/issues/353
- **Key files:**
  - `cmd/bd/import.go` (import command)
  - `cmd/bd/staleness.go` (staleness check)
  - `cmd/bd/main.go` (global flags)

## Estimated Time
~1-2 hours for implementation + testing

---

**Ready to implement?** Start with adding the flags, then update the error message, then test thoroughly.
