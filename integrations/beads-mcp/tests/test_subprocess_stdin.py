"""Tests for subprocess stdin handling to prevent MCP protocol interference.

When running as an MCP server, subprocesses must NOT inherit stdin because:
1. MCP uses stdin for JSON-RPC protocol communication
2. Inherited stdin causes subprocesses to block waiting for input
3. Subprocesses may steal bytes from the MCP protocol stream

These tests verify that all subprocess calls use stdin=DEVNULL.
"""

import asyncio
import subprocess
import sys
from unittest.mock import AsyncMock, MagicMock, call, patch

import pytest


class TestBdClientSubprocessStdin:
    """Test that BdClient subprocess calls don't inherit stdin."""

    @pytest.fixture
    def bd_client(self):
        """Create a BdClient instance for testing."""
        from beads_mcp.bd_client import BdClient
        return BdClient(bd_path="/usr/bin/bd", beads_db="/tmp/test.db")

    @pytest.fixture
    def mock_process(self):
        """Create a mock subprocess process."""
        process = MagicMock()
        process.returncode = 0
        process.communicate = AsyncMock(return_value=(b'{"id": "test-1"}', b""))
        return process

    @pytest.mark.asyncio
    async def test_run_command_uses_devnull_stdin(self, bd_client, mock_process):
        """Test that _run_command passes stdin=DEVNULL to prevent MCP stdin inheritance."""
        with patch("asyncio.create_subprocess_exec", return_value=mock_process) as mock_exec:
            await bd_client._run_command("show", "test-1")

            # Verify stdin=DEVNULL was passed
            mock_exec.assert_called_once()
            call_kwargs = mock_exec.call_args.kwargs
            assert call_kwargs.get("stdin") == asyncio.subprocess.DEVNULL, (
                "subprocess must use stdin=DEVNULL to prevent inheriting MCP's stdin stream"
            )

    @pytest.mark.asyncio
    async def test_check_version_uses_devnull_stdin(self, bd_client, mock_process):
        """Test that _check_version passes stdin=DEVNULL."""
        mock_process.communicate = AsyncMock(return_value=(b"bd version 0.9.5", b""))

        with patch("asyncio.create_subprocess_exec", return_value=mock_process) as mock_exec:
            await bd_client._check_version()

            mock_exec.assert_called_once()
            call_kwargs = mock_exec.call_args.kwargs
            assert call_kwargs.get("stdin") == asyncio.subprocess.DEVNULL, (
                "subprocess must use stdin=DEVNULL to prevent inheriting MCP's stdin stream"
            )

    @pytest.mark.asyncio
    async def test_add_dependency_uses_devnull_stdin(self, bd_client, mock_process):
        """Test that add_dependency passes stdin=DEVNULL."""
        from beads_mcp.models import AddDependencyParams

        with patch("asyncio.create_subprocess_exec", return_value=mock_process) as mock_exec:
            params = AddDependencyParams(issue_id="bd-2", depends_on_id="bd-1", dep_type="blocks")
            await bd_client.add_dependency(params)

            mock_exec.assert_called_once()
            call_kwargs = mock_exec.call_args.kwargs
            assert call_kwargs.get("stdin") == asyncio.subprocess.DEVNULL, (
                "subprocess must use stdin=DEVNULL to prevent inheriting MCP's stdin stream"
            )

    @pytest.mark.asyncio
    async def test_quickstart_uses_devnull_stdin(self, bd_client, mock_process):
        """Test that quickstart passes stdin=DEVNULL."""
        mock_process.communicate = AsyncMock(return_value=(b"# Quickstart guide", b""))

        with patch("asyncio.create_subprocess_exec", return_value=mock_process) as mock_exec:
            await bd_client.quickstart()

            mock_exec.assert_called_once()
            call_kwargs = mock_exec.call_args.kwargs
            assert call_kwargs.get("stdin") == asyncio.subprocess.DEVNULL, (
                "subprocess must use stdin=DEVNULL to prevent inheriting MCP's stdin stream"
            )

    @pytest.mark.asyncio
    async def test_init_uses_devnull_stdin(self, bd_client, mock_process):
        """Test that init passes stdin=DEVNULL."""
        mock_process.communicate = AsyncMock(return_value=(b"Initialized!", b""))

        with patch("asyncio.create_subprocess_exec", return_value=mock_process) as mock_exec:
            await bd_client.init()

            mock_exec.assert_called_once()
            call_kwargs = mock_exec.call_args.kwargs
            assert call_kwargs.get("stdin") == asyncio.subprocess.DEVNULL, (
                "subprocess must use stdin=DEVNULL to prevent inheriting MCP's stdin stream"
            )


class TestServerSubprocessStdin:
    """Test that server.py subprocess calls don't inherit stdin."""

    def test_resolve_workspace_root_uses_devnull_stdin(self):
        """Test that _resolve_workspace_root passes stdin=DEVNULL to git subprocess."""
        from beads_mcp.server import _resolve_workspace_root

        with patch("subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0, stdout="/repo/root\n")

            _resolve_workspace_root("/some/path")

            mock_run.assert_called_once()
            call_kwargs = mock_run.call_args.kwargs
            assert call_kwargs.get("stdin") == subprocess.DEVNULL, (
                "git subprocess must use stdin=DEVNULL to prevent inheriting MCP's stdin stream"
            )


class TestToolsSubprocessStdin:
    """Test that tools.py subprocess calls don't inherit stdin."""

    def test_tools_resolve_workspace_root_uses_devnull_stdin(self):
        """Test that tools._resolve_workspace_root passes stdin=DEVNULL to git subprocess."""
        from beads_mcp.tools import _resolve_workspace_root

        with patch("beads_mcp.tools.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(returncode=0, stdout="/repo/root\n")

            _resolve_workspace_root("/some/path")

            mock_run.assert_called_once()
            call_kwargs = mock_run.call_args.kwargs
            assert call_kwargs.get("stdin") == subprocess.DEVNULL, (
                "git subprocess must use stdin=DEVNULL to prevent inheriting MCP's stdin stream"
            )
