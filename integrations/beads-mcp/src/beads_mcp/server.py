"""FastMCP server for beads issue tracker."""

from fastmcp import FastMCP

from beads_mcp.models import BlockedIssue, DependencyType, Issue, IssueStatus, IssueType, Stats
from beads_mcp.tools import (
    beads_add_dependency,
    beads_blocked,
    beads_close_issue,
    beads_create_issue,
    beads_init,
    beads_list_issues,
    beads_quickstart,
    beads_ready_work,
    beads_show_issue,
    beads_stats,
    beads_update_issue,
)

# Create FastMCP server
mcp = FastMCP(
    name="Beads",
    instructions="""
We track work in Beads (bd) instead of Markdown.
Check the resource beads://quickstart to see how.
""",
)


# Register quickstart resource
@mcp.resource("beads://quickstart", name="Beads Quickstart Guide")
async def get_quickstart() -> str:
    """Get beads (bd) quickstart guide.

    Read this first to understand how to use beads (bd) commands.
    """
    return await beads_quickstart()


# Register all tools
@mcp.tool(name="ready", description="Find tasks that have no blockers and are ready to be worked on.")
async def ready_work(
    limit: int = 10,
    priority: int | None = None,
    assignee: str | None = None,
) -> list[Issue]:
    """Find issues with no blocking dependencies that are ready to work on."""
    return await beads_ready_work(limit=limit, priority=priority, assignee=assignee)


@mcp.tool(
    name="list",
    description="List all issues with optional filters (status, priority, type, assignee).",
)
async def list_issues(
    status: IssueStatus | None = None,
    priority: int | None = None,
    issue_type: IssueType | None = None,
    assignee: str | None = None,
    limit: int = 50,
) -> list[Issue]:
    """List all issues with optional filters."""
    return await beads_list_issues(
        status=status,
        priority=priority,
        issue_type=issue_type,
        assignee=assignee,
        limit=limit,
    )


@mcp.tool(
    name="show",
    description="Show detailed information about a specific issue including dependencies and dependents.",
)
async def show_issue(issue_id: str) -> Issue:
    """Show detailed information about a specific issue."""
    return await beads_show_issue(issue_id=issue_id)


@mcp.tool(
    name="create",
    description="""Create a new issue (bug, feature, task, epic, or chore) with optional design,
acceptance criteria, and dependencies.""",
)
async def create_issue(
    title: str,
    description: str = "",
    design: str | None = None,
    acceptance: str | None = None,
    external_ref: str | None = None,
    priority: int = 2,
    issue_type: IssueType = "task",
    assignee: str | None = None,
    labels: list[str] | None = None,
    id: str | None = None,
    deps: list[str] | None = None,
) -> Issue:
    """Create a new issue."""
    return await beads_create_issue(
        title=title,
        description=description,
        design=design,
        acceptance=acceptance,
        external_ref=external_ref,
        priority=priority,
        issue_type=issue_type,
        assignee=assignee,
        labels=labels,
        id=id,
        deps=deps,
    )


@mcp.tool(
    name="update",
    description="""Update an existing issue's status, priority, assignee, design notes,
or acceptance criteria. Use this to claim work (set status=in_progress).""",
)
async def update_issue(
    issue_id: str,
    status: IssueStatus | None = None,
    priority: int | None = None,
    assignee: str | None = None,
    title: str | None = None,
    design: str | None = None,
    acceptance_criteria: str | None = None,
    notes: str | None = None,
    external_ref: str | None = None,
) -> Issue:
    """Update an existing issue."""
    return await beads_update_issue(
        issue_id=issue_id,
        status=status,
        priority=priority,
        assignee=assignee,
        title=title,
        design=design,
        acceptance_criteria=acceptance_criteria,
        notes=notes,
        external_ref=external_ref,
    )


@mcp.tool(
    name="close",
    description="Close (complete) an issue. Mark work as done when you've finished implementing/fixing it.",
)
async def close_issue(issue_id: str, reason: str = "Completed") -> list[Issue]:
    """Close (complete) an issue."""
    return await beads_close_issue(issue_id=issue_id, reason=reason)


@mcp.tool(
    name="dep",
    description="""Add a dependency between issues. Types: blocks (hard blocker),
related (soft link), parent-child (epic/subtask), discovered-from (found during work).""",
)
async def add_dependency(
    from_id: str,
    to_id: str,
    dep_type: DependencyType = "blocks",
) -> str:
    """Add a dependency relationship between two issues."""
    return await beads_add_dependency(
        from_id=from_id,
        to_id=to_id,
        dep_type=dep_type,
    )


@mcp.tool(
    name="stats",
    description="Get statistics: total issues, open, in_progress, closed, blocked, ready, and average lead time.",
)
async def stats() -> Stats:
    """Get statistics about tasks."""
    return await beads_stats()


@mcp.tool(
    name="blocked",
    description="Get blocked issues showing what dependencies are blocking them from being worked on.",
)
async def blocked() -> list[BlockedIssue]:
    """Get blocked issues."""
    return await beads_blocked()


@mcp.tool(
    name="init",
    description="""Initialize bd in current directory. Creates .beads/ directory and
database with optional custom prefix for issue IDs.""",
)
async def init(prefix: str | None = None) -> str:
    """Initialize bd in current directory."""
    return await beads_init(prefix=prefix)


def main() -> None:
    """Entry point for the MCP server."""
    mcp.run()


if __name__ == "__main__":
    main()
