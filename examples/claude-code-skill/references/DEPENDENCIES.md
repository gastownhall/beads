# Dependency Types Guide

Deep dive into bd's four dependency types: blocks, related, parent-child, and discovered-from.

## Contents

- [Overview](#overview) - Four types at a glance, which affect bd ready?
- [blocks - Hard Blocker](#blocks---hard-blocker)
  - [When to Use](#when-to-use) - Prerequisites, sequential steps, build order
  - [When NOT to Use](#when-not-to-use) - Soft preferences, parallel work
  - [Examples](#examples) - API development, migrations, library dependencies
  - [Creating blocks Dependencies](#creating-blocks-dependencies)
  - [Common Patterns](#common-patterns) - Build foundation first, migration sequences, testing gates
  - [Automatic Unblocking](#automatic-unblocking)
- [related - Soft Link](#related---soft-link)
  - [When to Use](#when-to-use-1) - Context, related features, parallel work
  - [When NOT to Use](#when-not-to-use-1)
  - [Examples](#examples-1) - Feature context, research links, parallel development
  - [Creating related Dependencies](#creating-related-dependencies)
  - [Common Patterns](#common-patterns-1) - Context clusters, research threads, feature families
- [parent-child - Hierarchical](#parent-child---hierarchical)
  - [When to Use](#when-to-use-2) - Epics/subtasks, phases
  - [When NOT to Use](#when-not-to-use-2)
  - [Examples](#examples-2) - Epic with subtasks, phased projects
  - [Creating parent-child Dependencies](#creating-parent-child-dependencies)
  - [Combining with blocks](#combining-with-blocks)
  - [Common Patterns](#common-patterns-2) - Epic decomposition, nested hierarchies
- [discovered-from - Provenance](#discovered-from---provenance)
  - [When to Use](#when-to-use-3) - Side quests, research findings
  - [Why This Matters](#why-this-matters)
  - [Examples](#examples-3) - Bug discovered during feature work, research branches
  - [Creating discovered-from Dependencies](#creating-discovered-from-dependencies)
  - [Common Patterns](#common-patterns-3) - Discovery during implementation, research expansion
  - [Combining with blocks](#combining-with-blocks-1)
- [Decision Guide](#decision-guide)
  - [Decision Tree](#decision-tree)
  - [Quick Reference by Situation](#quick-reference-by-situation)
- [Common Mistakes](#common-mistakes)
  - Using blocks for preferences, using discovered-from for planning, not using dependencies, over-using blocks, wrong direction
- [Advanced Patterns](#advanced-patterns)
  - Diamond dependencies, optional dependencies, discovery cascade, epic with phases
- [Visualization](#visualization)
- [Summary](#summary)

## Overview

bd supports four dependency types that serve different purposes in organizing and tracking work:

| Type | Purpose | Affects `bd ready`? | Common Use |
|------|---------|---------------------|------------|
| **blocks** | Hard blocker | Yes - blocked issues excluded | Sequential work, prerequisites |
| **related** | Soft link | No - just informational | Context, related work |
| **parent-child** | Hierarchy | No - structural only | Epics and subtasks |
| **discovered-from** | Provenance | No - tracks origin | Side quests, research findings |

**Key insight**: Only `blocks` dependencies affect what work is ready. The other three provide structure and context.

---

## blocks - Hard Blocker

**Semantics**: Issue A blocks issue B. B cannot start until A is complete.

**Effect**: Issue B disappears from `bd ready` until issue A is closed.

### When to Use

Use `blocks` when work literally cannot proceed:

- **Prerequisites**: Database schema must exist before endpoints can use it
- **Sequential steps**: Migration step 1 must complete before step 2
- **Build order**: Foundation must be done before building on top
- **Technical blockers**: Library must be installed before code can use it

### When NOT to Use

Don't use `blocks` for:

- **Soft preferences**: "Should do X before Y but could do either"
- **Parallel work**: Both can proceed independently
- **Information links**: Just want to note relationship
- **Recommendations**: "Would be better if done in this order"

Use `related` instead for soft connections.

### Examples

**Example 1: API Development**

```
db-schema-1: "Create users table"
  blocks
api-endpoint-2: "Add GET /users endpoint"

Why: Endpoint literally needs table to exist
Effect: api-endpoint-2 won't show in bd ready until db-schema-1 closed
```

**Example 2: Migration Sequence**

```
migrate-1: "Backup production database"
  blocks
migrate-2: "Run schema migration"
  blocks
migrate-3: "Verify data integrity"

Why: Each step must complete before next can safely proceed
Effect: bd ready shows only migrate-1; closing it reveals migrate-2, etc.
```

**Example 3: Library Installation**

```
setup-1: "Install JWT library"
  blocks
auth-2: "Implement JWT validation"

Why: Code won't compile/run without library
Effect: Can't start auth-2 until setup-1 complete
```

### Creating blocks Dependencies

```bash
bd dep add prerequisite-issue blocked-issue
# or explicitly:
bd dep add prerequisite-issue blocked-issue --type blocks
```

**Direction matters**: `from_id` blocks `to_id`. Think: "prerequisite blocks dependent".

### Common Patterns

**Pattern: Build Foundation First**

```
foundation-1: "Set up authentication system"
  blocks all of:
    - feature-2: "Add user profiles"
    - feature-3: "Add admin panel"
    - feature-4: "Add API access"

One foundational issue blocks multiple dependent features.
```

**Pattern: Sequential Pipeline**

```
step-1 blocks step-2 blocks step-3 blocks step-4

Linear chain where each step depends on previous.
bd ready shows only current step.
```

**Pattern: Parallel Then Merge**

```
research-1: "Investigate option A"
research-2: "Investigate option B"
research-3: "Investigate option C"
All three block:
  decision-4: "Choose approach based on research"

Multiple parallel tasks must complete before next step.
```

### Automatic Unblocking

When you close an issue that's blocking others:

```
1. Close db-schema-1
2. bd automatically updates: api-endpoint-2 is now ready
3. bd ready shows api-endpoint-2
4. No manual unblocking needed
```

This is why `blocks` is powerful - bd maintains ready state automatically.

---

## related - Soft Link

**Semantics**: Issues are related but neither blocks the other.

**Effect**: No impact on `bd ready`. Pure informational link.

### When to Use

Use `related` for context and discoverability:

- **Similar work**: "These tackle the same problem from different angles"
- **Shared context**: "Working on one provides insight for the other"
- **Alternative approaches**: "These are different ways to solve X"
- **Complementary features**: "These work well together but aren't required"

### When NOT to Use

Don't use `related` if:

- One actually blocks the other → use `blocks`
- One discovered the other → use `discovered-from`
- One is parent of the other → use `parent-child`

### Examples

**Example 1: Related Refactoring**

```
refactor-1: "Extract validation logic"
  related to
refactor-2: "Extract error handling logic"

Why: Both are refactoring efforts, similar patterns, but independent
Effect: None on ready state; just notes the relationship
```

**Example 2: Documentation and Code**

```
feature-1: "Add OAuth login"
  related to
docs-2: "Document OAuth setup"

Why: Docs and feature go together, but can be done in any order
Effect: Can work on either whenever; just notes they're connected
```

**Example 3: Alternative Approaches**

```
perf-1: "Investigate Redis caching"
  related to
perf-2: "Investigate CDN caching"

Why: Both address performance, different approaches, explore both
Effect: Both show in bd ready; choosing one doesn't block the other
```

### Creating related Dependencies

```bash
bd dep add issue-1 issue-2 --type related
```

**Direction doesn't matter** for `related` - it's a symmetric link.

### Common Patterns

**Pattern: Cluster Related Work**

```
api-redesign related to:
  - api-docs-update
  - api-client-update
  - api-tests-update
  - api-versioning

Group of issues all related to API work.
Use related to show they're part of same initiative.
```

**Pattern: Cross-Cutting Concerns**

```
security-audit related to:
  - auth-module
  - api-endpoints
  - database-access
  - frontend-forms

Security audit touches multiple areas.
Related links show what areas it covers.
```

---

## parent-child - Hierarchical

**Semantics**: Issue A is parent of issue B. Typically A is an epic, B is a subtask.

**Effect**: No impact on `bd ready`. Creates hierarchical structure.

### When to Use

Use `parent-child` for breaking down large work:

- **Epics and subtasks**: Big feature split into smaller pieces
- **Hierarchical organization**: Logical grouping of related tasks
- **Progress tracking**: See completion of children relative to parent
- **Work breakdown structure**: Decompose complex work

### When NOT to Use

Don't use `parent-child` if:

- Siblings need ordering → add `blocks` between children
- Relationship is equality → use `related`
- Just discovered one from the other → use `discovered-from`

### Examples

**Example 1: Feature Epic**

```
oauth-epic: "Implement OAuth integration" (epic)
  parent of:
    - oauth-1: "Set up OAuth credentials" (task)
    - oauth-2: "Implement authorization flow" (task)
    - oauth-3: "Add token refresh" (task)
    - oauth-4: "Create login UI" (task)

Why: Epic decomposed into implementable tasks
Effect: Hierarchical structure; all show in bd ready (unless blocked)
```

**Example 2: Research with Findings**

```
research-epic: "Investigate caching strategies" (epic)
  parent of:
    - research-1: "Redis evaluation"
    - research-2: "Memcached evaluation"
    - research-3: "CDN evaluation"
    - decision-4: "Choose caching approach"

Why: Research project with multiple investigation threads
Effect: Can track progress across all investigations
```

### Creating parent-child Dependencies

```bash
bd dep add parent-epic-id child-task-id --type parent-child
```

**Direction matters**: `from_id` is parent, `to_id` is child.

### Combining with blocks

Parent-child gives structure; blocks gives ordering:

```
auth-epic (parent of all)
  ├─ auth-1: "Install library"
  ├─ auth-2: "Create middleware" (blocked by auth-1)
  ├─ auth-3: "Add endpoints" (blocked by auth-2)
  └─ auth-4: "Add tests" (blocked by auth-3)

parent-child: Shows these are all part of auth epic
blocks: Shows they must be done in order
```

### Common Patterns

**Pattern: Epic with Independent Subtasks**

```
Epic with no ordering between children:
All children show in bd ready immediately.
Work on any child in any order.
Close epic when all children complete.
```

**Pattern: Epic with Sequential Subtasks**

```
Epic with blocks dependencies between children:
bd ready shows only first child.
Closing each child unblocks next.
Epic provides structure, blocks provides order.
```

**Pattern: Nested Epics**

```
major-epic
  ├─ sub-epic-1
  │   ├─ task-1a
  │   └─ task-1b
  └─ sub-epic-2
      ├─ task-2a
      └─ task-2b

Multiple levels of hierarchy for complex projects.
```

---

## discovered-from - Provenance

**Semantics**: Issue B was discovered while working on issue A.

**Effect**: No impact on `bd ready`. Tracks origin and provides context.

### When to Use

Use `discovered-from` to preserve discovery context:

- **Side quests**: Found new work during implementation
- **Research findings**: Discovered issue while investigating
- **Bug found during feature work**: Context of discovery matters
- **Follow-up work**: Identified next steps during current work

### Why This Matters

Knowing where an issue came from helps:

- **Understand context**: Why was this created?
- **Reconstruct thinking**: What led to this discovery?
- **Assess relevance**: Is this still important given original context?
- **Track exploration**: See what emerged from research

### Examples

**Example 1: Bug During Feature**

```
feature-10: "Add user profiles"
  discovered-from leads to
bug-11: "Existing auth doesn't handle profile permissions"

Why: While adding profiles, discovered auth system inadequate
Context: Bug might not exist if profiles weren't being added
```

**Example 2: Research Findings**

```
research-5: "Investigate caching options"
  discovered-from leads to
finding-6: "Redis supports persistence unlike Memcached"
finding-7: "CDN caching incompatible with our auth model"
decision-8: "Choose Redis based on findings"

Why: Research generated specific findings
Context: Findings only relevant in context of research question
```

**Example 3: Refactoring Reveals Technical Debt**

```
refactor-20: "Extract validation logic"
  discovered-from leads to
debt-21: "Validation inconsistent across controllers"
debt-22: "No validation for edge cases"
improvement-23: "Could add validation library"

Why: Refactoring work revealed multiple related issues
Context: Issues discovered as side effect of refactoring
```

### Creating discovered-from Dependencies

```bash
bd dep add original-work-id discovered-issue-id --type discovered-from
```

**Direction matters**: `to_id` was discovered while working on `from_id`.

### Common Patterns

**Pattern: Exploration Tree**

```
spike-1: "Investigate API redesign"
  discovered-from →
    finding-2: "Current API mixes REST and GraphQL"
    finding-3: "Authentication not consistent"
    finding-4: "Rate limiting missing"

One exploration generates multiple findings.
Tree structure shows exploration process.
```

**Pattern: Bug Investigation Chain**

```
bug-1: "Login fails intermittently"
  discovered-from →
    bug-2: "Race condition in session creation"
      discovered-from →
        bug-3: "Database connection pool too small"

Investigation of one bug reveals root cause as another bug.
Chain shows how you got from symptom to cause.
```

**Pattern: Feature Implementation Side Quests**

```
feature-main: "Add shopping cart"
  discovered-from →
    improvement-a: "Product images should be cached"
    bug-b: "Price formatting wrong for some locales"
    debt-c: "Inventory system needs refactoring"

Main feature work generates tangential discoveries.
Captured for later without derailing main work.
```

### Combining with blocks

Can use both together:

```
feature-10: "Add user profiles"
  discovered-from →
    bug-11: "Auth system needs role-based access"
      blocks →
        feature-10: "Add user profiles"

Discovery: Found bug during feature work
Assessment: Bug actually blocks feature
Actions: Mark feature blocked, work on bug first
```

---

## Decision Guide

**"Which dependency type should I use?"**

### Decision Tree

```
Does Issue A prevent Issue B from starting?
  YES → blocks
  NO ↓

Is Issue B a subtask of Issue A?
  YES → parent-child (A parent, B child)
  NO ↓

Was Issue B discovered while working on Issue A?
  YES → discovered-from (A original, B discovered)
  NO ↓

Are Issues A and B just related?
  YES → related
```

### Quick Reference by Situation

| Situation | Use |
|-----------|-----|
| B needs A complete to start | blocks |
| B is part of A (epic/task) | parent-child |
| Found B while working on A | discovered-from |
| A and B are similar/connected | related |
| B should come after A but could start | related + note |
| A and B are alternatives | related |
| B is follow-up to A | discovered-from |

---

## Common Mistakes

### Mistake 1: Using blocks for Preferences

**Wrong**:
```
docs-1: "Update documentation"
  blocks
feature-2: "Add new feature"

Reason: "We prefer to update docs first"
```

**Problem**: Documentation doesn't actually block feature implementation.

**Right**: Use `related` or don't link at all. If you want ordering, note it in issue descriptions but don't enforce with blocks.

### Mistake 2: Using discovered-from for Planning

**Wrong**:
```
epic-1: "OAuth integration"
  discovered-from →
    task-2: "Set up OAuth credentials"

Reason: "I'm planning these tasks from the epic"
```

**Problem**: `discovered-from` is for emergent discoveries, not planned decomposition.

**Right**: Use `parent-child` for planned task breakdown.

### Mistake 3: Not Using Any Dependencies

**Symptom**: Long list of issues with no structure.

**Problem**: Can't tell what's blocked, what's related, how work is organized.

**Solution**: Add structure with dependencies:
- Group with parent-child
- Order with blocks
- Link with related
- Track discovery with discovered-from

### Mistake 4: Over-Using blocks

**Wrong**:
```
Everything blocks everything else in strict sequential order.
```

**Problem**: No parallel work possible; `bd ready` shows only one issue.

**Right**: Only use `blocks` for actual technical dependencies. Allow parallel work where possible.

### Mistake 5: Wrong Direction

**Wrong**:
```bash
bd dep add api-endpoint database-schema

Meaning: api-endpoint blocks database-schema
```

**Problem**: Backwards! Schema should block endpoint, not other way around.

**Right**:
```bash
bd dep add database-schema api-endpoint

Meaning: database-schema blocks api-endpoint
```

**Mnemonic**: "from_id blocks to_id" or "prerequisite blocks dependent"

---

## Advanced Patterns

### Pattern: Diamond Dependencies

```
        setup
       /    \
   impl-a  impl-b
       \    /
       testing

setup blocks both impl-a and impl-b
both impl-a and impl-b block testing
```

Both implementations must complete before testing can begin.

### Pattern: Optional Dependencies

```
core-feature (ready immediately)
  related to
nice-to-have (ready immediately)

Both can be done, neither blocks the other.
Use related to show they're connected.
```

### Pattern: Discovery Cascade

```
research-main
  discovered-from → finding-1
  discovered-from → finding-2
    discovered-from → deep-finding-3

Research generates findings.
Findings generate deeper findings.
Tree shows discovery process.
```

### Pattern: Epic with Phases

```
auth-epic
  parent of phase-1-epic
    parent of: setup-1, setup-2, setup-3
  parent of phase-2-epic
    parent of: implement-1, implement-2
  parent of phase-3-epic
    parent of: test-1, test-2

phase-1-epic blocks phase-2-epic blocks phase-3-epic

Nested hierarchy with phase ordering.
```

---

## Visualization

When you run `bd show issue-id` on an issue, you see:

```
Issue: feature-10
Dependencies (blocks this issue):
  - setup-5: "Install library"
  - config-6: "Add configuration"

Dependents (blocked by this issue):
  - test-12: "Add integration tests"
  - docs-13: "Document new feature"

Related:
  - refactor-8: "Similar refactoring effort"

Discovered from:
  - research-3: "API investigation"
```

This shows the full dependency context for an issue.

---

## Summary

**Four dependency types, four different purposes:**

1. **blocks**: Sequential work, prerequisites, hard blockers
   - Affects bd ready
   - Use for technical dependencies only

2. **related**: Context, similar work, soft connections
   - Informational only
   - Use liberally for discoverability

3. **parent-child**: Epics and subtasks, hierarchical structure
   - Organizational only
   - Use for work breakdown

4. **discovered-from**: Side quests, research findings, provenance
   - Context preservation
   - Use to track emergence

**Key insight**: Only `blocks` affects what work is ready. The other three provide rich context without constraining execution.

Use dependencies to create a graph that:
- Automatically maintains ready work
- Preserves discovery context
- Shows project structure
- Links related work

This graph becomes the persistent memory that survives compaction and enables long-horizon agent work.
