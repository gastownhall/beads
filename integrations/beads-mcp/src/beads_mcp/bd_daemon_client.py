"""Client for interacting with bd daemon via RPC over Unix socket."""

import asyncio
import json
import os
import socket
from pathlib import Path
from typing import Any, Dict, List, Optional

from .bd_client import BdClientBase, BdError
from .models import (
    AddDependencyParams,
    BlockedIssue,
    BlockedParams,
    CloseIssueParams,
    CreateIssueParams,
    InitParams,
    Issue,
    ListIssuesParams,
    ReadyWorkParams,
    ReopenIssueParams,
    ShowIssueParams,
    Stats,
    UpdateIssueParams,
)


class DaemonError(Exception):
    """Base exception for daemon client errors."""

    pass


class DaemonNotRunningError(DaemonError):
    """Raised when daemon is not running."""

    pass


class DaemonConnectionError(DaemonError):
    """Raised when connection to daemon fails."""

    pass


class BdDaemonClient(BdClientBase):
    """Client for calling bd daemon via RPC over Unix socket."""

    socket_path: str | None
    working_dir: str
    actor: str | None
    timeout: float

    def __init__(
        self,
        socket_path: str | None = None,
        working_dir: str | None = None,
        actor: str | None = None,
        timeout: float = 30.0,
    ):
        """Initialize daemon client.

        Args:
            socket_path: Path to daemon Unix socket (optional, will auto-discover if not provided)
            working_dir: Working directory for database discovery (optional)
            actor: Actor name for audit trail (optional)
            timeout: Socket timeout in seconds (default: 30.0)
        """
        self.socket_path = socket_path
        self.working_dir = working_dir or os.getcwd()
        self.actor = actor
        self.timeout = timeout

    async def _find_socket_path(self) -> str:
        """Find daemon socket path by searching for .beads directory.
        
        Checks local .beads/bd.sock first, then falls back to global ~/.beads/bd.sock.

        Returns:
            Path to bd.sock file

        Raises:
            DaemonNotRunningError: If no .beads directory or socket file found
        """
        if self.socket_path:
            return self.socket_path

        # Walk up from working_dir to find .beads/bd.sock
        current = Path(self.working_dir).resolve()
        while True:
            beads_dir = current / ".beads"
            if beads_dir.is_dir():
                sock_path = beads_dir / "bd.sock"
                if sock_path.exists():
                    return str(sock_path)
                # Found .beads but no socket - check global before failing
                break

            # Move up one directory
            parent = current.parent
            if parent == current:
                # Reached filesystem root - check global
                break
            current = parent
        
        # Check for global daemon socket at ~/.beads/bd.sock
        home = Path.home()
        global_sock_path = home / ".beads" / "bd.sock"
        if global_sock_path.exists():
            return str(global_sock_path)
        
        # No socket found anywhere
        raise DaemonNotRunningError(
            "Daemon socket not found. Is the daemon running? Try: bd daemon --start"
        )

    async def _send_request(self, operation: str, args: Dict[str, Any]) -> Any:
        """Send RPC request to daemon and get response.

        Args:
            operation: RPC operation name (e.g., "create", "list")
            args: Operation-specific arguments

        Returns:
            Parsed response data

        Raises:
            DaemonNotRunningError: If daemon is not running
            DaemonConnectionError: If connection fails
            DaemonError: If request fails
        """
        sock_path = await self._find_socket_path()

        # Build request
        request = {
            "operation": operation,
            "args": args,
            "cwd": self.working_dir,
        }
        if self.actor:
            request["actor"] = self.actor

        # Connect to socket and send request
        try:
            reader, writer = await asyncio.wait_for(
                asyncio.open_unix_connection(sock_path),
                timeout=self.timeout,
            )
        except FileNotFoundError:
            raise DaemonNotRunningError(
                f"Daemon socket not found: {sock_path}. Is the daemon running?"
            )
        except asyncio.TimeoutError:
            raise DaemonConnectionError(
                f"Timeout connecting to daemon at {sock_path}"
            )
        except Exception as e:
            raise DaemonConnectionError(
                f"Failed to connect to daemon at {sock_path}: {e}"
            )

        try:
            # Send request as newline-delimited JSON
            request_json = json.dumps(request) + "\n"
            writer.write(request_json.encode())
            await writer.drain()

            # Read response (also newline-delimited JSON)
            try:
                response_line = await asyncio.wait_for(
                    reader.readline(),
                    timeout=self.timeout,
                )
            except asyncio.TimeoutError:
                raise DaemonError(
                    f"Timeout waiting for response from daemon (operation: {operation})"
                )

            if not response_line:
                raise DaemonError("Daemon closed connection without responding")

            response = json.loads(response_line.decode())

            # Check for errors
            if not response.get("success"):
                error = response.get("error", "Unknown error")
                raise DaemonError(f"Daemon returned error: {error}")

            # Return data
            return response.get("data", {})

        finally:
            writer.close()
            await writer.wait_closed()

    async def ping(self) -> Dict[str, Any]:
        """Ping daemon to check if it's running.

        Returns:
            Dict with "message" and "version" fields

        Raises:
            DaemonNotRunningError: If daemon is not running
            DaemonConnectionError: If connection fails
            DaemonError: If request fails
        """
        data = await self._send_request("ping", {})
        result = json.loads(data) if isinstance(data, str) else data
        return dict(result)

    async def health(self) -> Dict[str, Any]:
        """Get daemon health status.

        Returns:
            Dict with health info including:
            - status: "healthy" | "degraded" | "unhealthy"
            - version: daemon version string
            - uptime: uptime in seconds
            - db_response_time_ms: database ping time
            - active_connections: number of active connections
            - memory_bytes: memory usage

        Raises:
            DaemonNotRunningError: If daemon is not running
            DaemonConnectionError: If connection fails
            DaemonError: If request fails
        """
        data = await self._send_request("health", {})
        result = json.loads(data) if isinstance(data, str) else data
        return dict(result)

    async def quickstart(self) -> str:
        """Get quickstart guide.

        Note: Daemon RPC doesn't support quickstart command.
        Returns static guide text pointing users to CLI.

        Returns:
            Quickstart guide text
        """
        return (
            "Beads (bd) Quickstart\n\n"
            "To get started with beads, please refer to the documentation or use the CLI:\n"
            "  bd quickstart\n\n"
            "For MCP usage, try 'beads list' or 'beads create'."
        )

    async def init(self, params: Optional[InitParams] = None) -> str:
        """Initialize new beads database (not typically used via daemon).

        Args:
            params: Initialization parameters (optional)

        Returns:
            Success message

        Note:
            This command is typically run via CLI, not daemon
        """
        params = params or InitParams()
        args: Dict[str, Any] = {}
        if params.prefix:
            args["prefix"] = params.prefix
        result = await self._send_request("init", args)
        return str(result) if result else "Initialized"

    async def create(self, params: CreateIssueParams) -> Issue:
        """Create a new issue.

        Args:
            params: Issue creation parameters

        Returns:
            Created issue
        """
        args = {
            "title": params.title,
            "issue_type": params.issue_type,
            "priority": params.priority if params.priority is not None else 2,
        }
        if params.id:
            args["id"] = params.id
        if params.description:
            args["description"] = params.description
        if params.design:
            args["design"] = params.design
        if params.acceptance:
            args["acceptance_criteria"] = params.acceptance
        if params.assignee:
            args["assignee"] = params.assignee
        if params.labels:
            args["labels"] = params.labels
        if params.deps:
            args["dependencies"] = params.deps

        data = await self._send_request("create", args)
        return Issue(**(json.loads(data) if isinstance(data, str) else data))

    async def update(self, params: UpdateIssueParams) -> Issue:
        """Update an existing issue.

        Args:
            params: Issue update parameters

        Returns:
            Updated issue
        """
        args: Dict[str, Any] = {"id": params.issue_id}
        if params.status:
            args["status"] = params.status
        if params.priority is not None:
            args["priority"] = params.priority
        if params.design is not None:
            args["design"] = params.design
        if params.acceptance_criteria is not None:
            args["acceptance_criteria"] = params.acceptance_criteria
        if params.notes is not None:
            args["notes"] = params.notes
        if params.assignee is not None:
            args["assignee"] = params.assignee
        if params.title is not None:
            args["title"] = params.title
        if params.description is not None:
            args["description"] = params.description

        data = await self._send_request("update", args)
        return Issue(**(json.loads(data) if isinstance(data, str) else data))

    async def close(self, params: CloseIssueParams) -> List[Issue]:
        """Close an issue.

        Args:
            params: Close parameters

        Returns:
            List containing the closed issue
        """
        args = {"id": params.issue_id}
        if params.reason:
            args["reason"] = params.reason

        data = await self._send_request("close", args)
        issue = Issue(**(json.loads(data) if isinstance(data, str) else data))
        return [issue]

    async def reopen(self, params: ReopenIssueParams) -> List[Issue]:
        """Reopen one or more closed issues.

        Args:
            params: Reopen parameters with issue IDs

        Returns:
            List of reopened issues

        Note:
            Reopen operation may not be implemented in daemon RPC yet
        """
        # Note: reopen operation may not be in RPC protocol yet
        # This is a placeholder for when it's added
        raise NotImplementedError("Reopen operation not yet supported via daemon")

    async def list_issues(self, params: Optional[ListIssuesParams] = None) -> List[Issue]:
        """List issues with optional filters.

        Args:
            params: List filter parameters (optional)

        Returns:
            List of matching issues
        """
        params = params or ListIssuesParams()
        args: Dict[str, Any] = {}
        if params.status:
            args["status"] = params.status
        if params.priority is not None:
            args["priority"] = params.priority
        if params.issue_type:
            args["issue_type"] = params.issue_type
        if params.assignee:
            args["assignee"] = params.assignee
        if params.labels:
            args["labels"] = params.labels
        if params.labels_any:
            args["labels_any"] = params.labels_any
        if params.query:
            args["query"] = params.query
        if params.unassigned:
            args["unassigned"] = params.unassigned
        if params.limit:
            args["limit"] = params.limit

        data = await self._send_request("list", args)
        issues_data = json.loads(data) if isinstance(data, str) else data
        if issues_data is None:
            return []
        return [Issue(**issue) for issue in issues_data]

    async def show(self, params: ShowIssueParams) -> Issue:
        """Show detailed issue information.

        Args:
            params: Show parameters with issue_id

        Returns:
            Issue details
        """
        args = {"id": params.issue_id}
        data = await self._send_request("show", args)
        return Issue(**(json.loads(data) if isinstance(data, str) else data))

    async def ready(self, params: Optional[ReadyWorkParams] = None) -> List[Issue]:
        """Get ready work (issues with no blockers).

        Args:
            params: Ready work filter parameters (optional)

        Returns:
            List of ready issues
        """
        params = params or ReadyWorkParams()
        args: Dict[str, Any] = {}
        if params.assignee:
            args["assignee"] = params.assignee
        if params.priority is not None:
            args["priority"] = params.priority
        if params.labels:
            args["labels"] = params.labels
        if params.labels_any:
            args["labels_any"] = params.labels_any
        if params.unassigned:
            args["unassigned"] = params.unassigned
        if params.sort_policy:
            args["sort_policy"] = params.sort_policy
        if params.limit:
            args["limit"] = params.limit
        # Parent filtering (descendants of a bead/epic)
        if params.parent_id:
            args["parent_id"] = params.parent_id

        data = await self._send_request("ready", args)
        issues_data = json.loads(data) if isinstance(data, str) else data
        if issues_data is None:
            return []
        return [Issue(**issue) for issue in issues_data]

    async def stats(self) -> Stats:
        """Get repository statistics.

        Returns:
            Statistics object
        """
        data = await self._send_request("stats", {})
        stats_data = json.loads(data) if isinstance(data, str) else data
        if stats_data is None:
            stats_data = {}
        return Stats(**stats_data)

    async def blocked(self, params: Optional[BlockedParams] = None) -> List[BlockedIssue]:
        """Get blocked issues.

        Args:
            params: Query parameters (optional)

        Returns:
            List of blocked issues with their blockers
        """
        params = params or BlockedParams()
        args: Dict[str, Any] = {}
        if params.parent_id:
            args["parent_id"] = params.parent_id

        data = await self._send_request("blocked", args)
        issues_data = json.loads(data) if isinstance(data, str) else data
        if issues_data is None:
            return []
        return [BlockedIssue(**issue) for issue in issues_data]

    async def inspect_migration(self) -> dict[str, Any]:
        """Get migration plan and database state for agent analysis.

        Returns:
            Migration plan dict with registered_migrations, warnings, etc.

        Note:
            This falls back to CLI since migrations are rare operations
        """
        raise NotImplementedError("inspect_migration not supported via daemon - use CLI client")

    async def get_schema_info(self) -> dict[str, Any]:
        """Get current database schema for inspection.

        Returns:
            Schema info dict with tables, version, config, sample IDs, etc.

        Note:
            This falls back to CLI since schema inspection is a rare operation
        """
        raise NotImplementedError("get_schema_info not supported via daemon - use CLI client")

    async def repair_deps(self, fix: bool = False) -> dict[str, Any]:
        """Find and optionally fix orphaned dependency references.

        Args:
            fix: If True, automatically remove orphaned dependencies

        Returns:
            Dict with orphans_found, orphans list, and fixed count if fix=True

        Note:
            This falls back to CLI since repair operations are rare
        """
        raise NotImplementedError("repair_deps not supported via daemon - use CLI client")

    async def detect_pollution(self, clean: bool = False) -> dict[str, Any]:
        """Detect test issues that leaked into production database.

        Args:
            clean: If True, delete detected test issues

        Returns:
            Dict with detected test issues and deleted count if clean=True

        Note:
            This falls back to CLI since pollution detection is a rare operation
        """
        raise NotImplementedError("detect_pollution not supported via daemon - use CLI client")

    async def validate(self, checks: str | None = None, fix_all: bool = False) -> dict[str, Any]:
        """Run database validation checks.

        Args:
            checks: Comma-separated list of checks (orphans,duplicates,pollution,conflicts)
            fix_all: If True, auto-fix all fixable issues

        Returns:
            Dict with validation results for each check

        Note:
            This falls back to CLI since validation is a rare operation
        """
        raise NotImplementedError("validate not supported via daemon - use CLI client")

    async def add_dependency(self, params: AddDependencyParams) -> None:
        """Add a dependency between issues.

        Args:
            params: Dependency parameters
        """
        args = {
            "from_id": params.issue_id,
            "to_id": params.depends_on_id,
            "dep_type": params.dep_type,
        }
        await self._send_request("dep_add", args)

    async def is_daemon_running(self) -> bool:
        """Check if daemon is running.

        Returns:
            True if daemon is running and responsive
        """
        try:
            await self.ping()
            return True
        except (DaemonNotRunningError, DaemonConnectionError, DaemonError):
            return False

    def cleanup(self) -> None:
        """Close daemon client connections and cleanup resources.
        
        This is called during MCP server shutdown to ensure clean termination.
        Since we use asyncio.open_unix_connection which closes per-request,
        there's no persistent connection to close. This method is a no-op
        but exists for API consistency.
        """
        # No persistent connections to close - each request opens/closes its own
        pass
