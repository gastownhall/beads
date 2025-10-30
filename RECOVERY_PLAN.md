# Recovery Plan from Export Deduplication Bug (bd-160)

## Fastest Path to Recovery

### 1. Choose Canonical Repo (30 seconds)

Pick **~/src/beads** as the source of truth because:
- It has bd-160 bug filed (the critical issue we just discovered)
- It has 148 issues vs 145 in fred/beads
- More likely to have the latest work

### 2. Clean Both Repos (2 minutes)

```bash
# In ~/src/beads (canonical)
cd ~/src/beads
sqlite3 .beads/beads.db "DELETE FROM export_hashes"
./bd export -o .beads/beads.jsonl
git add .beads/beads.jsonl
git commit -m "Recovery: full export after clearing stale export_hashes"
git push

# In /Users/stevey/src/fred/beads (secondary)
cd /Users/stevey/src/fred/beads
sqlite3 .beads/beads.db "DELETE FROM export_hashes"
git fetch origin
git reset --hard origin/main  # DANGER: Discards local-only issues
./bd export -o .beads/beads.jsonl  # Should match remote now
```

### 3. Verify Convergence (1 minute)

```bash
# Both repos should now be identical
cd ~/src/beads && ./bd stats && wc -l .beads/beads.jsonl
cd /Users/stevey/src/fred/beads && ./bd stats && wc -l .beads/beads.jsonl

# Verify git is clean
cd ~/src/beads && git status
cd /Users/stevey/src/fred/beads && git status
```

### 4. Test Critical Workflows (2 minutes)

```bash
# In fred/beads (the one we're actively using)
cd /Users/stevey/src/fred/beads

# Test export doesn't skip issues
./bd export -o /tmp/test.jsonl 2>&1 | grep -i skip
# Should see NO "Skipped X issues" messages

# Test basic operations
./bd create "Recovery test" -p 2
./bd ready --limit 5
./bd close bd-XXX --reason "Test"  # Whatever ID was created

# Verify auto-import works
echo "Testing auto-import detection"
./bd export -o .beads/beads.jsonl
git add .beads/beads.jsonl && git commit -m "Test commit" && git push
cd ~/src/beads && git pull
# Should auto-import new issues

# Run the build
go test ./cmd/bd -run TestExport -v
```

## Alternative: Merge Approach (if data loss is a concern)

If you want to preserve any issues that might exist only in fred/beads:

```bash
# Export both repos to separate files
cd ~/src/beads
./bd export -o /tmp/beads-home.jsonl

cd /Users/stevey/src/fred/beads  
./bd export -o /tmp/beads-fred.jsonl

# Compare them
diff /tmp/beads-home.jsonl /tmp/beads-fred.jsonl | head -50

# If there are unique issues in fred, import them to home
cd ~/src/beads
./bd import -i /tmp/beads-fred.jsonl --resolve-collisions

# Then proceed with cleanup as above
```

## What NOT To Do

❌ **Don't trust `bd sync` yet** - The auto-import may still have issues
❌ **Don't work in both repos simultaneously** - Pick one as primary
❌ **Don't assume "it's working" without testing** - Verify exports are complete

## Post-Recovery

1. **Always verify export completeness**:
   ```bash
   # After any export, check:
   ./bd stats  # Note total issues
   wc -l .beads/beads.jsonl  # Should match
   ```

2. **Monitor for recurrence**:
   ```bash
   # Check for export_hashes pollution:
   sqlite3 .beads/beads.db "SELECT COUNT(*) FROM export_hashes"
   # Should be 0 now (feature disabled)
   ```

3. **File follow-up issues**:
   - Test that N-way collision resolution still works without export dedup
   - Verify daemon auto-import works correctly
   - Consider adding `bd validate` check for JSONL/DB sync

## Estimated Total Time: 5-10 minutes

The key is: **pick one repo, nuke the other, verify everything works**.
