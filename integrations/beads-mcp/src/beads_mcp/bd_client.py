"""Client for interacting with bd (beads) CLI."""

import asyncio
import json

from .config import load_config
from .models import (
    AddDependencyParams,
    BlockedIssue,
    CloseIssueParams,
    CreateIssueParams,
    InitParams,
    Issue,
    ListIssuesParams,
    ReadyWorkParams,
    ShowIssueParams,
    Stats,
    UpdateIssueParams,
)


class BdError(Exception):
    """Base exception for bd CLI errors."""

    pass


class BdNotFoundError(BdError):
    """Raised when bd command is not found."""

    pass


class BdCommandError(BdError):
    """Raised when bd command fails."""

    stderr: str
    returncode: int

    def __init__(self, message: str, stderr: str = "", returncode: int = 1):
        super().__init__(message)
        self.stderr = stderr
        self.returncode = returncode


class BdClient:
    """Client for calling bd CLI commands and parsing JSON output."""

    bd_path: str
    beads_db: str | None
    actor: str | None
    no_auto_flush: bool
    no_auto_import: bool

    def __init__(
        self,
        bd_path: str | None = None,
        beads_db: str | None = None,
        actor: str | None = None,
        no_auto_flush: bool | None = None,
        no_auto_import: bool | None = None,
    ):
        """Initialize bd client.

        Args:
            bd_path: Path to bd executable (optional, loads from config if not provided)
            beads_db: Path to beads database file (optional, loads from config if not provided)
            actor: Actor name for audit trail (optional, loads from config if not provided)
            no_auto_flush: Disable automatic JSONL sync (optional, loads from config if not provided)
            no_auto_import: Disable automatic JSONL import (optional, loads from config if not provided)
        """
        config = load_config()
        self.bd_path = bd_path if bd_path is not None else config.beads_path
        self.beads_db = beads_db if beads_db is not None else config.beads_db
        self.actor = actor if actor is not None else config.beads_actor
        self.no_auto_flush = (
            no_auto_flush if no_auto_flush is not None else config.beads_no_auto_flush
        )
        self.no_auto_import = (
            no_auto_import if no_auto_import is not None else config.beads_no_auto_import
        )

    def _global_flags(self) -> list[str]:
        """Build list of global flags for bd commands.

        Returns:
            List of global flag arguments
        """
        flags = []
        if self.beads_db:
            flags.extend(["--db", self.beads_db])
        if self.actor:
            flags.extend(["--actor", self.actor])
        if self.no_auto_flush:
            flags.append("--no-auto-flush")
        if self.no_auto_import:
            flags.append("--no-auto-import")
        return flags

    async def _run_command(self, *args: str) -> object:
        """Run bd command and parse JSON output.

        Args:
            *args: Command arguments to pass to bd

        Returns:
            Parsed JSON output (dict or list)

        Raises:
            BdNotFoundError: If bd command not found
            BdCommandError: If bd command fails
        """
        cmd = [self.bd_path, *args, *self._global_flags(), "--json"]

        try:
            process = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await process.communicate()
        except FileNotFoundError as e:
            raise BdNotFoundError(
                f"bd command not found at '{self.bd_path}'. Make sure bd is installed and in PATH."
            ) from e

        if process.returncode != 0:
            raise BdCommandError(
                f"bd command failed: {stderr.decode()}",
                stderr=stderr.decode(),
                returncode=process.returncode or 1,
            )

        stdout_str = stdout.decode().strip()
        if not stdout_str:
            return {}

        try:
            result: object = json.loads(stdout_str)
            return result
        except json.JSONDecodeError as e:
            raise BdCommandError(
                f"Failed to parse bd JSON output: {e}",
                stderr=stdout_str,
            ) from e

    async def ready(self, params: ReadyWorkParams | None = None) -> list[Issue]:
        """Get ready work (issues with no blocking dependencies).

        Args:
            params: Query parameters

        Returns:
            List of ready issues
        """
        params = params or ReadyWorkParams()
        args = ["ready", "--limit", str(params.limit)]

        if params.priority is not None:
            args.extend(["--priority", str(params.priority)])
        if params.assignee:
            args.extend(["--assignee", params.assignee])

        data = await self._run_command(*args)
        if not isinstance(data, list):
            return []

        return [Issue.model_validate(issue) for issue in data]

    async def list_issues(self, params: ListIssuesParams | None = None) -> list[Issue]:
        """List issues with optional filters.

        Args:
            params: Query parameters

        Returns:
            List of issues
        """
        params = params or ListIssuesParams()
        args = ["list"]

        if params.status:
            args.extend(["--status", params.status])
        if params.priority is not None:
            args.extend(["--priority", str(params.priority)])
        if params.issue_type:
            args.extend(["--type", params.issue_type])
        if params.assignee:
            args.extend(["--assignee", params.assignee])
        if params.limit:
            args.extend(["--limit", str(params.limit)])

        data = await self._run_command(*args)
        if not isinstance(data, list):
            return []

        return [Issue.model_validate(issue) for issue in data]

    async def show(self, params: ShowIssueParams) -> Issue:
        """Show issue details.

        Args:
            params: Issue ID to show

        Returns:
            Issue details

        Raises:
            BdCommandError: If issue not found
        """
        data = await self._run_command("show", params.issue_id)
        if not isinstance(data, dict):
            raise BdCommandError(f"Invalid response for show {params.issue_id}")

        return Issue.model_validate(data)

    async def create(self, params: CreateIssueParams) -> Issue:
        """Create a new issue.

        Args:
            params: Issue creation parameters

        Returns:
            Created issue
        """
        args = ["create", params.title, "-p", str(params.priority), "-t", params.issue_type]

        if params.description:
            args.extend(["-d", params.description])
        if params.design:
            args.extend(["--design", params.design])
        if params.acceptance:
            args.extend(["--acceptance", params.acceptance])
        if params.external_ref:
            args.extend(["--external-ref", params.external_ref])
        if params.assignee:
            args.extend(["--assignee", params.assignee])
        if params.id:
            args.extend(["--id", params.id])
        for label in params.labels:
            args.extend(["-l", label])
        if params.deps:
            args.extend(["--deps", ",".join(params.deps)])

        data = await self._run_command(*args)
        if not isinstance(data, dict):
            raise BdCommandError("Invalid response for create")

        return Issue.model_validate(data)

    async def update(self, params: UpdateIssueParams) -> Issue:
        """Update an issue.

        Args:
            params: Issue update parameters

        Returns:
            Updated issue
        """
        args = ["update", params.issue_id]

        if params.status:
            args.extend(["--status", params.status])
        if params.priority is not None:
            args.extend(["--priority", str(params.priority)])
        if params.assignee:
            args.extend(["--assignee", params.assignee])
        if params.title:
            args.extend(["--title", params.title])
        if params.design:
            args.extend(["--design", params.design])
        if params.acceptance_criteria:
            args.extend(["--acceptance-criteria", params.acceptance_criteria])
        if params.notes:
            args.extend(["--notes", params.notes])
        if params.external_ref:
            args.extend(["--external-ref", params.external_ref])

        data = await self._run_command(*args)
        if not isinstance(data, dict):
            raise BdCommandError(f"Invalid response for update {params.issue_id}")

        return Issue.model_validate(data)

    async def close(self, params: CloseIssueParams) -> list[Issue]:
        """Close an issue.

        Args:
            params: Close parameters

        Returns:
            List containing closed issue
        """
        args = ["close", params.issue_id, "--reason", params.reason]

        data = await self._run_command(*args)
        if not isinstance(data, list):
            raise BdCommandError(f"Invalid response for close {params.issue_id}")

        return [Issue.model_validate(issue) for issue in data]

    async def add_dependency(self, params: AddDependencyParams) -> None:
        """Add a dependency between issues.

        Args:
            params: Dependency parameters
        """
        # bd dep add doesn't return JSON, just prints confirmation
        cmd = [
            self.bd_path,
            "dep",
            "add",
            params.from_id,
            params.to_id,
            "--type",
            params.dep_type,
            *self._global_flags(),
        ]

        try:
            process = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            _stdout, stderr = await process.communicate()
        except FileNotFoundError as e:
            raise BdNotFoundError(
                f"bd command not found at '{self.bd_path}'. Make sure bd is installed and in PATH."
            ) from e

        if process.returncode != 0:
            raise BdCommandError(
                f"bd dep add failed: {stderr.decode()}",
                stderr=stderr.decode(),
                returncode=process.returncode or 1,
            )

    async def quickstart(self) -> str:
        """Get bd quickstart guide.

        Returns:
            Quickstart guide text
        """
        cmd = [self.bd_path, "quickstart"]

        try:
            process = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await process.communicate()
        except FileNotFoundError as e:
            raise BdNotFoundError(
                f"bd command not found at '{self.bd_path}'. Make sure bd is installed and in PATH."
            ) from e

        if process.returncode != 0:
            raise BdCommandError(
                f"bd quickstart failed: {stderr.decode()}",
                stderr=stderr.decode(),
                returncode=process.returncode or 1,
            )

        return stdout.decode()

    async def stats(self) -> Stats:
        """Get statistics about issues.

        Returns:
            Statistics object
        """
        data = await self._run_command("stats")
        if not isinstance(data, dict):
            raise BdCommandError("Invalid response for stats")

        return Stats.model_validate(data)

    async def blocked(self) -> list[BlockedIssue]:
        """Get blocked issues.

        Returns:
            List of blocked issues with blocking information
        """
        data = await self._run_command("blocked")
        if not isinstance(data, list):
            return []

        return [BlockedIssue.model_validate(issue) for issue in data]

    async def init(self, params: InitParams | None = None) -> str:
        """Initialize bd in current directory.

        Args:
            params: Initialization parameters

        Returns:
            Initialization output message
        """
        params = params or InitParams()
        cmd = [self.bd_path, "init"]

        if params.prefix:
            cmd.extend(["--prefix", params.prefix])

        cmd.extend(self._global_flags())

        try:
            process = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await process.communicate()
        except FileNotFoundError as e:
            raise BdNotFoundError(
                f"bd command not found at '{self.bd_path}'. Make sure bd is installed and in PATH."
            ) from e

        if process.returncode != 0:
            raise BdCommandError(
                f"bd init failed: {stderr.decode()}",
                stderr=stderr.decode(),
                returncode=process.returncode or 1,
            )

        return stdout.decode()
