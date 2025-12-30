# Labels in Beads

Labels provide flexible, multi-dimensional categorization for issues beyond the structured fields (status, priority, type). Use labels for cross-cutting concerns, technical metadata, and contextual tagging without schema changes.

## Design Philosophy

**When to use labels vs. structured fields:**

- **Structured fields** (status, priority, type) → Core workflow state
  - Status: Where the issue is in the workflow (`open`, `in_progress`, `blocked`, `closed`)
  - Priority: How urgent (0-4)
  - Type: What kind of work (`bug`, `feature`, `task`, `epic`, `chore`)

- **Labels** → Everything else
  - Technical metadata (`backend`, `frontend`, `api`, `database`)
  - Domain/scope (`auth`, `payments`, `search`, `analytics`)
  - Effort estimates (`small`, `medium`, `large`)
  - Quality gates (`needs-review`, `needs-tests`, `breaking-change`)
  - Team/ownership (`team-infra`, `team-product`)
  - Release tracking (`v1.0`, `v2.0`, `backport-candidate`)

## Quick Start

```bash
# Add labels when creating issues
bd create "Fix auth bug" -t bug -p 1 -l auth,backend,urgent

# Add labels to existing issues
bd label add bd-42 security
bd label add bd-42 breaking-change

# List issue labels
bd label list bd-42

# Remove a label
bd label remove bd-42 urgent

# List all labels in use
bd label list-all

# Filter by labels (AND - must have ALL)
bd list --label backend,auth

# Filter by labels (OR - must have AT LEAST ONE)
bd list --label-any frontend,backend

# Combine filters
bd list --status open --priority 1 --label security
```

## Common Label Patterns

### 1. Technical Component Labels

Identify which part of the system:
```bash
backend
frontend
api
database
infrastructure
cli
ui
mobile
```

**Example:**
```bash
bd create "Add GraphQL endpoint" -t feature -p 2 -l backend,api
bd create "Update login form" -t task -p 2 -l frontend,auth,ui
```

### 2. Domain/Feature Area

Group by business domain:
```bash
auth
payments
search
analytics
billing
notifications
reporting
admin
```

**Example:**
```bash
bd list --label payments --status open  # All open payment issues
bd list --label-any auth,security       # Security-related work
```

### 3. Size/Effort Estimates

Quick effort indicators:
```bash
small     # < 1 day
medium    # 1-3 days
large     # > 3 days
```

**Example:**
```bash
# Find small quick wins
bd ready --json | jq '.[] | select(.labels[] == "small")'
```

### 4. Quality Gates

Track what's needed before closing:
```bash
needs-review
needs-tests
needs-docs
breaking-change
```

**Example:**
```bash
bd label add bd-42 needs-review
bd list --label needs-review --status in_progress
```

### 5. Release Management

Track release targeting:
```bash
v1.0
v2.0
backport-candidate
release-blocker
```

**Example:**
```bash
bd list --label v1.0 --status open    # What's left for v1.0?
bd label add bd-42 release-blocker
```

### 6. Team/Ownership

Indicate ownership or interest:
```bash
team-infra
team-product
team-mobile
needs-triage
help-wanted
```

**Example:**
```bash
bd list --assignee alice --label team-infra
bd create "Memory leak in cache" -t bug -p 1 -l team-infra,help-wanted
```

### 7. Special Markers

Process or workflow flags:
```bash
auto-generated     # Created by automation
discovered-from    # Found during other work (also a dep type)
technical-debt
good-first-issue
duplicate
wontfix
```

**Example:**
```bash
bd create "TODO: Refactor parser" -t chore -p 3 -l technical-debt,auto-generated
```

## Filtering by Labels

### AND Filtering (--label)
All specified labels must be present:

```bash
# Issues that are BOTH backend AND urgent
bd list --label backend,urgent

# Open bugs that need review AND tests
bd list --status open --type bug --label needs-review,needs-tests
```

### OR Filtering (--label-any)
At least one specified label must be present:

```bash
# Issues in frontend OR backend
bd list --label-any frontend,backend

# Security or auth related
bd list --label-any security,auth
```

### Combining AND/OR
Mix both filters for complex queries:

```bash
# Backend issues that are EITHER urgent OR a blocker
bd list --label backend --label-any urgent,release-blocker

# Frontend work that needs BOTH review and tests, but in any component
bd list --label needs-review,needs-tests --label-any frontend,ui,mobile
```

## Workflow Examples

### Triage Workflow
```bash
# Create untriaged issue
bd create "Crash on login" -t bug -p 1 -l needs-triage

# During triage, add context
bd label add bd-42 auth
bd label add bd-42 backend
bd label add bd-42 urgent
bd label remove bd-42 needs-triage

# Find untriaged issues
bd list --label needs-triage
```

### Quality Gate Workflow
```bash
# Start work
bd update bd-42 --status in_progress

# Mark quality requirements
bd label add bd-42 needs-tests
bd label add bd-42 needs-docs

# Before closing, verify
bd label list bd-42
# ... write tests and docs ...
bd label remove bd-42 needs-tests
bd label remove bd-42 needs-docs

# Close when gates satisfied
bd close bd-42
```

### Release Planning
```bash
# Tag issues for v1.0
bd label add bd-42 v1.0
bd label add bd-43 v1.0
bd label add bd-44 v1.0

# Track v1.0 progress
bd list --label v1.0 --status closed    # Done
bd list --label v1.0 --status open      # Remaining
bd stats  # Overall progress

# Mark critical items
bd label add bd-45 v1.0
bd label add bd-45 release-blocker
```

### Component-Based Work Distribution
```bash
# Backend team picks up work
bd ready --json | jq '.[] | select(.labels[]? == "backend")'

# Frontend team finds small tasks
bd list --status open --label frontend,small

# Find help-wanted items for new contributors
bd list --label help-wanted,good-first-issue
```

## Label Management

### Listing Labels
```bash
# Labels on a specific issue
bd label list bd-42

# All labels in database with usage counts
bd label list-all

# JSON output for scripting
bd label list-all --json
```

Output:
```json
[
  {"label": "auth", "count": 5},
  {"label": "backend", "count": 12},
  {"label": "frontend", "count": 8}
]
```

### Bulk Operations

Add labels in batch during creation:
```bash
bd create "Issue" -l label1,label2,label3
```

Script to add label to multiple issues:
```bash
# Add "needs-review" to all in_progress issues
bd list --status in_progress --json | jq -r '.[].id' | while read id; do
  bd label add "$id" needs-review
done
```

Remove label from multiple issues:
```bash
# Remove "urgent" from closed issues
bd list --status closed --label urgent --json | jq -r '.[].id' | while read id; do
  bd label remove "$id" urgent
done
```

## Integration with Git Workflow

Labels are automatically synced to `.beads/issues.jsonl` along with all issue data:

```bash
# Make changes
bd create "Fix bug" -l backend,urgent
bd label add bd-42 needs-review

# Auto-exported after 5 seconds (or use git hooks for immediate export)
git add .beads/issues.jsonl
git commit -m "Add backend issue"

# After git pull, labels are auto-imported
git pull
bd list --label backend  # Fresh data including labels
```

## Markdown Import/Export

Labels are preserved when importing from markdown:

```markdown
# Fix Authentication Bug

### Type
bug

### Priority
1

### Labels
auth, backend, urgent, needs-review

### Description
Users can't log in after recent deployment.
```

```bash
bd create -f issue.md
# Creates issue with all four labels
```

## Best Practices

### 1. Establish Conventions Early
Document your team's label taxonomy:
```bash
# Add to project README or CONTRIBUTING.md
- Use lowercase, hyphen-separated (e.g., `good-first-issue`)
- Prefix team labels (e.g., `team-infra`, `team-product`)
- Use consistent size labels (`small`, `medium`, `large`)
```

### 2. Don't Overuse Labels
Labels are flexible, but too many can cause confusion. Prefer:
- 5-10 core technical labels (`backend`, `frontend`, `api`, etc.)
- 3-5 domain labels per project
- Standard process labels (`needs-review`, `needs-tests`)
- Release labels as needed

### 3. Clean Up Unused Labels
Periodically review:
```bash
bd label list-all
# Remove obsolete labels from issues
```

### 4. Use Labels for Filtering, Not Search
Labels are for categorization, not free-text search:
- ✅ Good: `backend`, `auth`, `urgent`
- ❌ Bad: `fix-the-login-bug`, `john-asked-for-this`

### 5. Combine with Dependencies
Labels + dependencies = powerful organization:
```bash
# Epic with labeled subtasks
bd create "Auth system rewrite" -t epic -p 1 -l auth,v2.0
bd create "Implement JWT" -t task -p 1 -l auth,backend --deps parent-child:bd-42
bd create "Update login UI" -t task -p 1 -l auth,frontend --deps parent-child:bd-42

# Find all v2.0 auth work
bd list --label auth,v2.0
```

## AI Agent Usage

Labels are especially useful for AI agents managing complex workflows:

```bash
# Auto-label discovered work
bd create "Found TODO in auth.go" -t task -p 2 -l auto-generated,technical-debt

# Filter for agent review
bd list --label needs-review --status in_progress --json

# Track automation metadata
bd label add bd-42 ai-generated
bd label add bd-42 needs-human-review
```

Example agent workflow:
```bash
# Agent discovers issues during refactor
bd create "Extract validateToken function" -t chore -p 2 \
  -l technical-debt,backend,auth,small \
  --deps discovered-from:bd-10

# Agent marks work for review
bd update bd-42 --status in_progress
# ... agent does work ...
bd label add bd-42 needs-review
bd label add bd-42 ai-generated

# Human reviews and approves
bd label remove bd-42 needs-review
bd label add bd-42 approved
bd close bd-42
```

## Labels as State Cache

Labels can cache operational state for fast queries, enabling patterns where beads track both immutable history (events) and current state (labels).

### The Pattern

**Convention:** `<dimension>:<value>`

Examples:
- `patrol:muted` / `patrol:active` - patrol suppression state
- `mode:degraded` / `mode:normal` - operational mode
- `status:idle` / `status:working` - worker status
- `health:healthy` / `health:failing` - component health

**Implementation:**
1. Create an event bead (full context, immutable history)
2. Update the role bead's labels (current state cache)

```bash
# Event: Full record of what happened and why
bd create "Muted patrol: user requested during debugging" -t event \
  -l event-type:patrol-muted,actor:witness,reason:user-request

# State: Update the role bead's label to reflect current state
bd label remove beads/witness patrol:active
bd label add beads/witness patrol:muted
```

**Key principle:** Events are the source of truth. Labels are a cache for fast queries.

### Why This Pattern?

**Fast queries without event scanning:**
```bash
# Without labels-as-state: scan all events to find current patrol state
bd list --type event | grep "patrol" | tail -1  # Slow, fragile

# With labels-as-state: direct query
bd show beads/witness | grep "patrol:"  # Instant
```

**History preserved:**
```bash
# When was patrol muted? Why? Who did it?
bd list --label event-type:patrol-muted --type event
```

**State recovery:**
```bash
# If labels get corrupted, rebuild from events
bd list --type event --label event-type:patrol-muted | tail -1
# Then re-apply the label
```

### Common State Dimensions

| Dimension | Values | Use Case |
|-----------|--------|----------|
| `patrol:` | `active`, `muted` | Patrol cycle suppression |
| `mode:` | `normal`, `degraded`, `maintenance` | Operational mode |
| `status:` | `idle`, `working`, `blocked` | Worker activity |
| `health:` | `healthy`, `warning`, `failing` | Component health |
| `lock:` | `unlocked`, `locked` | Exclusive access control |

### State Transitions

Always create an event before changing state labels:

```bash
# Function to transition state with audit trail
transition_state() {
  local role="$1"
  local dimension="$2"
  local old_value="$3"
  local new_value="$4"
  local reason="$5"

  # Record the transition
  bd create "State change: $dimension $old_value → $new_value" -t event \
    -l "event-type:state-change,dimension:$dimension,from:$old_value,to:$new_value"

  # Update the cache
  bd label remove "$role" "$dimension:$old_value"
  bd label add "$role" "$dimension:$new_value"
}

# Usage
transition_state beads/witness patrol active muted "User debugging session"
```

### Querying State

```bash
# Current state of a role
bd label list beads/witness | grep ":"

# All roles in a specific state
bd list --label patrol:muted

# Roles NOT in expected state
bd list --label-any mode:degraded,health:failing

# History of state changes
bd list --type event --label event-type:state-change
```

### Best Practices

1. **Use namespaced dimensions** - Prefix with role type if ambiguous
2. **Keep value sets small** - 2-4 values per dimension
3. **Document valid values** - List allowed values in role docs
4. **Always create events first** - Never update labels without history
5. **Treat labels as ephemeral** - Rebuild from events if corrupted

### Future Helpers

The pattern suggests helper commands (see bd-7l67):
```bash
# Query current state
bd state beads/witness patrol     # → "muted"

# Transition with automatic event creation
bd set-state beads/witness patrol=active --reason "Debugging complete"
```

Until helpers exist, use the manual pattern above.

## Advanced Patterns

### Component Matrix
Track issues across multiple dimensions:
```bash
# Backend + auth + high priority
bd list --label backend,auth --priority 1

# Any frontend work that's small
bd list --label-any frontend,ui --label small

# Critical issues across all components
bd list --priority 0 --label-any backend,frontend,infrastructure
```

### Sprint Planning
```bash
# Label issues for sprint
for id in bd-42 bd-43 bd-44 bd-45; do
  bd label add "$id" sprint-12
done

# Track sprint progress
bd list --label sprint-12 --status closed    # Velocity
bd list --label sprint-12 --status open      # Remaining
bd stats | grep "In Progress"                # Current WIP
```

### Technical Debt Tracking
```bash
# Mark debt
bd create "Refactor legacy parser" -t chore -p 3 -l technical-debt,large

# Find debt to tackle
bd list --label technical-debt --label small
bd list --label technical-debt --priority 1  # High-priority debt
```

### Breaking Change Coordination
```bash
# Identify breaking changes
bd label add bd-42 breaking-change
bd label add bd-42 v2.0

# Find all breaking changes for next major release
bd list --label breaking-change,v2.0

# Ensure they're documented
bd list --label breaking-change --label needs-docs
```

## Operational State Pattern (Labels as Cache)

For orchestration systems like Gas Town, labels can cache the current operational state of "role beads" (issues representing agents or system components). This enables fast state queries without scanning event history.

### Convention: `<dimension>:<value>`

Use colon-separated labels with a dimension prefix and value suffix:

```
patrol:muted      patrol:active
mode:degraded     mode:normal
status:idle       status:working
health:healthy    health:failing
```

### The Pattern

1. **Create an event bead** with full context (immutable, audit trail)
2. **Update the role bead's labels** to reflect current state (fast lookup)

```bash
# 1. Record the event (source of truth)
bd create "Muted patrol for witness-abc" -t event \
  --parent witness-abc \
  -d "Reason: investigating stuck polecat. Expected duration: 30m"

# 2. Update the cached state label
bd label remove witness-abc patrol:active
bd label add witness-abc patrol:muted
```

### Why This Pattern?

**Events are source of truth. Labels are cache.**

| Approach | Events Only | Labels as Cache |
|----------|-------------|-----------------|
| Query current state | Scan all events, find latest | `bd list --label patrol:muted` |
| Query state history | Natural (all events exist) | Query events |
| Audit trail | Complete | Complete (events still exist) |
| Performance | O(n) events | O(1) label lookup |

The pattern gives you both: complete history via events, fast queries via labels.

### Example: Agent Role States

```bash
# Create a role bead for an agent
bd create "witness-alpha" -t role -l patrol:active,mode:normal,health:healthy

# Agent enters degraded mode
bd create "Degraded: high error rate" -t event --parent witness-alpha \
  -d "Error rate exceeded 5%. Reducing poll frequency."
bd label remove witness-alpha mode:normal
bd label add witness-alpha mode:degraded

# Query current state
bd list --label mode:degraded --type role  # All degraded roles

# Agent recovers
bd create "Recovered: error rate normal" -t event --parent witness-alpha
bd label remove witness-alpha mode:degraded
bd label add witness-alpha mode:normal
```

### Common Dimensions

| Dimension | Values | Use Case |
|-----------|--------|----------|
| `patrol` | `active`, `muted`, `suspended` | Agent patrol cycles |
| `mode` | `normal`, `degraded`, `maintenance` | Operational modes |
| `status` | `idle`, `working`, `blocked` | Work state |
| `health` | `healthy`, `warning`, `failing` | Health checks |
| `sync` | `current`, `stale`, `syncing` | Sync state |

### Best Practices

1. **Always create the event first** - Labels are cache; events are truth
2. **Remove old value before adding new** - Prevents dimension:value1 + dimension:value2 conflicts
3. **Use consistent dimension names** - Establish team conventions early
4. **Keep dimensions orthogonal** - patrol and mode are independent concerns

### Querying State

```bash
# Find all muted patrols
bd list --label patrol:muted

# Find healthy agents in normal mode
bd list --label health:healthy,mode:normal

# Find any non-healthy agents
bd list --label-any health:warning,health:failing

# Get state for a specific role
bd label list witness-alpha
# Output: patrol:active, mode:normal, health:healthy
```

### Future: Helper Commands

For convenience, these helpers are planned:

```bash
# Query a specific dimension
bd state witness-alpha patrol
# Output: active

# Set state (creates event + updates label atomically)
bd set-state witness-alpha patrol=muted --reason "Investigating issue"
```

Until then, use the manual event + label pattern above.

## Troubleshooting

### Labels Not Showing in List
Labels require explicit fetching. The `bd list` command shows issues but not labels in human output (only in JSON).

```bash
# See labels in JSON
bd list --json | jq '.[] | {id, labels}'

# See labels for specific issue
bd show bd-42 --json | jq '.labels'
bd label list bd-42
```

### Label Filtering Not Working
Check label names for exact matches (case-sensitive):
```bash
# These are different labels:
bd label add bd-42 Backend    # Capital B
bd list --label backend       # Won't match

# List all labels to see exact names
bd label list-all
```

### Syncing Labels with Git
Labels are included in `.beads/issues.jsonl` export. If labels seem out of sync:
```bash
# Force export
bd export -o .beads/issues.jsonl

# After pull, force import
bd import -i .beads/issues.jsonl
```

## See Also

- [README.md](../README.md) - Main documentation
- [AGENTS.md](../AGENTS.md) - AI agent integration guide
- [ADVANCED.md](ADVANCED.md) - JSONL format details
