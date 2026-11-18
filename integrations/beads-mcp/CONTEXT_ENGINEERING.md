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
  "dependent_count": 0,
  "comment_count": 3,
  "comments_omitted": true
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

Minimal rows never carry comment bodies. `comment_count` and `comments_omitted`
say whether the row left comments out, so a scan for text that lives in a
comment knows to look further; `null` means the CLI did not report it (for
example on `blocked`), not that the issue has no comments.

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

## Handling Large Result Sets

When a query returns more than 20 results, the response switches to `CompactedResult` format. This section explains how to detect and handle compacted responses.

### CompactedResult Schema

```python
# Response when >20 results
{
    "compacted": True,
    "total_count": 47,           # Total matching issues (not shown)
    "preview": [
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
        },
        # ... 4 more issues (PREVIEW_COUNT=5)
    ],
    "preview_count": 5,
    "hint": "Use show(issue_id) for full details or add filters"
}
```

### Detecting Compacted Results

Check the `compacted` field in the response:

```python
import json

def handle_issue_list(response):
    """Handle both list and compacted responses."""
    
    if isinstance(response, dict) and response.get("compacted"):
        # Compacted response
        total = response["total_count"]
        shown = response["preview_count"]
        issues = response["preview"]
        print(f"Showing {shown} of {total} issues")
        return issues
    else:
        # Regular list response (all results included)
        return response
```

### Getting Full Results When Compacted

When you get a compacted response, you have several options:

#### Option 1: Use `show()` for Specific Issues

```python
# Get full details for a specific issue
full_issue = show(issue_id="bd-a1b2")
# Returns the Issue model with dependencies, description, etc. Comments are
# count-only (comment_count, comments_omitted); add the bodies on request:
full_issue = show(issue_id="bd-a1b2", include_comments=True)
```

#### Option 2: Narrow Your Query

Add filters to reduce the result set:

```python
# Instead of list(status="open")  # Returns 47+ results
# Try:
issues = list(status="open", priority=0)  # Returns 8 results
```

#### Option 3: Check Type Hints

The response type tells you what to expect:

```python
from typing import Union

def handle_response(response: Union[list, dict]):
    """Properly typed response handling."""
    
    if isinstance(response, dict) and response.get("compacted"):
        # Handle CompactedResult
        for issue in response["preview"]:
            process_minimal_issue(issue)
        print(f"Note: {response['total_count']} total issues exist")
    else:
        # Handle list[IssueMinimal]
        for issue in response:
            process_minimal_issue(issue)
```

### Python Client Example

Here's a complete example handling both response types:

```python
class BeadsClient:
    """Example client with proper compaction handling."""
    
    def get_all_ready_work(self):
        """Safely get ready work, handling compaction."""
        response = self.ready(limit=10, priority=1)
        
        # Check if compacted
        if isinstance(response, dict) and response.get("compacted"):
            print(f"Warning: Showing {response['preview_count']} "
                  f"of {response['total_count']} ready items")
            print(f"Hint: {response['hint']}")
            return response["preview"]
        
        # Full list returned
        return response
    
    def list_with_fallback(self, **filters):
        """List issues, with automatic filter refinement on compaction."""
        response = self.list(**filters)
        
        if isinstance(response, dict) and response.get("compacted"):
            # Too many results - add priority filter to narrow down
            if "priority" not in filters:
                print(f"Too many results ({response['total_count']}). "
                      "Filtering by priority=1...")
                filters["priority"] = 1
                return self.list_with_fallback(**filters)
            else:
                # Can't narrow further, return preview
                return response["preview"]
        
        return response
    
    def show_full_issue(self, issue_id: str):
        """Always get full issue details (never compacted)."""
        return self.show(issue_id=issue_id)
```

## Migration Guide for Clients

If your client was written expecting `list()` and `ready()` to always return `list[Issue]`, follow these steps:

### Step 1: Update Type Hints

```python
# OLD (incorrect with new server)
def process_issues(issues: list[Issue]) -> None:
    for issue in issues:
        print(issue.description)

# NEW (handles both cases)
from typing import Union

IssueListOrCompacted = Union[list[IssueMinimal], CompactedResult]

def process_issues(response: IssueListOrCompacted) -> None:
    if isinstance(response, dict) and response.get("compacted"):
        issues = response["preview"]
        print(f"Note: Only showing preview of {response['total_count']} total")
    else:
        issues = response
    
    for issue in issues:
        print(f"{issue.id}: {issue.title}")  # Works with IssueMinimal
```

### Step 2: Handle Missing Fields

`IssueMinimal` doesn't include `description`, `design`, or `dependencies`. Adjust your code:

```python
# OLD (would fail if using IssueMinimal)
for issue in issues:
    print(f"{issue.title}\n{issue.description}")

# NEW (only use available fields)
for issue in issues:
    print(f"{issue.title}")
    if hasattr(issue, 'description'):
        print(issue.description)
    elif need_description:
        full = show(issue.id)
        print(full.description)
```

### Step 3: Use `show()` for Full Details

When you need dependencies or detailed information:

```python
# Get minimal info for listing
ready_issues = ready(limit=20)

# For detailed work, fetch full issue
for minimal_issue in ready_issues if not isinstance(ready_issues, dict) else ready_issues.get("preview", []):
    full_issue = show(issue_id=minimal_issue.id)
    print(f"Dependencies: {full_issue.dependencies}")
```

## Configuration

### Environment Variables (v0.29.0+)

Compaction behavior can be tuned via environment variables:

```bash
# Set custom compaction threshold
export BEADS_MCP_COMPACTION_THRESHOLD=50

# Set custom preview size
export BEADS_MCP_PREVIEW_COUNT=10
```

**Environment Variables:**

| Variable | Default | Purpose | Constraints |
|----------|---------|---------|-------------|
| `BEADS_MCP_COMPACTION_THRESHOLD` | 20 | Compact results with more than N issues | Must be ≥ 1 |
| `BEADS_MCP_PREVIEW_COUNT` | 5 | Show first N issues in preview | Must be ≥ 1 and ≤ threshold |

**Examples:**

```bash
# Disable compaction by setting high threshold
BEADS_MCP_COMPACTION_THRESHOLD=10000 beads-mcp

# Show more preview items in compacted results
BEADS_MCP_PREVIEW_COUNT=10 beads-mcp

# Show fewer items for limited context windows
BEADS_MCP_COMPACTION_THRESHOLD=10 BEADS_MCP_PREVIEW_COUNT=3 beads-mcp
```

**Default Values:**

If not set, the server uses:

```python
COMPACTION_THRESHOLD = 20  # Compact results with more than 20 issues
PREVIEW_COUNT = 5          # Show first 5 issues in preview
```

**Use Cases:**

- **Tight context windows (100k tokens):** Reduce threshold and preview count
  ```bash
  BEADS_MCP_COMPACTION_THRESHOLD=10 BEADS_MCP_PREVIEW_COUNT=3
  ```

- **Plenty of context (200k+ tokens):** Increase both settings or disable compaction
  ```bash
  BEADS_MCP_COMPACTION_THRESHOLD=1000 BEADS_MCP_PREVIEW_COUNT=20
  ```

- **Debugging/Testing:** Disable compaction entirely
  ```bash
  BEADS_MCP_COMPACTION_THRESHOLD=999999 beads-mcp
  ```

## Comparison

| Scenario | Before | After | Savings |
|----------|--------|-------|---------|
| Tool schemas (all) | ~15,000 bytes | ~500 bytes | 97% |
| List 50 issues | ~20,000 bytes | ~4,000 bytes | 80% |
| Ready work (10) | ~4,000 bytes | ~800 bytes | 80% |
| Single show() | ~400 bytes | ~400 bytes | 0% (full details; comment bodies only with `include_comments=True`) |

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
