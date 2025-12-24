"""FastMCP server for beads issue tracker.

Context Engineering Optimizations (v0.24.0):
- Lazy tool schema loading via discover_tools() and get_tool_info()
- Minimal issue models for list views (~80% context reduction)
- Result compaction for large queries (>20 issues)
- On-demand full details via show() command

These optimizations reduce context window usage from ~10-50k tokens to ~2-5k tokens,
enabling more efficient agent operation without sacrificing functionality.
"""

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

from beads_mcp.models import (
    BlockedIssue,
    BriefDep,
    BriefIssue,
    CompactedResult,
    DependencyType,
    Issue,
    IssueMinimal,
    IssueStatus,
    IssueType,
    LinkedIssue,
    OperationResult,
    Stats,
)
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

# =============================================================================
# CONTEXT ENGINEERING: Compaction Settings (Configurable via Environment)
# =============================================================================
# These settings control how large result sets are compacted to prevent context overflow.
# Override via environment variables:
#   BEADS_MCP_COMPACTION_THRESHOLD - Compact results with >N issues (default: 20)
#   BEADS_MCP_PREVIEW_COUNT - Show first N issues in preview (default: 5)

def _get_compaction_settings() -> tuple[int, int]:
    """Load compaction settings from environment or use defaults.
    
    Returns:
        (threshold, preview_count) tuple
    """
    import os
    
    threshold = int(os.environ.get("BEADS_MCP_COMPACTION_THRESHOLD", "20"))
    preview_count = int(os.environ.get("BEADS_MCP_PREVIEW_COUNT", "5"))
    
    # Validate settings
    if threshold < 1:
        raise ValueError("BEADS_MCP_COMPACTION_THRESHOLD must be >= 1")
    if preview_count < 1:
        raise ValueError("BEADS_MCP_PREVIEW_COUNT must be >= 1")
    if preview_count > threshold:
        raise ValueError("BEADS_MCP_PREVIEW_COUNT must be <= BEADS_MCP_COMPACTION_THRESHOLD")
    
    return threshold, preview_count


COMPACTION_THRESHOLD, PREVIEW_COUNT = _get_compaction_settings()

if os.environ.get("BEADS_MCP_COMPACTION_THRESHOLD"):
    logger.info(f"Using BEADS_MCP_COMPACTION_THRESHOLD={COMPACTION_THRESHOLD}")
if os.environ.get("BEADS_MCP_PREVIEW_COUNT"):
    logger.info(f"Using BEADS_MCP_PREVIEW_COUNT={PREVIEW_COUNT}")

# Create FastMCP server
mcp = FastMCP(
    name="Beads",
    instructions="""
We track work in Beads (bd) instead of Markdown.
Check the resource beads://quickstart to see how.

CONTEXT OPTIMIZATION: Use discover_tools() to see available tools (names only),
then get_tool_info(tool_name) for specific tool details. This saves context.

IMPORTANT: Call context(workspace_root='...') to set your workspace before any write operations.
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
    - BEADS_WORKING_DIR is set (from context tool)
    
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
                    "Context not set. Either provide workspace_root parameter or call context(workspace_root='...') first."
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


# =============================================================================
# CONTEXT ENGINEERING: Tool Discovery (Lazy Schema Loading)
# =============================================================================
# These tools enable agents to discover available tools without loading full schemas.
# This reduces initial context from ~10-50k tokens to ~500 bytes.

# Tool metadata for discovery (lightweight - just names and brief descriptions)
_TOOL_CATALOG = {
    "ready": "Find tasks ready to work on (no blockers)",
    "list": "List issues with filters (status, priority, type)",
    "show": "Show full details for a specific issue",
    "create": "Create a new issue (bug, feature, task, epic)",
    "update": "Update issue status, priority, or assignee",
    "close": "Close/complete an issue",
    "reopen": "Reopen closed issues",
    "dep": "Add dependency between issues",
    "stats": "Get issue statistics",
    "blocked": "Show blocked issues and what blocks them",
    "context": "Manage workspace context (set, show, init)",
    "admin": "Administrative/diagnostic operations (validate, repair, schema, debug, migration, pollution)",
    "discover_tools": "List available tools (names only)",
    "get_tool_info": "Get detailed info for a specific tool",
}


@mcp.tool(
    name="discover_tools",
    description="List available beads tools (names and brief descriptions only). Use get_tool_info() for full details.",
)
async def discover_tools() -> dict[str, Any]:
    """Discover available beads tools without loading full schemas.
    
    Returns lightweight tool catalog to minimize context usage.
    Use get_tool_info(tool_name) for full parameter details.
    
    Context savings: ~500 bytes vs ~10-50k for full schemas.
    """
    return {
        "tools": _TOOL_CATALOG,
        "count": len(_TOOL_CATALOG),
        "hint": "Use get_tool_info('tool_name') for full parameters and usage"
    }


@mcp.tool(
    name="get_tool_info",
    description="Get detailed information about a specific beads tool including parameters.",
)
async def get_tool_info(tool_name: str) -> dict[str, Any]:
    """Get detailed info for a specific tool.
    
    Args:
        tool_name: Name of the tool to get info for
        
    Returns:
        Full tool details including parameters and usage examples
    """
    tool_details = {
        "ready": {
            "name": "ready",
            "description": "Find tasks with no blockers, ready to work on",
            "parameters": {
                "limit": "int (1-100, default 10) - Max issues to return",
                "priority": "int (0-4, optional) - Filter by priority",
                "assignee": "str (optional) - Filter by assignee",
                "labels": "list[str] (optional) - AND filter: must have ALL labels",
                "labels_any": "list[str] (optional) - OR filter: must have at least one",
                "unassigned": "bool (default false) - Only unassigned issues",
                "sort_policy": "str (optional) - hybrid|priority|oldest",
                "brief": "bool (default false) - Return only {id, title, status, priority}",
                "fields": "list[str] (optional) - Custom field projection",
                "max_description_length": "int (optional) - Truncate descriptions",
                "workspace_root": "str (optional) - Workspace path"
            },
            "returns": "List of ready issues (minimal format for context efficiency)",
            "example": "ready(limit=5, priority=1, unassigned=True)"
        },
        "list": {
            "name": "list",
            "description": "List all issues with optional filters",
            "parameters": {
                "status": "open|in_progress|blocked|deferred|closed (optional)",
                "priority": "int 0-4 (optional)",
                "issue_type": "bug|feature|task|epic|chore (optional)",
                "assignee": "str (optional)",
                "labels": "list[str] (optional) - AND filter: must have ALL labels",
                "labels_any": "list[str] (optional) - OR filter: must have at least one",
                "query": "str (optional) - Search in title (case-insensitive)",
                "unassigned": "bool (default false) - Only unassigned issues",
                "limit": "int (1-100, default 20)",
                "brief": "bool (default false) - Return only {id, title, status, priority}",
                "fields": "list[str] (optional) - Custom field projection",
                "max_description_length": "int (optional) - Truncate descriptions",
                "workspace_root": "str (optional)"
            },
            "returns": "List of issues (compacted if >20 results)",
            "example": "list(status='open', labels=['bug'], query='auth')"
        },
        "show": {
            "name": "show",
            "description": "Show full details for a specific issue including dependencies",
            "parameters": {
                "issue_id": "str (required) - e.g., 'bd-a1b2'",
                "brief": "bool (default false) - Return only {id, title, status, priority}",
                "brief_deps": "bool (default false) - Full issue with compact dependencies",
                "fields": "list[str] (optional) - Custom field projection",
                "max_description_length": "int (optional) - Truncate description",
                "workspace_root": "str (optional)"
            },
            "returns": "Full Issue object (or BriefIssue/dict based on params)",
            "example": "show(issue_id='bd-a1b2', brief_deps=True)"
        },
        "create": {
            "name": "create",
            "description": "Create a new issue",
            "parameters": {
                "title": "str (required)",
                "description": "str (default '')",
                "priority": "int 0-4 (default 2)",
                "issue_type": "bug|feature|task|epic|chore (default task)",
                "assignee": "str (optional)",
                "labels": "list[str] (optional)",
                "deps": "list[str] (optional) - dependency IDs",
                "brief": "bool (default true) - Return OperationResult instead of full Issue",
                "workspace_root": "str (optional)"
            },
            "returns": "OperationResult {id, action} or full Issue if brief=False",
            "example": "create(title='Fix auth bug', priority=1, issue_type='bug')"
        },
        "update": {
            "name": "update",
            "description": "Update an existing issue",
            "parameters": {
                "issue_id": "str (required)",
                "status": "open|in_progress|blocked|deferred|closed (optional)",
                "priority": "int 0-4 (optional)",
                "assignee": "str (optional)",
                "title": "str (optional)",
                "description": "str (optional)",
                "brief": "bool (default true) - Return OperationResult instead of full Issue",
                "workspace_root": "str (optional)"
            },
            "returns": "OperationResult {id, action} or full Issue if brief=False",
            "example": "update(issue_id='bd-a1b2', status='in_progress')"
        },
        "close": {
            "name": "close",
            "description": "Close/complete an issue",
            "parameters": {
                "issue_id": "str (required)",
                "reason": "str (default 'Completed')",
                "brief": "bool (default true) - Return OperationResult instead of full Issue",
                "workspace_root": "str (optional)"
            },
            "returns": "List of OperationResult or full Issues if brief=False",
            "example": "close(issue_id='bd-a1b2', reason='Fixed in PR #123')"
        },
        "reopen": {
            "name": "reopen",
            "description": "Reopen one or more closed issues",
            "parameters": {
                "issue_ids": "list[str] (required)",
                "reason": "str (optional)",
                "brief": "bool (default true) - Return OperationResult instead of full Issue",
                "workspace_root": "str (optional)"
            },
            "returns": "List of OperationResult or full Issues if brief=False",
            "example": "reopen(issue_ids=['bd-a1b2'], reason='Need more work')"
        },
        "dep": {
            "name": "dep",
            "description": "Add dependency between issues",
            "parameters": {
                "issue_id": "str (required) - Issue that has the dependency",
                "depends_on_id": "str (required) - Issue it depends on",
                "dep_type": "blocks|related|parent-child|discovered-from (default blocks)",
                "workspace_root": "str (optional)"
            },
            "returns": "Confirmation message",
            "example": "dep(issue_id='bd-f1a2', depends_on_id='bd-a1b2', dep_type='blocks')"
        },
        "stats": {
            "name": "stats",
            "description": "Get issue statistics",
            "parameters": {"workspace_root": "str (optional)"},
            "returns": "Stats object with counts and metrics",
            "example": "stats()"
        },
        "blocked": {
            "name": "blocked",
            "description": "Show blocked issues and what blocks them",
            "parameters": {
                "brief": "bool (default false) - Return only {id, title, status, priority}",
                "brief_deps": "bool (default false) - Full issues with compact dependencies",
                "workspace_root": "str (optional)"
            },
            "returns": "List of BlockedIssue (or BriefIssue/dict based on params)",
            "example": "blocked(brief=True)"
        },
        "admin": {
            "name": "admin",
            "description": "Administrative and diagnostic operations",
            "parameters": {
                "action": "str (required) - validate|repair|schema|debug|migration|pollution",
                "checks": "str (optional) - For validate: orphans,duplicates,pollution,conflicts",
                "fix_all": "bool (default false) - For validate: auto-fix issues",
                "fix": "bool (default false) - For repair: apply fixes",
                "clean": "bool (default false) - For pollution: delete test issues",
                "workspace_root": "str (optional)"
            },
            "returns": "Dict with operation results (or string for debug)",
            "example": "admin(action='validate', checks='orphans')"
        },
        "context": {
            "name": "context",
            "description": "Manage workspace context for beads operations",
            "parameters": {
                "action": "str (optional) - set|show|init (default: show if no args, set if workspace_root provided)",
                "workspace_root": "str (optional) - Workspace path for set/init actions",
                "prefix": "str (optional) - Issue ID prefix for init action"
            },
            "returns": "String with context information or confirmation",
            "example": "context(action='set', workspace_root='/path/to/project')"
        },
    }

    if tool_name not in tool_details:
        available = list(tool_details.keys())
        return {
            "error": f"Unknown tool: {tool_name}",
            "available_tools": available,
            "hint": "Use discover_tools() to see all available tools"
        }
    
    return tool_details[tool_name]


# Context management tool - unified set_context, where_am_i, and init
@mcp.tool(
    name="context",
    description="""Manage workspace context for beads operations.
Actions:
- set: Set the workspace root directory (default when workspace_root provided)
- show: Show current workspace context and database path (default when no args)
- init: Initialize beads in the current workspace directory""",
)
async def context(
    action: str | None = None,
    workspace_root: str | None = None,
    prefix: str | None = None,
) -> str:
    """Manage workspace context for beads operations.

    Args:
        action: Action to perform - set, show, or init (inferred if not provided)
        workspace_root: Workspace path for set/init actions
        prefix: Issue ID prefix for init action

    Returns:
        Context information or confirmation message
    """
    # Infer action if not explicitly provided
    if action is None:
        if workspace_root is not None:
            action = "set"
        else:
            action = "show"

    action = action.lower()

    if action == "set":
        if workspace_root is None:
            return "Error: workspace_root is required for 'set' action"
        return await _context_set(workspace_root)

    elif action == "show":
        return _context_show()

    elif action == "init":
        # For init, we need context to be set first
        context_set = (
            _workspace_context.get("BEADS_CONTEXT_SET")
            or os.environ.get("BEADS_CONTEXT_SET")
        )
        if not context_set:
            return (
                "Error: Context must be set before init.\n"
                "Use context(action='set', workspace_root='/path/to/project') first."
            )
        return await beads_init(prefix=prefix)

    else:
        return f"Error: Unknown action '{action}'. Valid actions: set, show, init"


async def _context_set(workspace_root: str) -> str:
    """Set workspace root directory and discover the beads database."""
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
            f"  Database: Not found (run context(action='init') to create)"
        )

    # Set database path in both persistent context and os.environ
    _workspace_context["BEADS_DB"] = db_path
    os.environ["BEADS_DB"] = db_path

    return (
        f"Context set successfully:\n"
        f"  Workspace root: {resolved_root}\n"
        f"  Database: {db_path}"
    )


def _context_show() -> str:
    """Show current workspace context for debugging."""
    context_set = (
        _workspace_context.get("BEADS_CONTEXT_SET")
        or os.environ.get("BEADS_CONTEXT_SET")
    )

    if not context_set:
        return (
            "Context not set. Use context(action='set', workspace_root='...') first.\n"
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
# =============================================================================
# CONTEXT ENGINEERING: Optimized List Tools with Compaction
# =============================================================================

def _to_minimal(issue: Issue) -> IssueMinimal:
    """Convert full Issue to minimal format for context efficiency."""
    return IssueMinimal(
        id=issue.id,
        title=issue.title,
        status=issue.status,
        priority=issue.priority,
        issue_type=issue.issue_type,
        assignee=issue.assignee,
        labels=issue.labels,
        dependency_count=issue.dependency_count,
        dependent_count=issue.dependent_count,
    )


def _to_brief(issue: Issue) -> BriefIssue:
    """Convert full Issue to brief format (id, title, status, priority)."""
    return BriefIssue(
        id=issue.id,
        title=issue.title,
        status=issue.status,
        priority=issue.priority,
    )


def _to_brief_dep(linked: LinkedIssue) -> BriefDep:
    """Convert LinkedIssue to brief dependency format."""
    return BriefDep(
        id=linked.id,
        title=linked.title,
        status=linked.status,
        priority=linked.priority,
        dependency_type=linked.dependency_type,
    )


# Valid fields for Issue model (used for field validation)
VALID_ISSUE_FIELDS: set[str] = {
    "id", "title", "description", "design", "acceptance_criteria", "notes",
    "external_ref", "status", "priority", "issue_type", "created_at",
    "updated_at", "closed_at", "assignee", "labels", "dependency_count",
    "dependent_count", "dependencies", "dependents",
}


def _filter_fields(obj: Issue, fields: list[str]) -> dict[str, Any]:
    """Extract only specified fields from an Issue object.

    Raises:
        ValueError: If any requested field is not a valid Issue field.
    """
    # Validate fields first
    requested = set(fields)
    invalid = requested - VALID_ISSUE_FIELDS
    if invalid:
        raise ValueError(
            f"Invalid field(s): {sorted(invalid)}. "
            f"Valid fields: {sorted(VALID_ISSUE_FIELDS)}"
        )

    result: dict[str, Any] = {}
    for field in fields:
        value = getattr(obj, field)
        # Handle nested Pydantic models
        if hasattr(value, 'model_dump'):
            result[field] = value.model_dump()
        elif isinstance(value, list) and value and hasattr(value[0], 'model_dump'):
            result[field] = [item.model_dump() for item in value]
        else:
            result[field] = value
    return result


def _truncate_description(issue: Issue, max_length: int) -> Issue:
    """Return issue copy with truncated description if needed."""
    if issue.description and len(issue.description) > max_length:
        data = issue.model_dump()
        data['description'] = issue.description[:max_length] + "..."
        return Issue(**data)
    return issue


@mcp.tool(name="ready", description="Find tasks that have no blockers and are ready to be worked on. Returns minimal format for context efficiency.", output_schema=None)
@with_workspace
async def ready_work(
    limit: int = 10,
    priority: int | None = None,
    assignee: str | None = None,
    labels: list[str] | None = None,
    labels_any: list[str] | None = None,
    unassigned: bool = False,
    sort_policy: str | None = None,
    workspace_root: str | None = None,
    brief: bool = False,
    fields: list[str] | None = None,
    max_description_length: int | None = None,
) -> list[IssueMinimal] | list[BriefIssue] | list[dict[str, Any]] | CompactedResult:
    """Find issues with no blocking dependencies that are ready to work on.

    Args:
        limit: Maximum issues to return (1-100, default 10)
        priority: Filter by priority level (0-4)
        assignee: Filter by assignee
        labels: Filter by labels (AND: must have ALL specified labels)
        labels_any: Filter by labels (OR: must have at least one)
        unassigned: Filter to only unassigned issues
        sort_policy: Sort policy: hybrid (default), priority, oldest
        workspace_root: Workspace path override
        brief: If True, return only {id, title, status} (~97% smaller)
        fields: Return only specified fields (custom projections)
        max_description_length: Truncate descriptions to this length

    Returns minimal issue format to reduce context usage by ~80%.
    Use show(issue_id) for full details including dependencies.
    """
    issues = await beads_ready_work(
        limit=limit,
        priority=priority,
        assignee=assignee,
        labels=labels,
        labels_any=labels_any,
        unassigned=unassigned,
        sort_policy=sort_policy,
    )

    # Apply description truncation first
    if max_description_length:
        issues = [_truncate_description(i, max_description_length) for i in issues]

    # Return brief format if requested
    if brief:
        return [_to_brief(issue) for issue in issues]

    # Return specific fields if requested
    if fields:
        return [_filter_fields(issue, fields) for issue in issues]

    # Default: minimal format with compaction
    minimal_issues = [_to_minimal(issue) for issue in issues]

    # Apply compaction if over threshold
    if len(minimal_issues) > COMPACTION_THRESHOLD:
        return CompactedResult(
            compacted=True,
            total_count=len(minimal_issues),
            preview=minimal_issues[:PREVIEW_COUNT],
            preview_count=PREVIEW_COUNT,
            hint=f"Showing {PREVIEW_COUNT} of {len(minimal_issues)} ready issues. Use show(issue_id) for full details."
        )

    return minimal_issues


@mcp.tool(
    name="list",
    description="List all issues with optional filters. When status='blocked', returns BlockedIssue with blocked_by info.",
    output_schema=None,
)
@with_workspace
async def list_issues(
    status: IssueStatus | None = None,
    priority: int | None = None,
    issue_type: IssueType | None = None,
    assignee: str | None = None,
    labels: list[str] | None = None,
    labels_any: list[str] | None = None,
    query: str | None = None,
    unassigned: bool = False,
    limit: int = 20,
    workspace_root: str | None = None,
    brief: bool = False,
    fields: list[str] | None = None,
    max_description_length: int | None = None,
) -> list[IssueMinimal] | list[BriefIssue] | list[dict[str, Any]] | CompactedResult:
    """List all issues with optional filters.

    Args:
        status: Filter by status (open, in_progress, blocked, closed)
        priority: Filter by priority level (0-4)
        issue_type: Filter by type (bug, feature, task, epic, chore)
        assignee: Filter by assignee
        labels: Filter by labels (AND: must have ALL specified labels)
        labels_any: Filter by labels (OR: must have at least one)
        query: Search in title (case-insensitive substring)
        unassigned: Filter to only unassigned issues
        limit: Maximum issues to return (1-100, default 20)
        workspace_root: Workspace path override
        brief: If True, return only {id, title, status} (~97% smaller)
        fields: Return only specified fields (custom projections)
        max_description_length: Truncate descriptions to this length

    Returns minimal issue format to reduce context usage by ~80%.
    Use show(issue_id) for full details including dependencies.
    """
    issues = await beads_list_issues(
        status=status,
        priority=priority,
        issue_type=issue_type,
        assignee=assignee,
        labels=labels,
        labels_any=labels_any,
        query=query,
        unassigned=unassigned,
        limit=limit,
    )

    # Apply description truncation first
    if max_description_length:
        issues = [_truncate_description(i, max_description_length) for i in issues]

    # Return brief format if requested
    if brief:
        return [_to_brief(issue) for issue in issues]

    # Return specific fields if requested
    if fields:
        return [_filter_fields(issue, fields) for issue in issues]

    # Default: minimal format with compaction
    minimal_issues = [_to_minimal(issue) for issue in issues]

    # Apply compaction if over threshold
    if len(minimal_issues) > COMPACTION_THRESHOLD:
        return CompactedResult(
            compacted=True,
            total_count=len(minimal_issues),
            preview=minimal_issues[:PREVIEW_COUNT],
            preview_count=PREVIEW_COUNT,
            hint=f"Showing {PREVIEW_COUNT} of {len(minimal_issues)} issues. Use show(issue_id) for full details or add filters to narrow results."
        )

    return minimal_issues


@mcp.tool(
    name="show",
    description="Show detailed information about a specific issue including dependencies and dependents.",
    output_schema=None,
)
@with_workspace
async def show_issue(
    issue_id: str,
    workspace_root: str | None = None,
    brief: bool = False,
    brief_deps: bool = False,
    fields: list[str] | None = None,
    max_description_length: int | None = None,
) -> Issue | BriefIssue | dict[str, Any]:
    """Show detailed information about a specific issue.

    Args:
        issue_id: The issue ID to show (e.g., 'bd-a1b2')
        workspace_root: Workspace path override
        brief: If True, return only {id, title, status, priority}
        brief_deps: If True, return full issue but with compact dependencies
        fields: Return only specified fields (custom projections)
        max_description_length: Truncate description to this length
    """
    issue = await beads_show_issue(issue_id=issue_id)

    if max_description_length:
        issue = _truncate_description(issue, max_description_length)

    # Brief mode - just identification
    if brief:
        return _to_brief(issue)

    # Brief deps mode - full issue but compact dependencies
    if brief_deps:
        data = issue.model_dump()
        data["dependencies"] = [_to_brief_dep(d).model_dump() for d in issue.dependencies]
        data["dependents"] = [_to_brief_dep(d).model_dump() for d in issue.dependents]
        return data

    if fields:
        return _filter_fields(issue, fields)

    return issue


@mcp.tool(
    name="create",
    description="""Create a new issue (bug, feature, task, epic, or chore) with optional design,
acceptance criteria, and dependencies.""",
    output_schema=None,
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
    brief: bool = True,
) -> Issue | OperationResult:
    """Create a new issue.

    Args:
        brief: If True (default), return minimal OperationResult; if False, return full Issue
    """
    issue = await beads_create_issue(
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

    if brief:
        return OperationResult(id=issue.id, action="created")
    return issue


@mcp.tool(
    name="update",
    description="""Update an existing issue's status, priority, assignee, description, design notes,
or acceptance criteria. Use this to claim work (set status=in_progress).""",
    output_schema=None,
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
    brief: bool = True,
) -> Issue | OperationResult | list[Issue] | list[OperationResult] | None:
    """Update an existing issue.

    Args:
        brief: If True (default), return minimal OperationResult; if False, return full Issue
    """
    # If trying to close via update, redirect to close_issue to preserve approval workflow
    if status == "closed":
        issues = await beads_close_issue(issue_id=issue_id, reason="Closed via update")
        if not issues:
            return None
        if brief:
            return OperationResult(id=issues[0].id, action="closed", message="Closed via update")
        return issues[0]

    issue = await beads_update_issue(
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

    if issue is None:
        return None
    if brief:
        return OperationResult(id=issue.id, action="updated")
    return issue


@mcp.tool(
    name="close",
    description="Close (complete) an issue. Mark work as done when you've finished implementing/fixing it.",
    output_schema=None,
)
@with_workspace
@require_context
async def close_issue(
    issue_id: str,
    reason: str = "Completed",
    workspace_root: str | None = None,
    brief: bool = True,
) -> list[Issue] | list[OperationResult]:
    """Close (complete) an issue.

    Args:
        brief: If True (default), return minimal OperationResult list; if False, return full Issues
    """
    issues = await beads_close_issue(issue_id=issue_id, reason=reason)

    if not brief:
        return issues

    return [OperationResult(id=issue_id, action="closed", message=reason)]


@mcp.tool(
    name="reopen",
    description="Reopen one or more closed issues. Sets status to 'open' and clears closed_at timestamp.",
    output_schema=None,
)
@with_workspace
@require_context
async def reopen_issue(
    issue_ids: list[str],
    reason: str | None = None,
    workspace_root: str | None = None,
    brief: bool = True,
) -> list[Issue] | list[OperationResult]:
    """Reopen one or more closed issues.

    Args:
        brief: If True (default), return minimal OperationResult list; if False, return full Issues
    """
    issues = await beads_reopen_issue(issue_ids=issue_ids, reason=reason)

    if brief:
        return [OperationResult(id=i.id, action="reopened", message=reason) for i in issues]
    return issues


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
    output_schema=None,
)
@with_workspace
async def blocked(
    workspace_root: str | None = None,
    brief: bool = False,
    brief_deps: bool = False,
) -> list[BlockedIssue] | list[BriefIssue] | list[dict[str, Any]]:
    """Get blocked issues.

    Args:
        brief: If True, return only {id, title, status, priority} per issue
        brief_deps: If True, return full issues but with compact dependencies
    """
    issues = await beads_blocked()

    # Brief mode - just identification (most compact)
    if brief:
        return [_to_brief(issue) for issue in issues]

    # Brief deps mode - full issue but compact dependencies
    if brief_deps:
        result = []
        for issue in issues:
            data = issue.model_dump()
            data["dependencies"] = [_to_brief_dep(d).model_dump() for d in issue.dependencies]
            data["dependents"] = [_to_brief_dep(d).model_dump() for d in issue.dependents]
            result.append(data)
        return result

    return issues


@mcp.tool(
    name="admin",
    description="""Administrative and diagnostic operations.
Actions:
- validate: Run database health checks (checks=orphans,duplicates,pollution,conflicts)
- repair: Fix orphaned dependency references (fix=True to apply)
- schema: Show database schema info
- debug: Show environment and working directory info
- migration: Get migration plan and database state
- pollution: Detect/clean test issues (clean=True to delete)""",
)
@with_workspace
async def admin(
    action: str,  # validate, repair, schema, debug, migration, pollution
    checks: str | None = None,
    fix_all: bool = False,
    fix: bool = False,
    clean: bool = False,
    workspace_root: str | None = None,
) -> dict[str, Any] | str:
    """Administrative and diagnostic operations."""

    if action == "validate":
        return await beads_validate(checks=checks, fix_all=fix_all)

    elif action == "repair":
        return await beads_repair_deps(fix=fix)

    elif action == "schema":
        return await beads_get_schema_info()

    elif action == "debug":
        info = []
        info.append("=== Working Directory Debug Info ===\n")
        info.append(f"os.getcwd(): {os.getcwd()}\n")
        info.append(f"PWD env var: {os.environ.get('PWD', 'NOT SET')}\n")
        info.append(f"BEADS_WORKING_DIR env var: {os.environ.get('BEADS_WORKING_DIR', 'NOT SET')}\n")
        info.append(f"BEADS_PATH env var: {os.environ.get('BEADS_PATH', 'NOT SET')}\n")
        info.append(f"BEADS_DB env var: {os.environ.get('BEADS_DB', 'NOT SET')}\n")
        info.append(f"HOME: {os.environ.get('HOME', 'NOT SET')}\n")
        info.append(f"USER: {os.environ.get('USER', 'NOT SET')}\n")
        return "".join(info)

    elif action == "migration":
        return await beads_inspect_migration()

    elif action == "pollution":
        return await beads_detect_pollution(clean=clean)

    else:
        raise ValueError(f"Unknown action: {action}. Use 'validate', 'repair', 'schema', 'debug', 'migration', or 'pollution'")


async def async_main() -> None:
    """Async entry point for the MCP server."""
    await mcp.run_async(transport="stdio")


def main() -> None:
    """Entry point for the MCP server."""
    asyncio.run(async_main())


if __name__ == "__main__":
    main()
