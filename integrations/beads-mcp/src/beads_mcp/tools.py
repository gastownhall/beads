"""MCP tools for beads issue tracker."""

from typing import Annotated

from .bd_client import BdClient, BdError
from .models import (
    AddDependencyParams,
    BlockedIssue,
    CloseIssueParams,
    CreateIssueParams,
    DependencyType,
    InitParams,
    Issue,
    IssueStatus,
    IssueType,
    ListIssuesParams,
    ReadyWorkParams,
    ShowIssueParams,
    Stats,
    UpdateIssueParams,
)

# Global client instance - initialized on first use
_client: BdClient | None = None

# Default constants
DEFAULT_ISSUE_TYPE: IssueType = "task"
DEFAULT_DEPENDENCY_TYPE: DependencyType = "blocks"


def _get_client() -> BdClient:
    """Get a BdClient instance, creating it on first use.

    Returns:
        Configured BdClient instance (config loaded automatically)
    """
    global _client
    if _client is None:
        _client = BdClient()
    return _client


async def beads_ready_work(
    limit: Annotated[int, "Maximum number of issues to return (1-100)"] = 10,
    priority: Annotated[int | None, "Filter by priority (0-4, 0=highest)"] = None,
    assignee: Annotated[str | None, "Filter by assignee"] = None,
) -> list[Issue]:
    """Find issues with no blocking dependencies that are ready to work on.

    Ready work = status is 'open' AND no blocking dependencies.
    Perfect for agents to claim next work!
    """
    client = _get_client()
    params = ReadyWorkParams(limit=limit, priority=priority, assignee=assignee)
    return await client.ready(params)


async def beads_list_issues(
    status: Annotated[
        IssueStatus | None, "Filter by status (open, in_progress, blocked, closed)"
    ] = None,
    priority: Annotated[int | None, "Filter by priority (0-4, 0=highest)"] = None,
    issue_type: Annotated[
        IssueType | None, "Filter by type (bug, feature, task, epic, chore)"
    ] = None,
    assignee: Annotated[str | None, "Filter by assignee"] = None,
    limit: Annotated[int, "Maximum number of issues to return (1-1000)"] = 50,
) -> list[Issue]:
    """List all issues with optional filters."""
    client = _get_client()

    params = ListIssuesParams(
        status=status,
        priority=priority,
        issue_type=issue_type,
        assignee=assignee,
        limit=limit,
    )
    return await client.list_issues(params)


async def beads_show_issue(
    issue_id: Annotated[str, "Issue ID (e.g., bd-1)"],
) -> Issue:
    """Show detailed information about a specific issue.

    Includes full description, dependencies, and dependents.
    """
    client = _get_client()
    params = ShowIssueParams(issue_id=issue_id)
    return await client.show(params)


async def beads_create_issue(
    title: Annotated[str, "Issue title"],
    description: Annotated[str, "Issue description"] = "",
    design: Annotated[str | None, "Design notes"] = None,
    acceptance: Annotated[str | None, "Acceptance criteria"] = None,
    external_ref: Annotated[str | None, "External reference (e.g., gh-9, jira-ABC)"] = None,
    priority: Annotated[int, "Priority (0-4, 0=highest)"] = 2,
    issue_type: Annotated[
        IssueType, "Type: bug, feature, task, epic, or chore"
    ] = DEFAULT_ISSUE_TYPE,
    assignee: Annotated[str | None, "Assignee username"] = None,
    labels: Annotated[list[str] | None, "List of labels"] = None,
    id: Annotated[str | None, "Explicit issue ID (e.g., bd-42)"] = None,
    deps: Annotated[list[str] | None, "Dependencies (e.g., ['bd-20', 'blocks:bd-15'])"] = None,
) -> Issue:
    """Create a new issue.

    Use this when you discover new work during your session.
    Link it back with beads_add_dependency using 'discovered-from' type.
    """
    client = _get_client()
    params = CreateIssueParams(
        title=title,
        description=description,
        design=design,
        acceptance=acceptance,
        external_ref=external_ref,
        priority=priority,
        issue_type=issue_type,
        assignee=assignee,
        labels=labels or [],
        id=id,
        deps=deps or [],
    )
    return await client.create(params)


async def beads_update_issue(
    issue_id: Annotated[str, "Issue ID (e.g., bd-1)"],
    status: Annotated[IssueStatus | None, "New status (open, in_progress, blocked, closed)"] = None,
    priority: Annotated[int | None, "New priority (0-4)"] = None,
    assignee: Annotated[str | None, "New assignee"] = None,
    title: Annotated[str | None, "New title"] = None,
    design: Annotated[str | None, "Design notes"] = None,
    acceptance_criteria: Annotated[str | None, "Acceptance criteria"] = None,
    notes: Annotated[str | None, "Additional notes"] = None,
    external_ref: Annotated[str | None, "External reference (e.g., gh-9, jira-ABC)"] = None,
) -> Issue:
    """Update an existing issue.

    Claim work by setting status to 'in_progress'.
    """
    client = _get_client()
    params = UpdateIssueParams(
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
    return await client.update(params)


async def beads_close_issue(
    issue_id: Annotated[str, "Issue ID (e.g., bd-1)"],
    reason: Annotated[str, "Reason for closing"] = "Completed",
) -> list[Issue]:
    """Close (complete) an issue.

    Mark work as done when you've finished implementing/fixing it.
    """
    client = _get_client()
    params = CloseIssueParams(issue_id=issue_id, reason=reason)
    return await client.close(params)


async def beads_add_dependency(
    from_id: Annotated[str, "Issue that depends on another (e.g., bd-2)"],
    to_id: Annotated[str, "Issue that blocks or is related to from_id (e.g., bd-1)"],
    dep_type: Annotated[
        DependencyType,
        "Dependency type: blocks, related, parent-child, or discovered-from",
    ] = DEFAULT_DEPENDENCY_TYPE,
) -> str:
    """Add a dependency relationship between two issues.

    Types:
    - blocks: to_id must complete before from_id can start
    - related: Soft connection, doesn't block progress
    - parent-child: Epic/subtask hierarchical relationship
    - discovered-from: Track that from_id was discovered while working on to_id

    Use 'discovered-from' when you find new work during your session.
    """
    client = _get_client()
    params = AddDependencyParams(
        from_id=from_id,
        to_id=to_id,
        dep_type=dep_type,
    )
    try:
        await client.add_dependency(params)
        return f"Added dependency: {from_id} depends on {to_id} ({dep_type})"
    except BdError as e:
        return f"Error: {str(e)}"


async def beads_quickstart() -> str:
    """Get bd quickstart guide.

    Read this first to understand how to use beads (bd) commands.
    """
    client = _get_client()
    return await client.quickstart()


async def beads_stats() -> Stats:
    """Get statistics about issues.

    Returns total issues, open, in_progress, closed, blocked, ready issues,
    and average lead time in hours.
    """
    client = _get_client()
    return await client.stats()


async def beads_blocked() -> list[BlockedIssue]:
    """Get blocked issues.

    Returns issues that have blocking dependencies, showing what blocks them.
    """
    client = _get_client()
    return await client.blocked()


async def beads_init(
    prefix: Annotated[
        str | None, "Issue prefix (e.g., 'myproject' for myproject-1, myproject-2)"
    ] = None,
) -> str:
    """Initialize bd in current directory.

    Creates .beads/ directory and database file with optional custom prefix.
    """
    client = _get_client()
    params = InitParams(prefix=prefix)
    return await client.init(params)
