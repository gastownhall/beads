"""FastMCP server for beads issue tracker."""

import asyncio
import atexit
import importlib.metadata
import logging
import os
import signal
import subprocess
import sys
from functools import wraps
from types import FrameType
from typing import Any, Awaitable, Callable, TypeVar

from fastmcp import FastMCP

from beads_mcp.models import BlockedIssue, DependencyType, Issue, IssueStatus, IssueType, Stats
from beads_mcp.tools import (
    beads_add_dependency,
    beads_blocked,
    beads_close_issue,
    beads_create_issue,
    beads_detect_pollution,
    beads_get_schema_info,
    beads_init,
    beads_inspect_migration,
    beads_list_issues,
    beads_quickstart,
    beads_ready_work,
    beads_repair_deps,
    beads_reopen_issue,
    beads_show_issue,
    beads_stats,
    beads_update_issue,
    beads_validate,
    current_workspace,  # ContextVar for per-request workspace routing
)

# Setup logging for lifecycle events
logger = logging.getLogger(__name__)
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
    stream=sys.stderr,  # Ensure logs don't pollute stdio protocol
)

T = TypeVar("T")

# Global state for cleanup
_daemon_clients: list[Any] = []
_cleanup_done = False

# Persistent workspace context (survives across MCP tool calls)
# os.environ doesn't persist across MCP requests, so we need module-level storage
_workspace_context: dict[str, str] = {}

# Create FastMCP server
mcp = FastMCP(
    name="Beads",
    instructions="""
We track work in Beads (bd) instead of Markdown.
Check the resource beads://quickstart to see how.

IMPORTANT: Call set_context with your workspace root before any write operations.
""",
)


def cleanup() -> None:
    """Clean up resources on exit.
    
    Closes daemon connections and removes temp files.
    Safe to call multiple times.
    """
    global _cleanup_done
    
    if _cleanup_done:
        return
    
    _cleanup_done = True
    logger.info("Cleaning up beads-mcp resources...")
    
    # Close all daemon client connections
    for client in _daemon_clients:
        try:
            if hasattr(client, 'cleanup'):
                client.cleanup()
                logger.debug(f"Closed daemon client: {client}")
        except Exception as e:
            logger.warning(f"Error closing daemon client: {e}")
    
    _daemon_clients.clear()
    logger.info("Cleanup complete")


def signal_handler(signum: int, frame: FrameType | None) -> None:
    """Handle termination signals gracefully."""
    sig_name = signal.Signals(signum).name
    logger.info(f"Received {sig_name}, shutting down gracefully...")
    cleanup()
    sys.exit(0)


# Register cleanup handlers
atexit.register(cleanup)
signal.signal(signal.SIGTERM, signal_handler)
signal.signal(signal.SIGINT, signal_handler)

# Get version from package metadata
try:
    __version__ = importlib.metadata.version("beads-mcp")
except importlib.metadata.PackageNotFoundError:
    __version__ = "dev"

logger.info(f"beads-mcp v{__version__} initialized with lifecycle management")


def with_workspace(func: Callable[..., Awaitable[T]]) -> Callable[..., Awaitable[T]]:
    """Decorator to set workspace context for the duration of a tool call.

    Extracts workspace_root parameter from tool call kwargs, resolves it,
    and sets current_workspace ContextVar for the request duration.
    Falls back to persistent context or BEADS_WORKING_DIR if workspace_root not provided.

    This enables per-request workspace routing for multi-project support.
    """
    @wraps(func)
    async def wrapper(*args: Any, **kwargs: Any) -> T:
        # Extract workspace_root parameter (if provided)
        workspace_root = kwargs.get('workspace_root')

        # Determine workspace: parameter > persistent context > env > None
        workspace = (
            workspace_root
            or _workspace_context.get("BEADS_WORKING_DIR")
            or os.environ.get("BEADS_WORKING_DIR")
        )

        # Set ContextVar for this request
        token = current_workspace.set(workspace)

        try:
            # Execute tool with workspace context set
            return await func(*args, **kwargs)
        finally:
            # Always reset ContextVar after tool completes
            current_workspace.reset(token)

    return wrapper


def require_context(func: Callable[..., Awaitable[T]]) -> Callable[..., Awaitable[T]]:
    """Decorator to enforce context has been set before write operations.
    
    Passes if either:
    - workspace_root was provided on tool call (via ContextVar), OR
    - BEADS_WORKING_DIR is set (from set_context)
    
    Only enforces if BEADS_REQUIRE_CONTEXT=1 is set in environment.
    This allows backward compatibility while adding safety for multi-repo setups.
    """
    @wraps(func)
    async def wrapper(*args: Any, **kwargs: Any) -> T:
        # Only enforce if explicitly enabled
        if os.environ.get("BEADS_REQUIRE_CONTEXT") == "1":
            # Check ContextVar or environment
            workspace = current_workspace.get() or os.environ.get("BEADS_WORKING_DIR")
            if not workspace:
                raise ValueError(
                    "Context not set. Either provide workspace_root parameter or call set_context() first."
                )
        return await func(*args, **kwargs)
    return wrapper


def _find_beads_db(workspace_root: str) -> str | None:
    """Find .beads/*.db by walking up from workspace_root.
    
    Args:
        workspace_root: Starting directory to search from
        
    Returns:
        Absolute path to first .db file found in .beads/, None otherwise
    """
    import glob
    current = os.path.abspath(workspace_root)
    
    while True:
        beads_dir = os.path.join(current, ".beads")
        if os.path.isdir(beads_dir):
            # Find any .db file in .beads/
            db_files = glob.glob(os.path.join(beads_dir, "*.db"))
            if db_files:
                return db_files[0]  # Return first .db file found
        
        parent = os.path.dirname(current)
        if parent == current:  # Reached root
            break
        current = parent
    
    return None


def _resolve_workspace_root(path: str) -> str:
    """Resolve workspace root to git repo root if inside a git repo.
    
    Args:
        path: Directory path to resolve
        
    Returns:
        Git repo root if inside git repo, otherwise the original path
    """
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            cwd=path,
            capture_output=True,
            text=True,
            check=False,
            shell=sys.platform == "win32",
            stdin=subprocess.DEVNULL,  # Prevent inheriting MCP's stdin
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception as e:
        logger.debug(f"Git detection failed for {path}: {e}")
        pass
    
    return os.path.abspath(path)


# Register quickstart resource
@mcp.resource("beads://quickstart", name="Beads Quickstart Guide")
async def get_quickstart() -> str:
    """Get beads (bd) quickstart guide.

    Read this first to understand how to use beads (bd) commands.
    """
    return await beads_quickstart()


# Context management tools
@mcp.tool(
    name="set_context",
    description="Set the workspace root directory for all bd operations. Call this first!",
)
async def set_context(workspace_root: str) -> str:
    """Set workspace root directory and discover the beads database.

    Args:
        workspace_root: Absolute path to workspace/project root directory

    Returns:
        Confirmation message with resolved paths
    """
    # Resolve to git repo root if possible (run in thread to avoid blocking event loop)
    try:
        resolved_root = await asyncio.wait_for(
            asyncio.to_thread(_resolve_workspace_root, workspace_root),
            timeout=5.0,  # Longer timeout to handle slow git operations
        )
    except asyncio.TimeoutError:
        logger.error(f"Git detection timed out after 5s for: {workspace_root}")
        return (
            f"Error: Git repository detection timed out.\n"
            f"  Provided path: {workspace_root}\n"
            f"  This may indicate a slow filesystem or git configuration issue.\n"
            f"  Please ensure the path is correct and git is responsive."
        )

    # Store in persistent context (survives across MCP tool calls)
    _workspace_context["BEADS_WORKING_DIR"] = resolved_root
    _workspace_context["BEADS_CONTEXT_SET"] = "1"

    # Also set in os.environ for compatibility
    os.environ["BEADS_WORKING_DIR"] = resolved_root
    os.environ["BEADS_CONTEXT_SET"] = "1"

    # Find beads database
    db_path = _find_beads_db(resolved_root)

    if db_path is None:
        # Clear any stale DB path
        _workspace_context.pop("BEADS_DB", None)
        os.environ.pop("BEADS_DB", None)
        return (
            f"Context set successfully:\n"
            f"  Workspace root: {resolved_root}\n"
            f"  Database: Not found (run 'bd init' to create)"
        )

    # Set database path in both persistent context and os.environ
    _workspace_context["BEADS_DB"] = db_path
    os.environ["BEADS_DB"] = db_path

    return (
        f"Context set successfully:\n"
        f"  Workspace root: {resolved_root}\n"
        f"  Database: {db_path}"
    )


@mcp.tool(
    name="where_am_i",
    description="Show current workspace context and database path",
)
async def where_am_i(workspace_root: str | None = None) -> str:
    """Show current workspace context for debugging."""
    context_set = (
        _workspace_context.get("BEADS_CONTEXT_SET")
        or os.environ.get("BEADS_CONTEXT_SET")
    )

    if not context_set:
        return (
            "Context not set. Call set_context with your workspace root first.\n"
            f"Current process CWD: {os.getcwd()}\n"
            f"BEADS_WORKING_DIR (persistent): {_workspace_context.get('BEADS_WORKING_DIR', 'NOT SET')}\n"
            f"BEADS_WORKING_DIR (env): {os.environ.get('BEADS_WORKING_DIR', 'NOT SET')}\n"
            f"BEADS_DB: {_workspace_context.get('BEADS_DB') or os.environ.get('BEADS_DB', 'NOT SET')}"
        )

    working_dir = (
        _workspace_context.get("BEADS_WORKING_DIR")
        or os.environ.get("BEADS_WORKING_DIR", "NOT SET")
    )
    db_path = (
        _workspace_context.get("BEADS_DB")
        or os.environ.get("BEADS_DB", "NOT SET")
    )
    actor = os.environ.get("BEADS_ACTOR", "NOT SET")

    return (
        f"Workspace root: {working_dir}\n"
        f"Database: {db_path}\n"
        f"Actor: {actor}"
    )


# Register all tools
@mcp.tool(name="ready", description="Find tasks that have no blockers and are ready to be worked on.")
@with_workspace
async def ready_work(
    limit: int = 10,
    priority: int | None = None,
    assignee: str | None = None,
    workspace_root: str | None = None,
) -> list[Issue]:
    """Find issues with no blocking dependencies that are ready to work on."""
    issues = await beads_ready_work(limit=limit, priority=priority, assignee=assignee)

    # Strip dependencies/dependents to reduce payload size
    # Use show() for full details
    for issue in issues:
        issue.dependencies = []
        issue.dependents = []

    return issues


@mcp.tool(
    name="list",
    description="List all issues with optional filters (status, priority, type, assignee).",
)
@with_workspace
async def list_issues(
    status: IssueStatus | None = None,
    priority: int | None = None,
    issue_type: IssueType | None = None,
    assignee: str | None = None,
    limit: int = 20,  # Reduced from 50 to avoid MCP buffer overflow
    workspace_root: str | None = None,
) -> list[Issue]:
    """List all issues with optional filters."""
    issues = await beads_list_issues(
        status=status,
        priority=priority,
        issue_type=issue_type,
        assignee=assignee,
        limit=limit,
    )

    # Strip dependencies/dependents to reduce payload size
    # Use show() for full details
    for issue in issues:
        issue.dependencies = []
        issue.dependents = []

    return issues


@mcp.tool(
    name="show",
    description="Show detailed information about a specific issue including dependencies and dependents.",
)
@with_workspace
async def show_issue(issue_id: str, workspace_root: str | None = None) -> Issue:
    """Show detailed information about a specific issue."""
    return await beads_show_issue(issue_id=issue_id)


@mcp.tool(
    name="create",
    description="""Create a new issue (bug, feature, task, epic, or chore) with optional design,
acceptance criteria, and dependencies.""",
)
@with_workspace
@require_context
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
    workspace_root: str | None = None,
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
    description="""Update an existing issue's status, priority, assignee, description, design notes,
or acceptance criteria. Use this to claim work (set status=in_progress).""",
)
@with_workspace
@require_context
async def update_issue(
    issue_id: str,
    status: IssueStatus | None = None,
    priority: int | None = None,
    assignee: str | None = None,
    title: str | None = None,
    description: str | None = None,
    design: str | None = None,
    acceptance_criteria: str | None = None,
    notes: str | None = None,
    external_ref: str | None = None,
    workspace_root: str | None = None,
) -> Issue | list[Issue] | None:
    """Update an existing issue."""
    # If trying to close via update, redirect to close_issue to preserve approval workflow
    if status == "closed":
        issues = await beads_close_issue(issue_id=issue_id, reason="Closed via update")
        return issues[0] if issues else None
    
    return await beads_update_issue(
        issue_id=issue_id,
        status=status,
        priority=priority,
        assignee=assignee,
        title=title,
        description=description,
        design=design,
        acceptance_criteria=acceptance_criteria,
        notes=notes,
        external_ref=external_ref,
    )


@mcp.tool(
    name="close",
    description="Close (complete) an issue. Mark work as done when you've finished implementing/fixing it.",
)
@with_workspace
@require_context
async def close_issue(issue_id: str, reason: str = "Completed", workspace_root: str | None = None) -> list[Issue]:
    """Close (complete) an issue."""
    return await beads_close_issue(issue_id=issue_id, reason=reason)


@mcp.tool(
    name="reopen",
    description="Reopen one or more closed issues. Sets status to 'open' and clears closed_at timestamp.",
)
@with_workspace
@require_context
async def reopen_issue(issue_ids: list[str], reason: str | None = None, workspace_root: str | None = None) -> list[Issue]:
    """Reopen one or more closed issues."""
    return await beads_reopen_issue(issue_ids=issue_ids, reason=reason)


@mcp.tool(
    name="dep",
    description="""Add a dependency between issues. Types: blocks (hard blocker),
related (soft link), parent-child (epic/subtask), discovered-from (found during work).""",
)
@with_workspace
@require_context
async def add_dependency(
    issue_id: str,
    depends_on_id: str,
    dep_type: DependencyType = "blocks",
    workspace_root: str | None = None,
) -> str:
    """Add a dependency relationship between two issues."""
    return await beads_add_dependency(
        issue_id=issue_id,
        depends_on_id=depends_on_id,
        dep_type=dep_type,
    )


@mcp.tool(
    name="stats",
    description="Get statistics: total issues, open, in_progress, closed, blocked, ready, and average lead time.",
)
@with_workspace
async def stats(workspace_root: str | None = None) -> Stats:
    """Get statistics about tasks."""
    return await beads_stats()


@mcp.tool(
    name="blocked",
    description="Get blocked issues showing what dependencies are blocking them from being worked on.",
)
@with_workspace
async def blocked(workspace_root: str | None = None) -> list[BlockedIssue]:
    """Get blocked issues."""
    return await beads_blocked()


@mcp.tool(
    name="init",
    description="""Initialize bd in current directory. Creates .beads/ directory and
database with optional custom prefix for issue IDs.""",
)
@with_workspace
@require_context
async def init(prefix: str | None = None, workspace_root: str | None = None) -> str:
    """Initialize bd in current directory."""
    return await beads_init(prefix=prefix)


@mcp.tool(
    name="debug_env",
    description="Debug tool: Show environment and working directory information",
)
@with_workspace
async def debug_env(workspace_root: str | None = None) -> str:
    """Debug tool to check working directory and environment variables."""
    info = []
    info.append("=== Working Directory Debug Info ===\n")
    info.append(f"os.getcwd(): {os.getcwd()}\n")
    info.append(f"PWD env var: {os.environ.get('PWD', 'NOT SET')}\n")
    info.append(f"BEADS_WORKING_DIR env var: {os.environ.get('BEADS_WORKING_DIR', 'NOT SET')}\n")
    info.append(f"BEADS_PATH env var: {os.environ.get('BEADS_PATH', 'NOT SET')}\n")
    info.append(f"BEADS_DB env var: {os.environ.get('BEADS_DB', 'NOT SET')}\n")
    info.append(f"HOME: {os.environ.get('HOME', 'NOT SET')}\n")
    info.append(f"USER: {os.environ.get('USER', 'NOT SET')}\n")
    info.append("\n=== All Environment Variables ===\n")
    for key, value in sorted(os.environ.items()):
        if not key.startswith("_"):  # Skip internal vars
            info.append(f"{key}={value}\n")
    return "".join(info)


@mcp.tool(
    name="inspect_migration",
    description="Get migration plan and database state for agent analysis.",
)
@with_workspace
async def inspect_migration(workspace_root: str | None = None) -> dict[str, Any]:
    """Get migration plan and database state for agent analysis.
    
    AI agents should:
    1. Review registered_migrations to understand what will run
    2. Check warnings array for issues (missing config, version mismatch)
    3. Verify missing_config is empty before migrating
    4. Check invariants_to_check to understand safety guarantees
    
    Returns migration plan, current db state, warnings, and invariants.
    """
    return await beads_inspect_migration()


@mcp.tool(
    name="get_schema_info",
    description="Get current database schema for inspection.",
)
@with_workspace
async def get_schema_info(workspace_root: str | None = None) -> dict[str, Any]:
    """Get current database schema for inspection.
    
    Returns tables, schema version, config, sample issue IDs, and detected prefix.
    Useful for verifying database state before migrations.
    """
    return await beads_get_schema_info()


@mcp.tool(
    name="repair_deps",
    description="Find and optionally fix orphaned dependency references.",
)
@with_workspace
async def repair_deps(fix: bool = False, workspace_root: str | None = None) -> dict[str, Any]:
    """Find and optionally fix orphaned dependency references.
    
    Scans all issues for dependencies pointing to non-existent issues.
    Returns orphaned dependencies and optionally removes them with fix=True.
    """
    return await beads_repair_deps(fix=fix)


@mcp.tool(
    name="detect_pollution",
    description="Detect test issues that leaked into production database.",
)
@with_workspace
async def detect_pollution(clean: bool = False, workspace_root: str | None = None) -> dict[str, Any]:
    """Detect test issues that leaked into production database.
    
    Detects test issues using pattern matching (titles starting with 'test', etc.).
    Returns detected test issues and optionally deletes them with clean=True.
    """
    return await beads_detect_pollution(clean=clean)


@mcp.tool(
    name="validate",
    description="Run comprehensive database health checks.",
)
@with_workspace
async def validate(
    checks: str | None = None,
    fix_all: bool = False,
    workspace_root: str | None = None,
) -> dict[str, Any]:
    """Run comprehensive database health checks.
    
    Available checks: orphans, duplicates, pollution, conflicts.
    If checks is None, runs all checks.
    Returns validation results for each check.
    """
    return await beads_validate(checks=checks, fix_all=fix_all)


async def async_main() -> None:
    """Async entry point for the MCP server."""
    await mcp.run_async(transport="stdio")


def main() -> None:
    """Entry point for the MCP server."""
    asyncio.run(async_main())


if __name__ == "__main__":
    main()
