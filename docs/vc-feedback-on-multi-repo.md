# VC Feedback on Multi-Repo Contributor Workflow

**Date**: 2025-11-03
**Context**: Response to `docs/contributor-workflow-analysis.md`
**From**: VC Team (AI-supervised issue workflow system)

## Executive Summary

**Overall Assessment**: The multi-repo design is **sound and well-thought-out**. VC can adopt it post-bootstrap with minimal disruption.

**Key Concerns**:
1. **Library API stability** - Must remain transparent to library consumers
2. **Cross-repo dependency resolution** - Critical for VC's blocker-first prioritization
3. **Performance** - Hydration caching needed for VC's polling loop
4. **Namespace collisions** - Recommend Option B (global uniqueness)

**Current Status**: VC uses Beads v0.17.7 as a library, single-repo model, bootstrap phase (pre-contributors).

---

## 1. VC's Context & Usage Patterns

### How VC Uses Beads

**Architecture**:
- Beads as library: `beadsLib.NewSQLiteStorage(".beads/vc.db")`
- Extension model: VC adds tables (`vc_mission_state`, `vc_agent_events`)
- Single repo: `.beads/vc.db` + `.beads/issues.jsonl`
- Heavy use of ~20 library methods (GetIssue, CreateIssue, GetReadyWork, etc.)

**Key Workflows**:
1. **Blocker-first prioritization** - `GetReadyWork()` sorts by discovered:blocker label first
2. **Atomic claiming** - `UPDATE issues SET status='in_progress' WHERE status='open'`
3. **Auto-discovery** - AI analysis creates issues with `discovered:blocker` and `discovered:related` labels
4. **Self-healing** - Enters "degraded mode" when `baseline-failure` issues exist
5. **Executor exclusion** - `no-auto-claim` label prevents auto-claiming

**Performance Profile**:
- Polling loop: `GetReadyWork()` called every 5-10 seconds
- Need sub-second response times
- Cannot afford to re-read N JSONL files on every query

---

## 2. Impact Assessment

### Short-Term (Bootstrap Phase): ✅ MINIMAL

- Multi-repo is opt-in with backwards-compatible defaults
- VC continues with single `.beads/vc.db` and `.beads/issues.jsonl`
- No changes needed during bootstrap

### Medium-Term (Post-Bootstrap): ⚠️ LOW-MEDIUM

**Potential use cases**:
- **Testing isolation**: Separate repo for experimental executor features
- **Multi-contributor**: External contributors use `~/.beads-planning/`

**Concerns**:
- Cross-repo dependency resolution must work transparently
- Atomic claiming must preserve ACID guarantees
- Performance impact of multi-repo hydration

### Long-Term (Self-Hosting): ✅ BENEFICIAL

- Natural fit for VC's multi-contributor future
- Prevents PR pollution from contributor planning
- Aligns with VC's goal of becoming self-hosting

---

## 3. Critical Design Questions

### Q1. Library API Stability ⚠️ CRITICAL

**Question**: Is this a library API change or pure CLI feature?

**Context**: VC uses `beadsLib.NewSQLiteStorage()` and expects single JSONL file.

**What we need to know**:
- Does `NewSQLiteStorage()` API change?
- Is hydration transparent at library level?
- Or is multi-repo purely a `bd` CLI feature?

**Recommendation**:
```go
// Backwards-compatible: continue to work with no changes
store, err := beadsLib.NewSQLiteStorage(".beads/vc.db")

// Multi-repo should be configured externally (.beads/config.toml)
// and hydrated transparently by the storage layer

// If API must change, provide opt-in:
cfg := beadsLib.Config{
    Primary: ".beads/vc.db",
    Additional: []string{"~/.beads-planning"},
}
store, err := beadsLib.NewStorageWithConfig(cfg)
```

---

### Q2. Cross-Repo Dependencies ⚠️ CRITICAL

**Question**: How does `GetReadyWork()` handle cross-repo dependencies?

**Context**: VC's executor relies on dependency graph to find ready work.

**Example scenario**:
```
canonical repo (.beads/vc.db):
  vc-100 (open, P0) - ready work

planning repo (~/.beads-planning):
  vc-101 (open, P1, discovered:blocker) - ready work
  vc-102 (open, P2) depends on vc-100  ← cross-repo dependency

Expected results:
  GetReadyWork() returns [vc-101, vc-100]  ← blocker-first, then priority
                         (excludes vc-102 - blocked by vc-100)
```

**What we need**:
- Hydration layer builds unified dependency graph across all repos
- `GetReadyWork()` respects cross-repo dependencies
- Performance acceptable for frequent polling

**Recommendation**: Document cross-repo dependency behavior clearly and provide test cases.

---

### Q3. Atomic Operations Across Repos ⚠️ CRITICAL

**Question**: Are writes atomic when multiple repos are hydrated?

**Context**: VC's executor uses atomic claiming:
```go
// Must be atomic even if issue comes from planning repo
UPDATE issues SET status = 'in_progress', executor_id = ?
WHERE id = ? AND status = 'open'
```

**What we need to know**:
- If multiple repos hydrate into single `.beads/vc.db`, are writes atomic?
- How does hydration layer route writes back to correct JSONL?
- Are there race conditions between multiple processes?

**Recommendation**: Preserve ACID guarantees. Writes to hydrated database should be transparently routed to correct JSONL with transactional semantics.

---

### Q4. Visibility States vs Issue Status ⚠️ MEDIUM

**Question**: Are visibility and status orthogonal?

**Context**: VC uses `status: open | in_progress | closed` extensively.

**From document**:
```jsonl
{
  "status": "open",           // ← VC's current field
  "visibility": "local",      // ← New field proposed
  ...
}
```

**What we need to know**:
- Can an issue be `status: in_progress` and `visibility: local`?
- Does `GetReadyWork()` filter by visibility?
- Is this a breaking schema change?

**Recommendation**: Clarify orthogonality and provide migration guide.

---

### Q5. Performance - Hydration on Every Query? ⚠️ CRITICAL

**Question**: Does library-level hydration happen on every `GetReadyWork()` call?

**Context**: VC's executor polls every 5-10 seconds.

**Performance requirement**:
```go
// Executor polling loop
for {
    // Must be < 1 second, ideally < 100ms
    readyWork, err := store.GetReadyWork(ctx, filter)
    if len(readyWork) > 0 {
        claimIssue(readyWork[0])
    }
    time.Sleep(5 * time.Second)
}
```

**Recommendation**: Implement smart caching:
```go
type MultiRepoStorage struct {
    repos []RepoConfig
    cache *HydratedCache
    lastSync map[string]time.Time
}

func (s *MultiRepoStorage) GetReadyWork(ctx context.Context) ([]Issue, error) {
    // Check if any repo has changed since last sync
    for _, repo := range s.repos {
        if fileModTime(repo.JSONLPath) > s.lastSync[repo.Path] {
            s.rehydrate(repo)  // ← Only re-read changed repos
        }
    }

    // Query from cached hydrated database (fast)
    return s.cache.GetReadyWork(ctx)
}
```

**Rationale**: Cannot afford to re-parse N JSONL files every 5 seconds.

---

## 4. Design Feedback & Recommendations

### F1. Namespace Collisions ✅ VOTE FOR OPTION B

**From document's open question**:
> 1. **Namespace collisions**: If two repos both have `bd-a3f8e9`, how to handle?
>    - Option A: Hash includes repo path
>    - Option B: Global uniqueness (hash includes timestamp + random)  ← **VC PREFERS THIS**
>    - Option C: Allow collisions, use source_repo to disambiguate

**Rationale**:
- VC uses `vc-` prefix, Beads uses `bd-` prefix
- Hash-based IDs should be globally unique
- Avoids complexity of repo-scoped namespaces
- Simpler for cross-repo dependencies
- **Concern with Option C**: How does `bd dep add vc-123 vc-456` know which repo's `vc-123`?

**Recommendation**: **Option B** (global uniqueness). Include timestamp + random in hash.

---

### F2. Routing Labels vs Semantic Labels ⚠️ IMPORTANT

**From document**:
```toml
[routing.rules.label]
label = "architecture"
target = "~/.beads-work/architecture"
```

**Concern**: VC uses labels for semantic meaning, not routing:
- `discovered:blocker` - auto-generated blocker issues
- `discovered:related` - auto-generated related work
- `no-auto-claim` - prevent executor from claiming
- `baseline-failure` - self-healing baseline failures

**Problem**: If Beads uses labels for routing, this conflicts with VC's semantic labels.

**Recommendation**: Use separate mechanism for routing:
```toml
[routing.rules]
  # Option 1: Use tags instead of labels
  [[routing.rules.tag]]
    tag = "architecture"
    target = "~/.beads-work/architecture"

  # Option 2: Use issue type
  [[routing.rules.type]]
    type = "design"
    target = "~/.beads-work/architecture"

  # Option 3: Use explicit category/phase field
  [[routing.rules.phase]]
    phase = "architecture"
    target = "~/.beads-work/architecture"
```

**Rationale**: Don't overload labels - they're already a general-purpose tagging mechanism.

---

### F3. Proposal Workflow - Dependency Handling ⚠️ MEDIUM

**Question**: What happens to dependencies when an issue moves repos?

**Scenario**:
```
planning repo:
  vc-100 "Explore feature"
  vc-101 "Document findings"  (depends on vc-100)

Proposal workflow:
  bd propose vc-100  # ← Move to canonical

Result:
  canonical repo:
    vc-100 "Explore feature"

  planning repo:
    vc-101 "Document findings"  (depends on vc-100)  ← Cross-repo dep now!
```

**Recommendation**: Document this behavior clearly:
- Dependencies survive across repos (stored by ID)
- `bd ready` checks cross-repo dependencies
- Provide command: `bd dep tree --all-repos` to visualize
- Consider warning when `bd propose` creates cross-repo deps

---

### F4. Discovered Issues Routing ⚠️ MEDIUM

**Context**: VC's analysis phase auto-creates issues with labels:
- `discovered:blocker`
- `discovered:related`

**Question**: Which repo do discovered issues go to?

**Options**:
1. **Same repo as parent issue** ← **VC PREFERS THIS**
2. **Always canonical**
3. **Configurable routing**

**Rationale for Option 1**:
- Discovered issues are part of work breakdown
- Should stay with parent issue
- Avoids fragmenting related work across repos

**Example**:
```
planning repo:
  vc-100 "Explore feature" (status: in_progress)

Analysis phase discovers:
  vc-101 "Fix edge case" (discovered:blocker, parent: vc-100)

Expected: vc-101 goes to planning repo (same as vc-100)
```

---

### F5. Self-Healing Across Repos ⚠️ LOW

**Context**: VC has special behavior for `baseline-failure` label:
- Enters "degraded mode"
- Only works on baseline-failure issues until fixed

**Question**: How does this interact with multi-repo?

**Scenario**:
```
canonical repo:
  vc-300 (baseline-failure) - tests failing

planning repo:
  vc-301 (baseline-failure) - build failing

Expected: Executor sees both, enters degraded mode, works on either
```

**Recommendation**: Degraded mode should check ALL repos for baseline-failure labels.

---

## 5. Test Scenarios VC Needs to Work

### Scenario 1: Cross-Repo Blocker-First Prioritization

```
canonical repo:
  vc-100 (open, P0, no labels) - regular work

planning repo:
  vc-101 (open, P3, discovered:blocker) - blocker work

Expected: GetReadyWork() returns [vc-101, vc-100]
          (blocker-first, even though vc-101 is P3 in planning repo)
```

### Scenario 2: Cross-Repo Dependencies

```
canonical repo:
  vc-200 (open, P0)

planning repo:
  vc-201 (open, P0) depends on vc-200

Expected: GetReadyWork() returns [vc-200]
          (vc-201 is blocked by vc-200)
```

### Scenario 3: Atomic Claiming

```
planning repo:
  vc-300 (open, P0)

Executor A: Claims vc-300
Executor B: Tries to claim vc-300 concurrently

Expected: Only one executor succeeds (ACID guarantee)
          Write routes back to planning repo's JSONL
```

### Scenario 4: No-Auto-Claim Across Repos

```
canonical repo:
  vc-400 (open, P0, no-auto-claim)

planning repo:
  vc-401 (open, P0, no-auto-claim)

Expected: GetReadyWork() excludes both
          (no-auto-claim works regardless of repo or visibility)
```

### Scenario 5: Baseline Failure Degraded Mode

```
canonical repo:
  vc-500 (open, P0, baseline-failure)
  vc-501 (open, P0) - regular work

planning repo:
  vc-502 (open, P0) - regular work

Expected: Executor enters degraded mode
          Only works on vc-500 (ignores vc-501 and vc-502)
```

---

## 6. Documentation Requests

### For Library Consumers (VC's Needs)

1. **Migration guide**: How to adopt multi-repo for existing single-repo projects
2. **API stability guarantees**: What will/won't break in future versions
3. **Cross-repo dependency semantics**: Detailed behavior and examples
4. **Performance characteristics**: Hydration cost, caching strategy, optimization tips
5. **Schema changes**: Backward compatibility for visibility field

### For Multi-Repo Users

6. **Cross-repo workflow examples**: Contributor, multi-phase, multi-persona scenarios
7. **Proposal workflow**: What happens to dependencies, labels, metadata when proposing
8. **Troubleshooting**: Common issues (namespace collisions, sync conflicts, performance)
9. **Best practices**: When to use multi-repo vs single-repo, repo organization patterns

---

## 7. Open Questions for Beads Team

### Priority 1 - CRITICAL:
1. Is this a breaking change to storage library API?
2. How does cross-repo dependency resolution work at library level?
3. What's the hydration performance model for frequent queries?
4. Are atomic operations preserved across multi-repo?

### Priority 2 - IMPORTANT:
5. Which namespace collision strategy will you choose? (VC votes Option B)
6. How will routing interact with semantic labels?
7. What's the migration path for library consumers?

### Priority 3 - NICE TO HAVE:
8. How will discovered issues routing work?
9. How will special labels (baseline-failure, no-auto-claim) work across repos?
10. Will there be performance monitoring/profiling tools for multi-repo setups?

---

## 8. VC's Roadmap for Multi-Repo Adoption

### Phase 1: Bootstrap (Current)
- ✅ Stick with single repo (`.beads/vc.db`, `.beads/issues.jsonl`)
- ✅ Monitor Beads releases for API changes
- ✅ No code changes needed unless API breaks

### Phase 2: Post-Bootstrap Testing
- 📋 Evaluate multi-repo for isolated executor testing
- 📋 Test cross-repo scenarios (dependencies, claiming, performance)
- 📋 Validate blocker-first prioritization across repos

### Phase 3: Self-Hosting with Contributors
- 📋 Adopt multi-repo for contributor workflow
- 📋 Contributors use `~/.beads-planning/`
- 📋 Canonical issues stay in `.beads/issues.jsonl`
- 📋 Executor handles both transparently

---

## 9. Summary & Recommendations

### For Beads Team:

**High Priority**:
1. ✅ **Solution #4 (Separate Repos) is correct** - VCS-agnostic, clean isolation
2. ⚠️ **Library API must remain stable** - Transparent hydration for existing consumers
3. ⚠️ **Cross-repo dependencies are critical** - Must work transparently in GetReadyWork()
4. ⚠️ **Performance matters** - Smart caching needed for polling loops
5. ✅ **Choose Option B for namespaces** - Global uniqueness (timestamp + random)

**Medium Priority**:
6. ⚠️ **Don't overload labels for routing** - Use separate mechanism (tags/types/phases)
7. ⚠️ **Document cross-repo dependency behavior** - Especially in proposal workflow
8. ⚠️ **Provide migration guide** - For library consumers adopting multi-repo

**Design is fundamentally sound**. VC can adopt post-bootstrap with minimal changes IF library API remains stable.

### For VC Team:

**Short-term**: No action needed. Continue single-repo development.

**Medium-term**: Create tracking issues:
- Monitor Beads multi-repo feature development
- Evaluate adoption post-bootstrap
- Test cross-repo scenarios with executor

**Long-term**: Adopt for contributor workflow when self-hosting.

---

## 10. Contact & Follow-Up

**VC Project**: https://github.com/steveyegge/vc
**Current Beads Version**: v0.17.7
**VC's Bootstrap Status**: Phase 1 (building core executor)

**Questions for Beads team?** Feel free to ping VC maintainer or open an issue on VC repo for clarification.

**Test scenarios needed?** VC can provide more detailed test cases for cross-repo scenarios.

---

**Thank you for the thorough design doc!** This is exactly the kind of forward-thinking design discussion that helps downstream consumers prepare for changes. 🙏
