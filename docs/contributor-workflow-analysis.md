# Beads Contributor Workflow Analysis

**Date**: 2025-11-03
**Context**: Design discussion on how to handle beads issues in PR/OSS contribution workflows

## The Problem (from #207)

When contributing to OSS projects with beads installed:
- Git hooks automatically commit contributor's personal planning to PRs
- Contributor's experimental musings pollute the upstream project's issue tracker
- No clear ownership/permission model for external contributors
- Difficult to keep beads changes out of commits

**Core tension**: Beads is great for team planning (shared namespace), but breaks down for OSS contributions (hierarchical gatekeeping).

## Key Insights from Discussion

### Beads as "Moving Frontier"

Beads is not a traditional issue tracker. It captures the **active working set** - the sliding window of issues currently under attention:

- Work moves fast with AI agents (10x-50x acceleration)
- Completed work fades quickly (95% never revisited, should be pruned aggressively)
- Future work is mostly blocked (small frontier of ready tasks)
- The frontier is bounded by team size (dozens to hundreds of issues, not thousands)

**Design principle**: Beads should focus on the "what's next" cloud, not long-term planning or historical archive.

### The Git Ledger is Fundamental

Beads achieves reliability despite being unreliable (merge conflicts, sync issues, data staleness) through:

**A. Git is the ledger and immutable backstop for forensics**
**B. AI is the ultimate arbiter and problem-solver when things go wrong**

Any solution that removes the git ledger (e.g., gitignored contributor files) breaks this model entirely.

### Requirements for Contributors

Contributors need:
- Git-backed persistence (multi-clone sync, forensics, AI repair)
- Isolated planning space (don't pollute upstream)
- Ability to propose selected issues upstream
- Support for multiple workers across multiple clones of the same repo

## Proposed Solutions

### Idea 1: Fork-Aware Hooks + Two-File System

**Structure**:
```
# Upstream repo
.beads/
  beads.jsonl          # Canonical frontier (committed)
  .gitignore           # Ignores local.jsonl

# Contributor's fork
.beads/
  beads.jsonl          # Synced from upstream (read-only)
  local.jsonl          # Contributor planning (committed to fork)
  beads.db             # Hydrated from both
```

**Detection**: Check for `upstream` remote to distinguish fork from canonical repo

**Workflow**:
```bash
# In fork
$ bd add "Experiment"        # → local.jsonl (committed to fork)
$ bd sync                    # → Pulls upstream's beads.jsonl
$ bd show                    # → Shows both
$ bd propose bd-a3f8e9       # → Moves issue to beads.jsonl for PR
```

**Pros**:
- Git ledger preserved (local.jsonl committed to fork)
- Multi-clone sync works
- Upstream .gitignore prevents pollution

**Cons**:
- Fork detection doesn't help teams using branches (most common workflow)
- Two files to manage
- Requires discipline to use `bd propose`

### Idea 2: Ownership Metadata + Smart PR Filtering

**Structure**:
```jsonl
{"id":"bd-123","owner":"upstream","title":"Canonical issue",...}
{"id":"bd-456","owner":"stevey","title":"My planning",...}
```

**Workflow**:
```bash
$ bd add "Experiment"        # → Creates with owner="stevey"
$ bd propose bd-456          # → Changes owner to "upstream"
$ bd clean-pr                # → Filters commit to only upstream-owned issues
$ git push                   # → PR contains only proposed issues
```

**Pros**:
- Single file (simpler)
- Works with any git workflow (branch, fork, etc)
- Git ledger fully preserved

**Cons**:
- Requires discipline to run `bd clean-pr`
- Clean commit is awkward (temporarily removing data)
- Merge conflicts if upstream and contributor both modify beads.jsonl

### Idea 3: Branch-Scoped Databases

Track which issues belong to which branch, filter at PR time.

**Implementation**: Similar to #2 but uses labels/metadata to track branch instead of owner.

**Challenge**: Complex with multiple feature branches, requires tracking branch scope.

### Idea 4: Separate Planning Repo (Most Isolated)

**Structure**:
```bash
# Main project repos (many)
~/projects/beads/.beads/beads.jsonl
~/projects/foo/.beads/beads.jsonl

# Single planning repo (one)
~/.beads-planning/.beads/beads.jsonl

# Configuration links them
~/projects/beads/.beads/config.toml:
  planning_repo = "~/.beads-planning"
```

**Workflow**:
```bash
$ cd ~/projects/beads
$ bd add "My idea"           # → Commits to ~/.beads-planning/
$ bd show                    # → Shows beads canonical + my planning
$ bd propose bd-a3f8e9       # → Exports to beads repo for PR
```

**Pros**:
- Complete isolation (separate git histories, zero PR pollution risk)
- Git ledger fully preserved (both repos tracked)
- Multi-clone works perfectly (clone both repos)
- No special filtering/detection needed
- **Scales better**: One planning repo for all projects

**Cons**:
- Two repos to manage
- Less obvious for new users (where's my planning?)

## Analysis: Fork vs Clone vs Branch

**Clone**: Local copy of a repo (`git clone <url>`)
- `origin` remote points to source
- Push directly to origin (if you have write access)

**Fork**: Server-side copy on GitHub
- For contributors without write access
- `origin` → your fork, `upstream` → original repo
- Push to fork, then PR from fork → upstream

**Branch**: Feature branches in same repo
- Most common for teams with write access
- Push to same repo, PR from branch → main

**Key insight**: Branches are universal, forks are only for external contributors. Most teams work on branches in a shared repo.

## Current Thinking: Idea 4 is Cleanest

After analysis, **separate planning repo (#4)** is likely the best solution because:

1. **Only solution that truly prevents PR pollution** (separate git histories)
2. **Git ledger fully preserved** (both repos tracked)
3. **Multi-clone works perfectly** (just clone both)
4. **No complex filtering/detection needed** (simple config)
5. **Better scaling**: One planning repo across all projects you contribute to

The "managing two repos" concern is actually an advantage: your planning is centralized and project-agnostic.

## Open Questions

### About the Workflow

1. **Where does PR pollution actually happen?**
   - Scenario A: Feature branch → upstream/main includes all beads changes from that branch?
   - Scenario B: Something else?

2. **Multi-clone usage pattern**:
   - Multiple clones on different machines?
   - All push/pull to same remote?
   - Workers coordinate via git sync?
   - PRs created from feature branches?

### About Implementation

1. **Proposed issue IDs**: When moving issue from planning → canonical, keep same ID? (Hash-based IDs are globally unique)

2. **Upstream acceptance sync**: If upstream accepts/modifies a proposal, how to sync back to contributor?
   - `bd sync` detects accepted proposals
   - Moves from planning repo to project's canonical beads.jsonl

3. **Multiple projects**: One planning repo for all projects you contribute to, or one per project?

4. **Backwards compatibility**: Single-user projects unchanged (single beads.jsonl)

5. **Discovery**: How do users discover this feature? Auto-detect and prompt?

## Next Steps

Need to clarify:
1. User's actual multi-clone workflow (understand the real use case)
2. Where exactly PR pollution occurs (branch vs fork workflow)
3. Which solution best fits the "git ledger + multi-clone" requirements
4. Whether centralized planning repo (#4) or per-project isolation (#1/#2) is preferred

## Design Principles to Preserve

From the conversation, these are non-negotiable:

- **Git as ledger**: Everything must be git-tracked for forensics and AI repair
- **Moving frontier**: Focus on active work, aggressively prune completed work
- **Multi-clone sync**: Workers across clones must coordinate via git
- **Small databases**: Keep beads.jsonl small enough for agents to read (<25k)
- **Simple defaults**: Don't break single-user workflows
- **Explicit over implicit**: Clear boundaries between personal and canonical

---

# Decision: Separate Repos (Solution #4)

**Date**: 2025-11-03 (continued discussion)

## Why Separate Repos

After consideration, **Solution #4 (Separate Planning Repos)** is the chosen approach:

### Key Rationale

1. **Beads as a Separate Channel**: Beads is fundamentally a separate communication channel that happens to use git/VCS for persistence, not a git-centric tool. It should work with any VCS (jujutsu, sapling, mercurial, etc.).

2. **VCS-Agnostic Design**: Solution #1 (fork detection) is too git-centric and wouldn't work with other version control systems. Separate repos work regardless of VCS.

3. **Maximum Flexibility**: Supports multiple workflows and personas:
   - OSS contributor with personal planning
   - Multi-phase development (different beads DBs for different stages)
   - Multiple personas (architect, implementer, reviewer)
   - Team vs personal planning separation

4. **Zero PR Pollution Risk**: Completely separate git histories guarantee no accidental pollution of upstream projects.

5. **Proven Pain Point**: Experience shows that accidental bulk commits (100k issues) can be catastrophic and traumatic to recover from. Complete isolation is worth the complexity.

## Core Architecture Principles

### 1. Multi-Repo Support (N ≥ 1)

**Configuration should support N repos, including N=1 for backward compatibility:**

When N=1 (default), this is the current single-repo workflow - no changes needed.
When N≥2, multiple repos are hydrated together.

```toml
# .beads/config.toml

# Default mode: single repo (backwards compatible)
mode = "single"

# Multi-repo mode
[repos]
  # Primary repo: where canonical issues live
  primary = "."

  # Additional repos to hydrate into the database
  additional = [
    "~/.beads-planning",           # Personal planning across all projects
    "~/.beads-work/phase1",        # Architecting phase
    "~/.beads-work/phase2",        # Implementation phase
    "~/team-shared/.beads",        # Shared team planning
  ]

# Routing: where do new issues go?
[routing]
  mode = "auto"  # auto | explicit
  default = "~/.beads-planning"    # Default for `bd add`

  # Auto-detection: based on user permissions
  [routing.auto]
    maintainer = "."                   # If maintainer, use primary
    contributor = "~/.beads-planning"  # Otherwise use planning repo
```

### 2. Hydration Model

On `bd show`, `bd list`, etc., the database hydrates from multiple sources:

```
beads.db ← [
  ./.beads/beads.jsonl           (primary, read-write if maintainer)
  ~/.beads-planning/beads.jsonl   (personal, read-write)
  ~/team-shared/.beads/beads.jsonl (shared, read-write if team member)
]
```

**Metadata tracking**:
```jsonl
{
  "id": "bd-a3f8e9",
  "title": "Add dark mode",
  "source_repo": "~/.beads-planning",  # Which repo owns this issue
  "visibility": "local",               # local | proposed | canonical
  ...
}
```

### 3. Visibility States

Issues can be in different states of visibility:

- **local**: Personal planning, only in planning repo
- **proposed**: Exported for upstream consideration (staged for PR)
- **canonical**: In the primary repo (upstream accepted it)

### 4. VCS-Agnostic Operations

Beads should not assume git. Core operations:

- **Sync**: `bd sync` should work with git, jj, hg, sl, etc.
- **Ledger**: Each repo uses whatever VCS it's under (or none)
- **Transport**: Issues move between repos via export/import, not git-specific operations

## Workflow Examples

### Use Case 1: OSS Contributor

```bash
# One-time setup
$ mkdir ~/.beads-planning
$ cd ~/.beads-planning
$ git init && bd init

# Contributing to upstream project
$ cd ~/projects/some-oss-project
$ bd config --add-repo ~/.beads-planning --routing contributor

# Work
$ bd add "Explore dark mode implementation"
# → Goes to ~/.beads-planning/beads.jsonl
# → Commits to planning repo (git tracked, forensic trail)

$ bd show
# → Shows upstream's canonical issues (read-only)
# → Shows my planning issues (read-write)

$ bd work bd-a3f8e9
$ bd status bd-a3f8e9 in-progress

# Ready to propose
$ bd propose bd-a3f8e9 --target upstream
# → Exports issue from planning repo
# → Creates issue in ./beads/beads.jsonl (staged for PR)
# → Marks as visibility="proposed" in planning repo

$ git add .beads/beads.jsonl
$ git commit -m "Propose: Add dark mode"
$ git push origin feature-branch
# → PR contains only the proposed issue, not all my planning
```

### Use Case 2: Multi-Phase Development

```bash
# Setup phases
$ mkdir -p ~/.beads-work/{architecture,implementation,testing}
$ for dir in ~/.beads-work/*; do (cd $dir && git init && bd init); done

# Configure project
$ cd ~/my-big-project
$ bd config --add-repo ~/.beads-work/architecture
$ bd config --add-repo ~/.beads-work/implementation
$ bd config --add-repo ~/.beads-work/testing

# Architecture phase
$ bd add "Design authentication system" --repo ~/.beads-work/architecture
$ bd show --repo ~/.beads-work/architecture
# → Only architecture issues

# Implementation phase (later)
$ bd add "Implement JWT validation" --repo ~/.beads-work/implementation

# View all phases
$ bd show
# → Shows all issues from all configured repos
```

### Use Case 3: Multiple Contributors on Same Project

```bash
# Team member Alice (maintainer)
$ cd ~/project
$ bd add "Fix bug in parser"
# → Goes to ./beads/beads.jsonl (she's maintainer)
# → Commits to project repo

# Team member Bob (contributor)
$ cd ~/project
$ bd add "Explore performance optimization"
# → Goes to ~/.beads-planning/beads.jsonl (he's contributor)
# → Does NOT pollute project repo

$ bd show
# → Sees Alice's canonical issue
# → Sees his own planning

$ bd propose bd-xyz
# → Proposes to Alice's canonical repo
```

## Implementation Outline

### Phase 1: Core Multi-Repo Support

**Commands**:
```bash
bd config --add-repo <path>         # Add a repo to hydration
bd config --remove-repo <path>      # Remove a repo
bd config --list-repos              # Show all configured repos
bd config --routing <mode>          # Set routing: single|auto|explicit
```

**Config schema**:
```toml
[repos]
primary = "."
additional = ["path1", "path2", ...]

[routing]
default = "path"  # Where `bd add` goes by default
mode = "auto"     # auto | explicit
```

**Database changes**:
- Add `source_repo` field to issues
- Hydration layer reads from multiple JSONLs
- Writes go to correct JSONL based on source_repo

### Phase 2: Proposal Flow

**Commands**:
```bash
bd propose <issue-id> [--target <repo>]   # Move issue to target repo
bd withdraw <issue-id>                    # Un-propose (move back)
bd accept <issue-id>                      # Maintainer accepts proposal
```

**States**:
- `visibility: local` → Personal planning
- `visibility: proposed` → Staged for PR
- `visibility: canonical` → Accepted by upstream

### Phase 3: Routing Rules

**Auto-detection**:
- Detect if user is maintainer (git config, permissions)
- Auto-route to primary vs planning repo

**Config-based routing** (no new schema fields):
```toml
[routing]
mode = "auto"  # auto | explicit
default = "~/.beads-planning"  # Fallback for contributors

# Auto-detection rules
[routing.auto]
maintainer = "."  # If user is maintainer, use primary repo
contributor = "~/.beads-planning"  # Otherwise use planning repo
```

**Explicit routing** via CLI flag:
```bash
# Override auto-detection for specific issues
bd add "Design system" --repo ~/.beads-work/architecture
```

**Discovered issue inheritance**:
- Issues with parent_id automatically inherit parent's source_repo
- Keeps related work co-located

### Phase 4: VCS-Agnostic Sync

**Sync operations**:
- Detect VCS type per repo (git, jj, hg, sl)
- Use appropriate sync commands
- Fall back to manual sync if no VCS

**Example**:
```bash
$ bd sync
# Auto-detects:
# - . is git → runs git pull
# - ~/.beads-planning is jj → runs jj git fetch && jj rebase
# - ~/other is hg → runs hg pull && hg update
```

## Migration Path

### Existing Users (Single Repo)

No changes required. Current workflow continues to work:

```bash
$ bd add "Task"
# → .beads/beads.jsonl (as before)
```

### Opting Into Multi-Repo

```bash
# Create planning repo
$ mkdir ~/.beads-planning && cd ~/.beads-planning
$ git init && bd init

# Link to project
$ cd ~/my-project
$ bd config --add-repo ~/.beads-planning
$ bd config --routing auto  # Auto-detect maintainer vs contributor

# Optionally migrate existing issues
$ bd migrate --move-to ~/.beads-planning --filter "author=me"
```

### Teams Adopting Beads

```bash
# Maintainer sets up project
$ cd ~/team-project
$ bd init
$ git add .beads/ && git commit -m "Initialize beads"

# Contributors clone and configure
$ git clone team-project
$ cd team-project
$ mkdir ~/.beads-planning && cd ~/.beads-planning
$ git init && bd init
$ cd ~/team-project
$ bd config --add-repo ~/.beads-planning --routing contributor
```

## Design Decisions (Resolved)

### 1. Namespace Collisions: **Option B (Global Uniqueness)**

**Decision**: Use globally unique hash-based IDs that include timestamp + random component.

**Rationale** (from VC feedback):
- Option C (allow collisions) breaks dependency references: `bd dep add bd-a3f8e9 bd-b7c2d1` becomes ambiguous
- Need to support cross-repo dependencies without repo-scoped namespacing
- Hash should be: `hash(title + description + timestamp_ms + random_4bytes)`
- Collision probability: ~1 in 10^12 (acceptable)

### 2. Cross-Repo Dependencies: **Yes, Fully Supported**

**Decision**: Dependencies work transparently across all repos.

**Implementation**:
- Hydrated database contains all issues from all repos
- Dependencies stored by ID only (no repo qualifier needed)
- `bd ready` checks dependency graph across all repos
- Writes route back to correct JSONL via `source_repo` metadata

### 3. Routing Mechanism: **Config-Based, No Schema Changes**

**Decision**: Use config-based routing + explicit `--repo` flag. No new schema fields.

**Rationale**:
- `IssueType` already exists and is used semantically (bug, feature, task, epic, chore)
- Labels are used semantically by VC (`discovered:blocker`, `no-auto-claim`)
- Routing is a storage concern, not issue metadata
- Simpler: auto-detect maintainer vs contributor from config
- Discovered issues inherit parent's `source_repo` automatically

### 4. Performance: **Smart Caching with File Mtime Tracking**

**Decision**: SQLite DB is the cache, JSONLs are source of truth.

**Implementation**:
```go
type MultiRepoStorage struct {
    repos      []RepoConfig
    db         *sql.DB
    repoMtimes map[string]time.Time  // Track file modification times
}

func (s *MultiRepoStorage) GetReadyWork(ctx) ([]Issue, error) {
    // Fast path: check if ANY JSONL changed
    needSync := false
    for repo, jsonlPath := range s.jsonlPaths() {
        currentMtime := stat(jsonlPath).ModTime()
        if currentMtime.After(s.repoMtimes[repo]) {
            needSync = true
            s.repoMtimes[repo] = currentMtime
        }
    }

    // Only re-hydrate if something changed
    if needSync {
        s.rehydrate()  // Expensive but rare
    }

    // Query is fast (in-memory SQLite)
    return s.db.Query("SELECT * FROM issues WHERE ...")
}
```

**Rationale**: VC's polling loop (every 5-10 seconds) requires sub-second queries. File stat is microseconds, re-parsing only when needed.

### 5. Visibility Field: **Optional, Backward Compatible**

**Decision**: Add `visibility` as optional field, defaults to "canonical" if missing.

**Schema**:
```go
type Issue struct {
    // ... existing fields ...
    Visibility *string `json:"visibility,omitempty"`  // nil = canonical
}
```

**States**:
- `local`: Personal planning only
- `proposed`: Staged for upstream PR
- `canonical`: Accepted by upstream (or default for existing issues)

**Orthogonality**: Visibility and Status are independent:
- `status: in_progress, visibility: local` → Working on personal planning
- `status: open, visibility: proposed` → Proposed to upstream, awaiting review

### 6. Library API Stability: **Transparent Hydration**

**Decision**: Hybrid approach - transparent by default, explicit opt-in available.

**Backward Compatible**:
```go
// Existing code keeps working - reads config.toml automatically
store, err := beadsLib.NewSQLiteStorage(".beads/vc.db")
```

**Explicit Override**:
```go
// Library consumers can override config
cfg := beadsLib.Config{
    Primary: ".beads/vc.db",
    Additional: []string{"~/.beads-planning"},
}
store, err := beadsLib.NewStorageWithConfig(cfg)
```

### 7. ACID Guarantees: **Per-Repo File Locking**

**Decision**: Use file-based locks per JSONL, atomic within single repo.

**Implementation**:
```go
func (s *Storage) UpdateIssue(issue Issue) error {
    sourceRepo := issue.SourceRepo

    // Lock that repo's JSONL
    lock := flock(sourceRepo + "/beads.jsonl.lock")
    defer lock.Unlock()

    // Read-modify-write
    issues := s.readJSONL(sourceRepo)
    issues.Update(issue)
    s.writeJSONL(sourceRepo, issues)

    // Update in-memory DB
    s.db.Update(issue)
}
```

**Limitation**: Cross-repo transactions are NOT atomic (acceptable, rare use case).

## Key Learnings from VC Feedback

The VC project (VibeCoder) provided critical feedback as a real downstream consumer that uses beads as a library. Key insights:

### 1. Two Consumer Models

Beads has two distinct consumer types:
- **CLI users**: Use `bd` commands directly
- **Library consumers**: Use `beadsLib` in Go/TypeScript/etc. (like VC)

Multi-repo must work transparently for both.

### 2. Performance is Critical for Automation

VC's executor polls `GetReadyWork()` every 5-10 seconds. Multi-repo hydration must:
- Use smart caching (file mtime tracking)
- Avoid re-parsing JSONLs on every query
- Keep queries sub-second (ideally <100ms)

### 3. Special Labels Must Work Across Repos

VC uses semantic labels that must work regardless of repo:
- `discovered:blocker` - Auto-generated blocker issues (priority boost)
- `discovered:related` - Auto-generated related work
- `no-auto-claim` - Prevent executor from claiming
- `baseline-failure` - Self-healing baseline failures

These are **semantic labels**, not routing labels. Don't overload labels for routing.

### 4. Discovered Issues Routing

When VC's analysis phase auto-creates issues with `discovered:blocker` label, they should:
- Inherit parent's `source_repo` automatically
- Stay co-located with related work
- Not require manual routing decisions

### 5. Library API Stability is Non-Negotiable

VC's code uses `beadsLib.NewSQLiteStorage()`. Must not break. Solution:
- Read `.beads/config.toml` automatically (transparent)
- Provide `NewStorageWithConfig()` for explicit override
- Hydration happens at storage layer, invisible to library consumers

## Remaining Open Questions

1. **Sync semantics**: When upstream accepts a proposed issue and modifies it, how to sync back?
   - Option A: Mark as "accepted" in planning repo, keep both copies
   - Option B: Delete from planning repo (it's now canonical)
   - Option C: Keep in planning repo but mark as read-only mirror

2. **Discovery**: How do users learn about this feature?
   - Auto-prompt when detecting fork/contributor status?
   - Docs + examples?
   - `bd init --contributor` wizard?

3. **Metadata fields**: Should `source_repo` be exposed in JSON export, or keep it internal to storage layer?

4. **Proposed issue lifecycle**: What happens to proposed issues after PR is merged/rejected?
   - Auto-delete from planning repo?
   - Mark as "accepted" or "rejected"?
   - Manual cleanup via `bd withdraw`?

## Success Metrics

How we'll know this works:

1. **Zero pollution**: No contributor planning issues accidentally merged upstream
2. **Multi-clone sync**: Workers on different machines see consistent state (via VCS sync)
3. **Flexibility**: Users can configure for their workflow (personas, phases, etc.)
4. **Backwards compat**: Existing single-repo users unaffected
5. **VCS-agnostic**: Works with git, jj, hg, sl, or no VCS

## Next Actions

Suggested epics/issues to create (can be done in follow-up session):

1. **Epic: Multi-repo hydration layer**
   - Design schema for source_repo metadata
   - Implement config parsing for repos.additional
   - Build hydration logic (read from N JSONLs)
   - Build write routing (write to correct JSONL)

2. **Epic: Proposal workflow**
   - Implement `bd propose` command
   - Implement `bd withdraw` command
   - Implement `bd accept` command (maintainer only)
   - Design visibility state machine

3. **Epic: Auto-routing**
   - Detect maintainer vs contributor status
   - Implement routing rules (label, priority, custom)
   - Make `bd add` route to correct repo

4. **Epic: VCS-agnostic sync**
   - Detect VCS type per repo
   - Implement sync adapters (git, jj, hg, sl)
   - Handle mixed-VCS multi-repo configs

5. **Epic: Migration and onboarding**
   - Write migration guide
   - Implement `bd migrate` command
   - Create init wizards for common scenarios
   - Update documentation

---

## Summary and Next Steps

This document represents the design evolution for multi-repo support in beads, driven by:

1. **Original problem** (GitHub #207): Contributors' personal planning pollutes upstream PRs
2. **Core insight**: Beads is a separate communication channel that happens to use VCS
3. **VC feedback**: Real-world library consumer with specific performance and API stability needs

### Final Architecture

**Solution #4 (Separate Repos)** with these refinements:

- **N ≥ 1 repos**: Single repo (N=1) is default, multi-repo is opt-in
- **VCS-agnostic**: Works with git, jj, hg, sapling, or no VCS
- **Config-based routing**: No schema changes, auto-detect maintainer vs contributor
- **Smart caching**: File mtime tracking, SQLite DB as cache layer
- **Transparent hydration**: Library API remains stable, config-driven
- **Global namespace**: Hash-based IDs with timestamp + random for uniqueness
- **Cross-repo dependencies**: Fully supported, transparent to users
- **Discovered issues**: Inherit parent's source_repo automatically

### Why This Design Wins

1. **Zero PR pollution**: Separate git histories = impossible to accidentally merge planning
2. **Git ledger preserved**: All repos are VCS-tracked, full forensic capability
3. **Maximum flexibility**: Supports OSS contributors, multi-phase dev, multi-persona workflows
4. **Backward compatible**: Existing single-repo users unchanged
5. **Performance**: Sub-second queries even with polling loops
6. **Library-friendly**: Transparent to downstream consumers like VC

### Related Documents

- Original issue: GitHub #207
- VC feedback: `./vc-feedback-on-multi-repo.md`
- Implementation tracking: TBD (epics to be created)

### Status

**Design**: ✅ Complete (pending resolution of open questions)
**Implementation**: ⏳ Not started
**Target**: TBD

Last updated: 2025-11-03
