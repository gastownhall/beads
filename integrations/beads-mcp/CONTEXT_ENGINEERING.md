# Context Engineering for beads-mcp

## Overview

This document describes the context engineering optimizations added to beads-mcp to reduce context window usage by ~80-90% while maintaining full functionality.

## The Problem

MCP servers load all tool schemas at startup, consuming significant context:
- **Before:** ~10-50k tokens for full beads tool schemas
- **After:** ~2-5k tokens with lazy loading and compaction

For coding agents operating in limited context windows (100k-200k tokens), this overhead leaves less room for:
- Code files and diffs
- Conversation history
- Task planning and reasoning

## Solutions Implemented

### 1. Lazy Tool Schema Loading

Instead of loading all tool schemas upfront, agents can discover tools on-demand:

```python
# Step 1: Discover available tools (lightweight - ~500 bytes)
discover_tools()
# Returns: { "tools": { "ready": "Find ready tasks", ... }, "count": 15 }

# Step 2: Get details for specific tool (~300 bytes each)
get_tool_info("ready")
# Returns: { "name": "ready", "parameters": {...}, "example": "..." }
```

**Savings:** ~95% reduction in initial schema overhead

### 2. Minimal Issue Models

List operations now return `IssueMinimal` instead of full `Issue`:

```python
# IssueMinimal (~80 bytes per issue)
{
  "id": "bd-a1b2",
  "title": "Fix auth bug",
  "status": "open",
  "priority": 1,
  "issue_type": "bug",
  "assignee": "alice",
  "labels": ["backend"],
  "dependency_count": 2,
  "dependent_count": 0
}

# vs Full Issue (~400 bytes per issue)
{
  "id": "bd-a1b2",
  "title": "Fix auth bug",
  "description": "Long description...",
  "design": "Design notes...",
  "acceptance_criteria": "...",
  "notes": "...",
  "status": "open",
  "priority": 1,
  "issue_type": "bug",
  "created_at": "2024-01-01T...",
  "updated_at": "2024-01-02T...",
  "closed_at": null,
  "assignee": "alice",
  "labels": ["backend"],
  "dependencies": [...],
  "dependents": [...],
  ...
}
```

**Savings:** ~80% reduction per issue in list views

### 3. Result Compaction

When results exceed threshold (20 issues), returns preview + metadata:

```python
# Request: list(status="open")
# Response when >20 results:
{
  "compacted": true,
  "total_count": 47,
  "preview": [/* first 5 issues */],
  "preview_count": 5,
  "hint": "Use show(issue_id) for full details or add filters"
}
```

**Savings:** Prevents unbounded context growth from large queries

## Usage Patterns

### Efficient Workflow (Recommended)

```python
# 1. Set context once
set_context(workspace_root="/path/to/project")

# 2. Get ready work (minimal format)
issues = ready(limit=10, priority=1)

# 3. Pick an issue and get full details only when needed
full_issue = show(issue_id="bd-a1b2")

# 4. Do work...

# 5. Close when done
close(issue_id="bd-a1b2", reason="Fixed in PR #123")
```

### Tool Discovery Workflow

```python
# First time using beads? Discover tools efficiently:
tools = discover_tools()
# → {"tools": {"ready": "...", "list": "...", ...}, "count": 15}

# Need to know how to use a specific tool?
info = get_tool_info("create")
# → {"parameters": {...}, "example": "create(title='...', ...)"}
```

## Configuration

Compaction settings in `server.py`:

```python
COMPACTION_THRESHOLD = 20  # Compact results with more than N issues
PREVIEW_COUNT = 5          # Show N issues in preview
```

## Comparison

| Scenario | Before | After | Savings |
|----------|--------|-------|---------|
| Tool schemas (all) | ~15,000 bytes | ~500 bytes | 97% |
| List 50 issues | ~20,000 bytes | ~4,000 bytes | 80% |
| Ready work (10) | ~4,000 bytes | ~800 bytes | 80% |
| Single show() | ~400 bytes | ~400 bytes | 0% (full details) |

## Design Principles

1. **Lazy Loading**: Only fetch what you need, when you need it
2. **Minimal by Default**: List views use lightweight models
3. **Full Details On-Demand**: Use `show()` for complete information
4. **Graceful Degradation**: Large results auto-compact with hints
5. **Backward Compatible**: Existing workflows continue to work

## Credits

Inspired by:
- [MCP Bridge](https://github.com/mahawi1992/mwilliams_mcpbridge) - Context engineering for MCP servers
- [Manus Context Engineering](https://rlancemartin.github.io/2025/10/15/manus/) - Compaction and offloading patterns
- [Anthropic's Context Engineering Guide](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
