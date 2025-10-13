# BD-9: Collision Resolution Design Document

**Status**: In progress, design complete, ready for implementation
**Date**: 2025-10-12
**Issue**: bd-9 - Build collision resolution tooling for distributed branch workflows

## Problem Statement

When branches diverge and both create issues, auto-incrementing IDs collide on merge:
- Branch A creates bd-10, bd-11, bd-12
- Branch B (diverged) creates bd-10, bd-11, bd-12 (different issues!)
- On merge: 6 issues, but 3 duplicate IDs
- References to "bd-10" in descriptions/dependencies are now ambiguous

## Design Goals

1. **Preserve brevity** - Keep bd-302 format, not bd-302-branch-a-uuid-mess
2. **Minimize disruption** - Renumber issues with fewer references
3. **Update all references** - Text fields AND dependency table
4. **Atomic operation** - All or nothing
5. **Clear feedback** - User must understand what changed

## Algorithm Design

### Phase 1: Collision Detection

```
Input: JSONL issues + current DB state
Output: Set of colliding issues

for each issue in JSONL:
  if DB contains issue.ID:
    if DB issue == JSONL issue:
      skip (already imported, idempotent)
    else:
      mark as COLLISION
```

### Phase 2: Reference Counting (The Smart Part)

Renumber issues with FEWER references first because:
- If bd-10 is referenced 20 times and bd-11 once
- Renumbering bd-11→bd-15 updates 1 reference
- Renumbering bd-10→bd-15 updates 20 references

```
for each colliding_issue:
  score = 0

  // Count text references in OTHER issues
  for each other_issue in JSONL:
    score += count_mentions(other_issue.all_text, colliding_issue.ID)

  // Count dependency references
  deps = DB.get_dependents(colliding_issue.ID)  // who depends on me?
  score += len(deps)

  // Store score
  collision_scores[colliding_issue.ID] = score

// Sort ascending: lowest score = fewest references = renumber first
sorted_collisions = sort_by(collision_scores)
```

### Phase 3: ID Allocation

```
id_mapping = {}  // old_id -> new_id
next_num = DB.get_next_id_number()

for collision in sorted_collisions:
  // Find next available ID
  while true:
    candidate = f"{prefix}-{next_num}"
    if not DB.exists(candidate) and candidate not in id_mapping.values():
      id_mapping[collision.ID] = candidate
      next_num++
      break
    next_num++
```

### Phase 4: Reference Updates

This is the trickiest part - must update:
1. Issue IDs themselves
2. Text field references (description, design, notes, acceptance_criteria)
3. Dependency records (when they reference old IDs)

```
updated_issues = []
reference_update_count = 0

for issue in JSONL:
  new_issue = clone(issue)

  // 1. Update own ID if it collided
  if issue.ID in id_mapping:
    new_issue.ID = id_mapping[issue.ID]

  // 2. Update text field references
  for old_id, new_id in id_mapping:
    for field in [title, description, design, notes, acceptance_criteria]:
      if field:
        pattern = r'\b' + re.escape(old_id) + r'\b'
        new_text, count = re.subn(pattern, new_id, field)
        field = new_text
        reference_update_count += count

  updated_issues.append(new_issue)
```

### Phase 5: Dependency Handling

**Approach A: Export dependencies in JSONL** (PREFERRED)
- Extend export to include `"dependencies": [{...}]` per issue
- Import dependencies along with issues
- Update dependency records during phase 4

Why preferred:
- Self-contained JSONL (better for git workflow)
- Easier to reason about
- Can detect cross-file dependencies

### Phase 6: Atomic Import

```
transaction:
  for issue in updated_issues:
    if issue.ID was remapped:
      DB.create_issue(issue)
    else:
      DB.upsert_issue(issue)

  // Update dependency table
  for issue in updated_issues:
    for dep in issue.dependencies:
      // dep IDs already updated in phase 4
      DB.create_or_update_dependency(dep)

  commit
```

### Phase 7: User Reporting

```
report = {
  collisions_detected: N,
  remappings: [
    "bd-10 → bd-15 (Score: 3 references)",
    "bd-11 → bd-16 (Score: 15 references)",
  ],
  text_updates: M,
  dependency_updates: K,
}
```

## Edge Cases

1. **Chain dependencies**: bd-10 depends on bd-11, both collide
   - Sorted renumbering handles this naturally
   - Lower-referenced one renumbered first

2. **Circular dependencies**: Shouldn't happen (DB has cycle detection)

3. **Partial ID matches**: "bd-1" shouldn't match "bd-10"
   - Use word boundary regex: `\bbd-10\b`

4. **Case sensitivity**: IDs are case-sensitive (bd-10 ≠ BD-10)

5. **IDs in code blocks**: Will be replaced
   - Could add `--preserve-code-blocks` flag later

6. **Triple merges**: Branch A, B, C all create bd-10
   - Algorithm handles N collisions

7. **Dependencies pointing to DB-only issues**:
   - JSONL issue depends on bd-999 (only in DB)
   - No collision, works fine

## Performance Considerations

- O(N*M) for reference counting (N issues × M collisions)
- For 1000 issues, 10 collisions: 10,000 text scans
- Acceptable for typical use (hundreds of issues)
- Could optimize with index/trie if needed

## API Design

```bash
# Default: fail on collision (safe)
bd import -i issues.jsonl
# Error: Collision detected: bd-10 already exists

# With auto-resolution
bd import -i issues.jsonl --resolve-collisions
# Resolved 3 collisions:
#   bd-10 → bd-15 (3 refs)
#   bd-11 → bd-16 (1 ref)
#   bd-12 → bd-17 (7 refs)
# Imported 45 issues, updated 23 references

# Dry run (preview changes)
bd import -i issues.jsonl --resolve-collisions --dry-run
```

## Implementation Breakdown

### Child Issues to Create

1. **bd-10**: Extend export to include dependencies in JSONL
   - Modify export.go to include dependencies array
   - Format: `{"id":"bd-10","dependencies":[{"depends_on_id":"bd-5","type":"blocks"}]}`
   - Priority: 1, Type: task

2. **bd-11**: Implement collision detection in import
   - Create collision.go with detectCollisions() function
   - Compare incoming JSONL against DB state
   - Distinguish: exact match (skip), collision (flag), new (create)
   - Priority: 1, Type: task

3. **bd-12**: Implement reference scoring algorithm
   - Count text mentions + dependency references
   - Sort collisions by score ascending (fewest refs first)
   - Minimize total updates during renumbering
   - Priority: 1, Type: task

4. **bd-13**: Implement ID remapping with reference updates
   - Allocate new IDs for colliding issues
   - Update text field references with word-boundary regex
   - Update dependency records
   - Build id_mapping for reporting
   - Priority: 1, Type: task

5. **bd-14**: Add --resolve-collisions flag and user reporting
   - Add import flags: --resolve-collisions, --dry-run
   - Display clear report with remappings and counts
   - Default: fail on collision (safe)
   - Priority: 1, Type: task

6. **bd-15**: Write comprehensive collision resolution tests
   - Test cases: simple/multiple collisions, dependencies, text refs
   - Edge cases: partial matches, case sensitivity, triple merges
   - Add to import_test.go and collision_test.go
   - Priority: 1, Type: task

7. **bd-16**: Update documentation for collision resolution
   - Update README.md with collision resolution section
   - Update CLAUDE.md with new workflow
   - Document flags and example scenarios
   - Priority: 1, Type: task

### Additional Issue: Add Design Field Support

**NEW ISSUE**: Add design field to bd update command
- Currently: `bd update` doesn't support --design flag (discovered during work)
- Need: Allow updating design, notes, acceptance_criteria fields
- This would make bd-9's design easier to attach to the issue itself
- Priority: 2, Type: feature

## Current State

- bd-9 is in_progress (claimed)
- bd-10 was successfully created (first child issue)
- bd-11+ creation failed with UNIQUE constraint (collision!)
  - This demonstrates the exact problem we're solving
  - Need to manually create remaining issues with different IDs
  - Or implement collision resolution first! (chicken/egg)

## Data Structures Involved

- **Issues table**: id, title, description, design, notes, acceptance_criteria, status, priority, issue_type, assignee, estimated_minutes, created_at, updated_at, closed_at
- **Dependencies table**: issue_id, depends_on_id, type, created_at, created_by
- **Text fields with ID references**: description, design, notes, acceptance_criteria (title too?)

## Files to Modify

1. `cmd/bd/export.go` - Add dependency export
2. `cmd/bd/import.go` - Call collision resolution
3. `cmd/bd/collision.go` - NEW FILE - Core algorithm
4. `cmd/bd/collision_test.go` - NEW FILE - Tests
5. `internal/types/types.go` - May need collision report types
6. `README.md` - Documentation
7. `CLAUDE.md` - AI agent workflow docs

## Next Steps

1. ✅ Design complete
2. 🔄 Create child issues (bd-10 created, bd-11+ need different IDs)
3. ⏳ Implement Phase 1: Export enhancement
4. ⏳ Implement Phase 2-7: Core algorithm
5. ⏳ Tests
6. ⏳ Documentation
7. ⏳ Export issues to JSONL before committing

## Meta: Real Collision Encountered!

While creating child issues, we hit the exact problem:
- bd-10 was created successfully
- bd-11, bd-12, bd-13, bd-14, bd-15, bd-16 all failed with "UNIQUE constraint failed"
- This means the DB already has bd-11+ from a previous session/import
- Perfect demonstration of why we need collision resolution!

Resolution: Create remaining child issues manually with explicit IDs after checking what exists.
