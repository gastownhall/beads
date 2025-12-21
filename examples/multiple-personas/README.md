# Multiple Personas Workflow Example

This example demonstrates how to use beads when different roles work on the same project (architect, implementer, reviewer, etc.).

## Problem

Complex projects involve different personas with different concerns:
- **Architect:** System design, technical decisions, high-level planning
- **Implementer:** Write code, fix bugs, implement features
- **Reviewer:** Code review, quality gates, testing
- **Product:** Requirements, priorities, user stories

Each persona needs:
- Different views of the same work
- Clear handoffs between roles
- Track discovered work in context

## Solution

Use beads labels, priorities, and dependencies to organize work by persona, with clear ownership and handoffs.

## Setup

```bash
# Initialize beads
cd my-project
bd init

# Start daemon for auto-sync (optional for teams)
bd daemon --start --auto-commit --auto-push
```

## Persona: Architect

The architect creates high-level design and makes technical decisions.

### Create Architecture Epic

```bash
# Main epic
bd create "Design new caching layer" -t epic -p 1
# Returns: bd-a1b2c3

# Add architecture label
bd label add bd-a1b2c3 architecture

# Architecture tasks
bd create "Research caching strategies (Redis vs Memcached)" -p 1 \
  --deps discovered-from:bd-a1b2c3
bd label add bd-xyz architecture

bd create "Write ADR: Caching layer design" -p 1 \
  --deps discovered-from:bd-a1b2c3
bd label add bd-abc architecture

bd create "Design cache invalidation strategy" -p 1 \
  --deps discovered-from:bd-a1b2c3
bd label add bd-def architecture
```

### View Architect Work

```bash
# See only architecture issues
bd list --label architecture

# See architecture issues that are ready
bd list --label architecture --status open | grep -v blocked

# High-priority architecture decisions
bd list --label architecture --priority 0
bd list --label architecture --priority 1
```

### Handoff to Implementer

When design is complete, create implementation tasks:

```bash
# Close architecture tasks
bd close bd-xyz --reason "Decided on Redis with write-through"
bd close bd-abc --reason "ADR-007 published"

# Create implementation tasks with labels
bd create "Implement Redis connection pool" -p 1 \
  --deps discovered-from:bd-a1b2c3
bd label add bd-impl1 implementation

bd create "Add cache middleware to API routes" -p 1 \
  --deps discovered-from:bd-a1b2c3
bd label add bd-impl2 implementation

# Link implementation to architecture
bd dep add bd-impl1 bd-abc --type related  # Based on ADR
bd dep add bd-impl2 bd-abc --type related
```

## Persona: Implementer

The implementer writes code based on architecture decisions.

### View Implementation Work

```bash
# See only implementation tasks
bd list --label implementation --status open

# See what's ready to implement
bd ready | grep implementation

# High-priority bugs to fix
bd list --label implementation --type bug --priority 0
bd list --label implementation --type bug --priority 1
```

### Claim and Implement

```bash
# Claim a task
bd update bd-impl1 --status in_progress

# During implementation, discover issues
bd create "Need connection retry logic" -t bug -p 1 \
  --deps discovered-from:bd-impl1
bd label add bd-bug1 implementation bug

bd create "Add metrics for cache hit rate" -p 2 \
  --deps discovered-from:bd-impl1
bd label add bd-metric1 implementation observability

# Complete implementation
bd close bd-impl1 --reason "Redis pool working, tested locally"
```

### Handoff to Reviewer

```bash
# Mark ready for review
bd create "Code review: Redis caching layer" -p 1
bd label add bd-review1 review

# Link to implementation
bd dep add bd-review1 bd-impl1 --type related
bd dep add bd-review1 bd-impl2 --type related
```

## Persona: Reviewer

The reviewer checks code quality, tests, and approvals.

### View Review Work

```bash
# See all review tasks
bd list --label review --status open

# See what's ready for review
bd ready | grep review

# High-priority reviews
bd list --label review --priority 0
bd list --label review --priority 1
```

### Perform Review

```bash
# Claim review
bd update bd-review1 --status in_progress

# Found issues during review
bd create "Add unit tests for retry logic" -t task -p 1 \
  --deps discovered-from:bd-review1
bd label add bd-test1 implementation testing

bd create "Fix: connection leak on timeout" -t bug -p 0 \
  --deps discovered-from:bd-review1
bd label add bd-bug2 implementation bug critical

bd create "Document Redis config options" -p 2 \
  --deps discovered-from:bd-review1
bd label add bd-doc1 documentation

# Block review until issues fixed
bd dep add bd-review1 bd-test1 --type blocks
bd dep add bd-review1 bd-bug2 --type blocks
```

### Approve or Request Changes

```bash
# After fixes, approve
bd close bd-review1 --reason "LGTM, all tests pass"

# Or request changes
bd update bd-review1 --status blocked
# (blockers will show up in dependency tree)
```

## Persona: Product Owner

The product owner manages priorities and requirements.

### View Product Work

```bash
# See all features
bd list --type feature

# See high-priority work
bd list --priority 0
bd list --priority 1

# See what's in progress
bd list --status in_progress

# See what's blocked
bd list --status blocked
```

### Prioritize Work

```bash
# Bump priority based on customer feedback
bd update bd-impl2 --priority 0

# Lower priority for nice-to-haves
bd update bd-metric1 --priority 3

# Add product label to track customer-facing work
bd label add bd-impl2 customer-facing
```

### Create User Stories

```bash
# User story
bd create "As a user, I want faster page loads" -t feature -p 1
bd label add bd-story1 user-story customer-facing

# Link technical work to user story
bd dep add bd-impl1 bd-story1 --type related
bd dep add bd-impl2 bd-story1 --type related
```

## Multi-Persona Workflow Example

### Week 1: Architecture Phase

**Architect:**

```bash
# Create epic
bd create "Implement rate limiting" -t epic -p 1  # bd-epic1
bd label add bd-epic1 architecture

# Research
bd create "Research rate limiting algorithms" -p 1 \
  --deps discovered-from:bd-epic1
bd label add bd-research1 architecture research

bd update bd-research1 --status in_progress
# ... research done ...
bd close bd-research1 --reason "Chose token bucket algorithm"

# Design
bd create "Write ADR: Rate limiting design" -p 1 \
  --deps discovered-from:bd-epic1
bd label add bd-adr1 architecture documentation

bd close bd-adr1 --reason "ADR-012 approved"
```

### Week 2: Implementation Phase

**Implementer:**

```bash
# See what's ready to implement
bd ready | grep implementation

# Create implementation tasks based on architecture
bd create "Implement token bucket algorithm" -p 1 \
  --deps discovered-from:bd-epic1
bd label add bd-impl1 implementation
bd dep add bd-impl1 bd-adr1 --type related

bd create "Add rate limit middleware" -p 1 \
  --deps discovered-from:bd-epic1
bd label add bd-impl2 implementation

# Claim and start
bd update bd-impl1 --status in_progress

# Discover issues
bd create "Need distributed rate limiting (Redis)" -t bug -p 1 \
  --deps discovered-from:bd-impl1
bd label add bd-bug1 implementation bug
```

**Architect (consulted):**

```bash
# Architect reviews discovered issue
bd show bd-bug1
bd update bd-bug1 --priority 0  # Escalate to critical
bd label add bd-bug1 architecture  # Architect will handle

# Make decision
bd create "Design: Distributed rate limiting" -p 0 \
  --deps discovered-from:bd-bug1
bd label add bd-design1 architecture

bd close bd-design1 --reason "Use Redis with sliding window"
```

**Implementer (continues):**

```bash
# Implement based on architecture decision
bd create "Add Redis sliding window for rate limits" -p 0 \
  --deps discovered-from:bd-design1
bd label add bd-impl3 implementation

bd close bd-impl1 --reason "Token bucket working"
bd close bd-impl3 --reason "Redis rate limiting working"
```

### Week 3: Review Phase

**Reviewer:**

```bash
# See what's ready for review
bd list --label review

# Create review task
bd create "Code review: Rate limiting" -p 1
bd label add bd-review1 review
bd dep add bd-review1 bd-impl1 --type related
bd dep add bd-review1 bd-impl3 --type related

bd update bd-review1 --status in_progress

# Found issues
bd create "Add integration tests for Redis" -t task -p 1 \
  --deps discovered-from:bd-review1
bd label add bd-test1 testing implementation

bd create "Missing error handling for Redis down" -t bug -p 0 \
  --deps discovered-from:bd-review1
bd label add bd-bug2 implementation bug critical

# Block review
bd dep add bd-review1 bd-test1 --type blocks
bd dep add bd-review1 bd-bug2 --type blocks
```

**Implementer (fixes):**

```bash
# Fix review findings
bd update bd-bug2 --status in_progress
bd close bd-bug2 --reason "Added circuit breaker for Redis"

bd update bd-test1 --status in_progress
bd close bd-test1 --reason "Integration tests passing"
```

**Reviewer (approves):**

```bash
# Review unblocked
bd close bd-review1 --reason "Approved, merging PR"
```

**Product Owner (closes epic):**

```bash
# Feature shipped!
bd close bd-epic1 --reason "Rate limiting in production"
```

## Label Organization

### Recommended Labels

```bash
# Role labels
architecture, implementation, review, product

# Type labels
bug, feature, task, chore, documentation

# Status labels
critical, blocked, waiting-feedback, needs-design

# Domain labels
frontend, backend, infrastructure, database

# Quality labels
testing, security, performance, accessibility

# Customer labels
customer-facing, user-story, feedback
```

### View by Label Combination

```bash
# Critical bugs for implementers
bd list --label implementation --label bug --label critical

# Architecture issues needing review
bd list --label architecture --label review

# Customer-facing features
bd list --label customer-facing --type feature

# Backend implementation work
bd list --label backend --label implementation --status open
```

## Filtering by Persona

### Architect View

```bash
# My work
bd list --label architecture --status open

# Design decisions to make
bd list --label architecture --label needs-design

# High-priority architecture
bd list --label architecture --priority 0
bd list --label architecture --priority 1
```

### Implementer View

```bash
# My work
bd list --label implementation --status open

# Ready to implement
bd ready | grep implementation

# Bugs to fix
bd list --label implementation --type bug --priority 0
bd list --label implementation --type bug --priority 1

# Blocked work
bd list --label implementation --status blocked
```

### Reviewer View

```bash
# Reviews waiting
bd list --label review --status open

# Critical reviews
bd list --label review --priority 0

# Blocked reviews
bd list --label review --status blocked
```

### Product Owner View

```bash
# All customer-facing work
bd list --label customer-facing

# Features in progress
bd list --type feature --status in_progress

# Blocked work (needs attention)
bd list --status blocked

# High-priority items across all personas
bd list --priority 0
```

## Handoff Patterns

### Architecture → Implementation

```bash
# Architect creates spec
bd create "Design: New payment API" -p 1
bd label add bd-design1 architecture documentation

# When done, create implementation tasks
bd create "Implement Stripe integration" -p 1
bd label add bd-impl1 implementation
bd dep add bd-impl1 bd-design1 --type related

bd close bd-design1 --reason "Spec complete, ready for implementation"
```

### Implementation → Review

```bash
# Implementer finishes
bd close bd-impl1 --reason "Stripe working, PR ready"

# Create review task
bd create "Code review: Stripe integration" -p 1
bd label add bd-review1 review
bd dep add bd-review1 bd-impl1 --type related
```

### Review → Product

```bash
# Reviewer approves
bd close bd-review1 --reason "Approved, deployed to staging"

# Product tests in staging
bd create "UAT: Test Stripe in staging" -p 1
bd label add bd-uat1 product testing
bd dep add bd-uat1 bd-review1 --type related

# Product approves for production
bd close bd-uat1 --reason "UAT passed, deploying to prod"
```

## Best Practices

### 1. Use Labels Consistently

```bash
# Good: Clear role separation
bd label add bd-123 architecture
bd label add bd-456 implementation
bd label add bd-789 review

# Bad: Mixing concerns
# (same issue shouldn't be both architecture and implementation)
```

### 2. Link Related Work

```bash
# Always link implementation to architecture
bd dep add bd-impl bd-arch --type related

# Link bugs to features
bd dep add bd-bug bd-feature --type discovered-from
```

### 3. Clear Handoffs

```bash
# Document why closing
bd close bd-arch --reason "Design complete, created bd-impl1 and bd-impl2 for implementation"

# Not: "done" (too vague)
```

### 4. Escalate When Needed

```bash
# Implementer discovers architectural issue
bd create "Current design doesn't handle edge case X" -t bug -p 0
bd label add bd-issue architecture  # Tag for architect
bd label add bd-issue needs-design  # Flag as needing design
```

### 5. Regular Syncs

```bash
# Daily: Each persona checks their work
bd list --label architecture --status open  # Architect
bd list --label implementation --status open  # Implementer
bd list --label review --status open  # Reviewer

# Weekly: Team reviews together
bd stats  # Overall progress
bd list --status blocked  # What's stuck?
bd ready  # What's ready to work on?
```

## Common Patterns

### Spike Then Implement

```bash
# Architect creates research spike
bd create "Spike: Evaluate GraphQL vs REST" -p 1
bd label add bd-spike1 architecture research

bd close bd-spike1 --reason "Chose GraphQL, created implementation tasks"

# Implementation follows
bd create "Implement GraphQL API" -p 1
bd label add bd-impl1 implementation
bd dep add bd-impl1 bd-spike1 --type related
```

### Bug Triage

```bash
# Bug reported
bd create "App crashes on large files" -t bug -p 1

# Implementer investigates
bd update bd-bug1 --label implementation
bd update bd-bug1 --status in_progress

# Discovers architectural issue
bd create "Need streaming uploads, not buffering" -t bug -p 0
bd label add bd-arch1 architecture
bd dep add bd-arch1 bd-bug1 --type discovered-from

# Architect designs solution
bd update bd-arch1 --label architecture
bd close bd-arch1 --reason "Designed streaming upload flow"

# Implementer fixes
bd update bd-bug1 --status in_progress
bd close bd-bug1 --reason "Implemented streaming uploads"
```

### Feature Development

```bash
# Product creates user story
bd create "Users want bulk import" -t feature -p 1
bd label add bd-story1 user-story product

# Architect designs
bd create "Design: Bulk import system" -p 1
bd label add bd-design1 architecture
bd dep add bd-design1 bd-story1 --type related

# Implementation tasks
bd create "Implement CSV parser" -p 1
bd label add bd-impl1 implementation
bd dep add bd-impl1 bd-design1 --type related

bd create "Implement batch processor" -p 1
bd label add bd-impl2 implementation
bd dep add bd-impl2 bd-design1 --type related

# Review
bd create "Code review: Bulk import" -p 1
bd label add bd-review1 review
bd dep add bd-review1 bd-impl1 --type blocks
bd dep add bd-review1 bd-impl2 --type blocks

# Product UAT
bd create "UAT: Bulk import" -p 1
bd label add bd-uat1 product testing
bd dep add bd-uat1 bd-review1 --type blocks
```

## See Also

- [Multi-Phase Development](../multi-phase-development/) - Organize work by phase
- [Team Workflow](../team-workflow/) - Collaborate across personas
- [Contributor Workflow](../contributor-workflow/) - External contributions
- [Labels Documentation](../../docs/LABELS.md) - Label management guide
