# Boundaries: When to Use bd vs TodoWrite

This reference provides detailed decision criteria for choosing between bd issue tracking and TodoWrite for task management.

## Contents

- [The Core Question](#the-core-question)
- [Decision Matrix](#decision-matrix)
  - [Use bd for](#use-bd-for): Multi-Session Work, Complex Dependencies, Knowledge Work, Side Quests, Project Memory
  - [Use TodoWrite for](#use-todowrite-for): Single-Session Tasks, Linear Execution, Immediate Context, Simple Tracking
- [Detailed Comparison](#detailed-comparison)
- [Integration Patterns](#integration-patterns)
  - Pattern 1: bd as Strategic, TodoWrite as Tactical
  - Pattern 2: TodoWrite as Working Copy of bd
  - Pattern 3: Transition Mid-Session
- [Real-World Examples](#real-world-examples)
  - Strategic Document Development, Simple Feature Implementation, Bug Investigation, Refactoring with Dependencies
- [Common Mistakes](#common-mistakes)
  - Using TodoWrite for multi-session work, using bd for simple tasks, not transitioning when complexity emerges, creating too many bd issues, never using bd
- [The Transition Point](#the-transition-point)
- [Summary Heuristics](#summary-heuristics)

## The Core Question

**"Could I resume this work after 2 weeks away?"**

- If bd would help you resume → **use bd**
- If markdown skim would suffice → **TodoWrite is fine**

This heuristic captures the essential difference: bd provides structured context that persists across long gaps, while TodoWrite excels at immediate session tracking.

## Decision Matrix

### Use bd for:

#### Multi-Session Work
Work spanning multiple compaction cycles or days where context needs to persist.

**Examples:**
- Strategic document development requiring research across multiple sessions
- Feature implementation split across several coding sessions
- Bug investigation requiring experimentation over time
- Architecture design evolving through multiple iterations

**Why bd wins**: Issues capture context that survives compaction. Return weeks later and see full history, design decisions, and current status.

#### Complex Dependencies
Work with blockers, prerequisites, or hierarchical structure.

**Examples:**
- OAuth integration requiring database setup, endpoint creation, and frontend changes
- Research project with multiple parallel investigation threads
- Refactoring with dependencies between different code areas
- Migration requiring sequential steps in specific order

**Why bd wins**: Dependency graph shows what's blocking what. `bd ready` automatically surfaces unblocked work. No manual tracking required.

#### Knowledge Work
Tasks with fuzzy boundaries, exploration, or strategic thinking.

**Examples:**
- Architecture decision requiring research into frameworks and trade-offs
- API design requiring research into multiple options
- Performance optimization requiring measurement and experimentation
- Documentation requiring understanding system architecture

**Why bd wins**: `design` and `acceptance_criteria` fields capture evolving understanding. Issues can be refined as exploration reveals more information.

#### Side Quests
Exploratory work that might pause the main task.

**Examples:**
- During feature work, discover a better pattern worth exploring
- While debugging, notice related architectural issue
- During code review, identify potential improvement
- While writing tests, find edge case requiring research

**Why bd wins**: Create issue with `discovered-from` dependency, pause main work safely. Context preserved for both tracks. Resume either one later.

#### Project Memory
Need to resume work after significant time with full context.

**Examples:**
- Open source contributions across months
- Part-time projects with irregular schedule
- Complex features split across sprints
- Research projects with long investigation periods

**Why bd wins**: Git-backed database persists indefinitely. All context, decisions, and history available on resume. No relying on conversation scrollback or markdown files.

---

### Use TodoWrite for:

#### Single-Session Tasks
Work that completes within current conversation.

**Examples:**
- Implementing a single function based on clear spec
- Fixing a bug with known root cause
- Adding unit tests for existing code
- Updating documentation for recent changes

**Why TodoWrite wins**: Simple checklist is perfect for linear execution. No need for persistence or dependencies. Clear completion within session.

#### Linear Execution
Straightforward step-by-step tasks with no branching.

**Examples:**
- Database migration with clear sequence
- Deployment checklist
- Code style cleanup across files
- Dependency updates following upgrade guide

**Why TodoWrite wins**: Steps are predetermined and sequential. No discovery, no blockers, no side quests. Just execute top to bottom.

#### Immediate Context
All information already in conversation.

**Examples:**
- User provides complete spec and asks for implementation
- Bug report with reproduction steps and fix approach
- Refactoring request with clear before/after vision
- Config changes based on user preferences

**Why TodoWrite wins**: No external context to track. Everything needed is in current conversation. TodoWrite provides user visibility, nothing more needed.

#### Simple Tracking
Just need a checklist to show progress to user.

**Examples:**
- Breaking down implementation into visible steps
- Showing validation workflow progress
- Demonstrating systematic approach
- Providing reassurance work is proceeding

**Why TodoWrite wins**: User wants to see thinking and progress. TodoWrite is visible in conversation. bd is invisible background structure.

---

## Detailed Comparison

| Aspect | bd | TodoWrite |
|--------|-----|-----------|
| **Persistence** | Git-backed, survives compaction | Session-only, lost after conversation |
| **Dependencies** | Graph-based, automatic ready detection | Manual, no automatic tracking |
| **Discoverability** | `bd ready` surfaces work | Scroll conversation for todos |
| **Complexity** | Handles nested epics, blockers | Flat list only |
| **Visibility** | Background structure, not in conversation | Visible to user in chat |
| **Setup** | Requires `.beads/` directory in project | Always available |
| **Best for** | Complex, multi-session, explorative | Simple, single-session, linear |
| **Context capture** | Design notes, acceptance criteria, links | Just task description |
| **Evolution** | Issues can be updated, refined over time | Static once written |
| **Audit trail** | Full history of changes | Only visible in conversation |

## Integration Patterns

bd and TodoWrite can coexist effectively in a session. Use both strategically.

### Pattern 1: bd as Strategic, TodoWrite as Tactical

**Setup:**
- bd tracks high-level issues and dependencies
- TodoWrite tracks current session's execution steps

**Example:**
```
bd issue: "Implement user authentication" (epic)
  ├─ Child issue: "Create login endpoint"
  ├─ Child issue: "Add JWT token validation"  ← Currently working on this
  └─ Child issue: "Implement logout"

TodoWrite (for JWT validation):
- [ ] Install JWT library
- [ ] Create token validation middleware
- [ ] Add tests for token expiry
- [ ] Update API documentation
```

**When to use:**
- Complex features with clear implementation steps
- User wants to see current progress but larger context exists
- Multi-session work currently in single-session execution phase

### Pattern 2: TodoWrite as Working Copy of bd

**Setup:**
- Start with bd issue containing full context
- Create TodoWrite checklist from bd issue's acceptance criteria
- Update bd as TodoWrite items complete

**Example:**
```
Session start:
- Check bd: "issue-auth-42: Add JWT token validation" is ready
- Extract acceptance criteria into TodoWrite
- Mark bd issue as in_progress
- Work through TodoWrite items
- Update bd design notes as you learn
- When TodoWrite completes, close bd issue
```

**When to use:**
- bd issue is ready but execution is straightforward
- User wants visible progress tracking
- Need structured approach to larger issue

### Pattern 3: Transition Mid-Session

**From TodoWrite to bd:**

Recognize mid-execution that work is more complex than anticipated.

**Trigger signals:**
- Discovering blockers or dependencies
- Realizing work won't complete this session
- Finding side quests or related issues
- Needing to pause and resume later

**How to transition:**
```
1. Create bd issue with current TodoWrite content
2. Note: "Discovered this is multi-session work during implementation"
3. Add dependencies as discovered
4. Keep TodoWrite for current session
5. Update bd issue before session ends
6. Next session: resume from bd, create new TodoWrite if needed
```

**From bd to TodoWrite:**

Rare, but happens when bd issue turns out simpler than expected.

**Trigger signals:**
- All context already clear
- No dependencies discovered
- Can complete within session
- User wants execution visibility

**How to transition:**
```
1. Keep bd issue for historical record
2. Create TodoWrite from issue description
3. Execute via TodoWrite
4. Close bd issue when done
5. Note: "Completed in single session, simpler than expected"
```

## Real-World Examples

### Example 1: Database Migration Planning

**Scenario**: Planning migration from MySQL to PostgreSQL for production application.

**Why bd**:
- Multi-session work across days/weeks
- Fuzzy boundaries - scope emerges through investigation
- Side quests - discover schema incompatibilities requiring refactoring
- Dependencies - can't migrate data until schema validated
- Project memory - need to resume after interruptions

**bd structure**:
```
db-epic: "Migrate production database to PostgreSQL"
  ├─ db-1: "Audit current MySQL schema and queries"
  ├─ db-2: "Research PostgreSQL equivalents for MySQL features" (blocks schema design)
  ├─ db-3: "Design PostgreSQL schema with type mappings"
  └─ db-4: "Create migration scripts and test data integrity" (blocked by db-3)
```

**TodoWrite role**: None initially. Might use TodoWrite for single-session testing sprints once migration scripts ready.

### Example 2: Simple Feature Implementation

**Scenario**: Add logging to existing endpoint based on clear specification.

**Why TodoWrite**:
- Single session work
- Linear execution - add import, call logger, add test
- All context in user message
- Completes within conversation

**TodoWrite**:
```
- [ ] Import logging library
- [ ] Add log statements to endpoint
- [ ] Add test for log output
- [ ] Run tests
```

**bd role**: None. Overkill for straightforward task.

### Example 3: Bug Investigation

**Initial assessment**: Seems simple, try TodoWrite first.

**TodoWrite**:
```
- [ ] Reproduce bug
- [ ] Identify root cause
- [ ] Implement fix
- [ ] Add regression test
```

**What actually happens**: Reproducing bug reveals it's intermittent. Root cause investigation shows multiple potential issues. Needs time to investigate.

**Transition to bd**:
```
Create bd issue: "Fix intermittent auth failure in production"
  - Description: Initially seemed simple but reproduction shows complex race condition
  - Design: Three potential causes identified, need to test each
  - Created issues for each hypothesis with discovered-from dependency

Pause for day, resume next session from bd context
```

### Example 4: Refactoring with Dependencies

**Scenario**: Extract common validation logic from three controllers.

**Why bd**:
- Dependencies - must extract before modifying callers
- Multi-file changes need coordination
- Potential side quest - might discover better pattern during extraction
- Need to track which controllers updated

**bd structure**:
```
refactor-1: "Create shared validation module"
  → blocks refactor-2, refactor-3, refactor-4

refactor-2: "Update auth controller to use shared validation"
refactor-3: "Update user controller to use shared validation"
refactor-4: "Update payment controller to use shared validation"
```

**TodoWrite role**: Could use TodoWrite for individual controller updates as implementing.

**Why this works**: bd ensures you don't forget to update a controller. `bd ready` shows next available work. Dependencies prevent starting controller update before extraction complete.

## Common Mistakes

### Mistake 1: Using TodoWrite for Multi-Session Work

**What happens**:
- Next session, forget what was done
- Scroll conversation history to reconstruct
- Lose design decisions made during implementation
- Start over or duplicate work

**Solution**: Create bd issue instead. Persist context across sessions.

### Mistake 2: Using bd for Simple Linear Tasks

**What happens**:
- Overhead of creating issue not justified
- User can't see progress in conversation
- Extra tool use for no benefit

**Solution**: Use TodoWrite. It's designed for exactly this case.

### Mistake 3: Not Transitioning When Complexity Emerges

**What happens**:
- Start with TodoWrite for "simple" task
- Discover blockers and dependencies mid-way
- Keep using TodoWrite despite poor fit
- Lose context when conversation ends

**Solution**: Transition to bd when complexity signal appears. Not too late mid-session.

### Mistake 4: Creating Too Many bd Issues

**What happens**:
- Every tiny task gets an issue
- Database cluttered with trivial items
- Hard to find meaningful work in `bd ready`

**Solution**: Reserve bd for work that actually benefits from persistence. Use "2 week test" - would bd help resume after 2 weeks? If no, skip it.

### Mistake 5: Never Using bd Because TodoWrite is Familiar

**What happens**:
- Multi-session projects become markdown swamps
- Lose track of dependencies and blockers
- Can't resume work effectively
- Rotten half-implemented plans

**Solution**: Force yourself to use bd for next multi-session project. Experience the difference in organization and resumability.

### Mistake 6: Always Asking Before Creating Issues (or Never Asking)

**When to create directly** (no user question needed):
- **Bug reports**: Clear scope, specific problem ("Found: auth doesn't check profile permissions")
- **Research tasks**: Investigative work ("Research workaround for Slides export")
- **Technical TODOs**: Discovered during implementation ("Add validation to form handler")
- **Side quest capture**: Discoveries that need tracking ("Issue: MCP can't read Shared Drive files")

**Why create directly**: Asking slows discovery capture. User expects proactive issue creation for clear-cut problems.

**When to ask first** (get user input):
- **Strategic work**: Fuzzy boundaries, multiple valid approaches ("Should we implement X or Y pattern?")
- **Potential duplicates**: Might overlap with existing work
- **Large epics**: Multiple approaches, unclear scope ("Plan migration strategy")
- **Major scope changes**: Changing direction of existing issue

**Why ask**: Ensures alignment on fuzzy work, prevents duplicate effort, clarifies scope before investment.

**Rule of thumb**: If you can write a clear, specific issue title and description in one sentence, create directly. If you need user input to clarify the work, ask first.

**Examples**:
- ✅ Create directly: "workspace MCP: Google Doc → .docx export fails with UTF-8 encoding error"
- ✅ Create directly: "Research: Workarounds for reading Google Slides from Shared Drives"
- ❓ Ask first: "Should we refactor the auth system now or later?" (strategic decision)
- ❓ Ask first: "I found several data validation issues, should I file them all?" (potential overwhelming)

## The Transition Point

Most work starts with an implicit mental model:

**"This looks straightforward"** → TodoWrite

**As work progresses:**

✅ **Stays straightforward** → Continue with TodoWrite, complete in session

⚠️ **Complexity emerges** → Transition to bd, preserve context

The skill is recognizing the transition point:

**Transition signals:**
- "This is taking longer than expected"
- "I've discovered a blocker"
- "This needs more research"
- "I should pause this and investigate X first"
- "The user might not be available to continue today"
- "I found three related issues while working on this"

**When you notice these signals**: Create bd issue, preserve context, work from structured foundation.

## Summary Heuristics

Quick decision guides:

**Time horizon:**
- Same session → TodoWrite
- Multiple sessions → bd

**Dependency structure:**
- Linear steps → TodoWrite
- Blockers/prerequisites → bd

**Scope clarity:**
- Well-defined → TodoWrite
- Exploratory → bd

**Context complexity:**
- Conversation has everything → TodoWrite
- External context needed → bd

**User interaction:**
- User watching progress → TodoWrite visible in chat
- Background work → bd invisible structure

**Resume difficulty:**
- Easy from markdown → TodoWrite
- Need structured history → bd

When in doubt: **Use the 2-week test**. If you'd struggle to resume this work after 2 weeks without bd, use bd.
