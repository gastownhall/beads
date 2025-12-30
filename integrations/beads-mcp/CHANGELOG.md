# Changelog

All notable changes to beads-mcp will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.41.0] - 2024-12-30

### Changed
- Minor maintenance release

## [0.24.0] - 2024-01-15

### Changed

#### Breaking: Context Engineering Optimizations

The `ready()` and `list()` MCP tools now return `list[IssueMinimal]` or `CompactedResult` instead of `list[Issue]`. This **breaking change** reduces context window usage by ~80%.

**What changed:**
- `ready()` returns `list[IssueMinimal] | CompactedResult` (was `list[Issue]`)
- `list()` returns `list[IssueMinimal] | CompactedResult` (was `list[Issue]`)
- `show()` still returns full `Issue` (unchanged)

**IssueMinimal includes:**
- `id`, `title`, `status`, `priority`, `issue_type`
- `assignee`, `labels`, `dependency_count`, `dependent_count`

**IssueMinimal excludes (use show() for these):**
- `description`, `design`, `acceptance_criteria`, `notes`
- `created_at`, `updated_at`, `closed_at`
- `dependencies`, `dependents` (full objects)

**CompactedResult (returned when >20 results):**
```python
{
    "compacted": True,
    "total_count": 47,
    "preview": [/* first 5 IssueMinimal */],
    "preview_count": 5,
    "hint": "Use show(issue_id) for full details"
}
```

**Migration guide:**

1. Check for compacted results:
   ```python
   if isinstance(response, dict) and response.get("compacted"):
       issues = response["preview"]
   else:
       issues = response
   ```

2. Use `show()` for full details:
   ```python
   for issue in issues:
       full_issue = show(issue_id=issue.id)
       print(full_issue.description)
   ```

3. Available options for list operations:
   - `brief=True` - Returns `BriefIssue` (id, title, status, priority only)
   - `fields=["id", "title"]` - Custom field projection
   - `max_description_length=100` - Truncate descriptions

**Rationale:**
- Reduces context usage from ~400 bytes/issue to ~80 bytes/issue
- Prevents context overflow for large issue lists (>20 items)
- Encourages efficient "list then show" pattern for AI agents

See [CONTEXT_ENGINEERING.md](./CONTEXT_ENGINEERING.md) for full migration guide.

### Added
- `discover_tools()` - Lightweight tool catalog for lazy schema loading
- `get_tool_info(tool_name)` - On-demand tool details
- `CompactedResult` model for large result sets
- `IssueMinimal` model for list views
- `BriefIssue` model for ultra-compact responses
- `brief`, `fields`, `max_description_length` parameters for all list operations
- Environment variables for compaction tuning:
  - `BEADS_MCP_COMPACTION_THRESHOLD` (default: 20)
  - `BEADS_MCP_PREVIEW_COUNT` (default: 5)

## [0.23.0] and earlier

See git history for changes prior to context engineering optimizations.
