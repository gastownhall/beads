# Agent Memory Patterns

This document describes conventions for AI agents to maintain context and track outcomes using beads' built-in features.

## Overview

Rather than creating separate files for agent memory, use beads' existing comment system with structured prefixes. This keeps all information attached to issues where it belongs, and ensures data travels with issues through sync, export, and compaction.

## Context Comments

Use `[context]` prefix to record discoveries, decisions, and learnings during issue work:

```bash
# Record a discovery
bd comments add bd-123 "[context] API uses OAuth2, not API keys as initially assumed"

# Record a decision
bd comments add bd-123 "[context] decision: Using retry with exponential backoff for rate limits"

# Record a finding
bd comments add bd-123 "[context] The config file is at ~/.config/app/settings.json, not /etc/app/"

# Record an error and fix
bd comments add bd-123 "[context] error: Connection timeout on first attempt - fixed by increasing timeout to 30s"
```

### Context Categories

For richer categorization, use sub-prefixes:

| Prefix | Use Case |
|--------|----------|
| `[context]` | General learnings |
| `[context] decision:` | Architectural or implementation decisions |
| `[context] error:` | Errors encountered and how they were resolved |
| `[context] finding:` | Discoveries about the codebase or system |
| `[context] blocker:` | What blocked progress and how it was resolved |

## Outcome Comments

When closing an issue, add an `[outcome]` comment to track success patterns:

```bash
# Record outcome when closing
bd comments add bd-123 "[outcome] success:true approach:incremental duration:45min"
bd close bd-123 --reason="Implemented feature with incremental approach"

# For failures
bd comments add bd-123 "[outcome] success:false reason:blocked-by-external-api"
bd close bd-123 --reason="Blocked by third-party API limitation"
```

### Outcome Fields

| Field | Values | Description |
|-------|--------|-------------|
| `success` | true/false | Whether the issue was completed successfully |
| `approach` | incremental, rewrite, workaround, etc. | How the problem was solved |
| `duration` | Xmin, Xhr | Approximate time spent |
| `reason` | free text | Why it failed (for failures) |
| `complexity` | low, medium, high | Actual vs expected complexity |

## Querying Context and Outcomes

Use `bd comments` to retrieve context for an issue:

```bash
# View all comments on an issue
bd comments bd-123

# JSON output for parsing
bd comments bd-123 --json
```

To find patterns across issues, export and grep:

```bash
# Find all context comments
bd export | grep '\[context\]'

# Find all outcomes
bd export | grep '\[outcome\]'

# Find all decisions
bd export | grep '\[context\] decision:'
```

## Best Practices

1. **Add context as you work** - Don't wait until the end; record discoveries immediately
2. **Be specific** - Include file paths, function names, error messages
3. **Record the why** - Future agents benefit from understanding reasoning
4. **Add outcomes when closing** - This builds a knowledge base of what works
5. **Use consistent prefixes** - Makes searching and aggregation easier

## Example Workflow

```bash
# Starting work on an issue
bd update bd-123 --status in_progress

# Found something unexpected
bd comments add bd-123 "[context] finding: Database schema has soft deletes, need to filter deleted_at IS NULL"

# Made a decision
bd comments add bd-123 "[context] decision: Using existing ORM instead of raw SQL for consistency"

# Hit an error
bd comments add bd-123 "[context] error: Foreign key constraint failed - fixed by adding CASCADE"

# Completing the work
bd comments add bd-123 "[outcome] success:true approach:incremental duration:30min complexity:medium"
bd close bd-123 --reason="Added soft delete filtering to all queries"
```

## Migration from Separate Files

If you have existing `context.json` or `outcomes.jsonl` files, you can migrate them to comments:

```bash
# For each issue with context, add as comments
bd comments add bd-123 "[context] <migrated content>"

# Then remove the separate files
rm .beads/context.json .beads/outcomes.jsonl
```
