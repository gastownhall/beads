"""Tests for daemon health check and reconnection logic."""

import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from beads_mcp.bd_client import BdError
from beads_mcp.bd_daemon_client import (
    BdDaemonClient,
    DaemonConnectionError,
    DaemonError,
    DaemonNotRunningError,
)
from beads_mcp.tools import _get_client, _health_check_client, _reconnect_client


@pytest.mark.asyncio
async def test_daemon_client_ping_success():
    """Test successful ping to daemon."""
    client = BdDaemonClient(socket_path="/tmp/bd.sock", working_dir="/tmp/test")
    
    with patch.object(client, '_send_request', new_callable=AsyncMock) as mock_send:
        mock_send.return_value = {"message": "pong", "version": "0.9.10"}
        
        result = await client.ping()
        
        assert result["message"] == "pong"
        assert result["version"] == "0.9.10"
        mock_send.assert_called_once_with("ping", {})


@pytest.mark.asyncio
async def test_daemon_client_ping_connection_error():
    """Test ping when daemon connection fails."""
    client = BdDaemonClient(socket_path="/tmp/bd.sock", working_dir="/tmp/test")
    
    with patch.object(client, '_send_request', new_callable=AsyncMock) as mock_send:
        mock_send.side_effect = DaemonConnectionError("Connection failed")
        
        with pytest.raises(DaemonConnectionError):
            await client.ping()


@pytest.mark.asyncio
async def test_daemon_client_health_success():
    """Test successful health check to daemon."""
    client = BdDaemonClient(socket_path="/tmp/bd.sock", working_dir="/tmp/test")
    
    with patch.object(client, '_send_request', new_callable=AsyncMock) as mock_send:
        mock_send.return_value = {
            "status": "healthy",
            "version": "0.9.10",
            "uptime": 123.45,
            "db_response_time_ms": 2.5,
            "active_connections": 3,
            "memory_bytes": 104857600,
        }
        
        result = await client.health()
        
        assert result["status"] == "healthy"
        assert result["version"] == "0.9.10"
        assert result["uptime"] == 123.45
        mock_send.assert_called_once_with("health", {})


@pytest.mark.asyncio
async def test_daemon_client_health_unhealthy():
    """Test health check when daemon is unhealthy."""
    client = BdDaemonClient(socket_path="/tmp/bd.sock", working_dir="/tmp/test")
    
    with patch.object(client, '_send_request', new_callable=AsyncMock) as mock_send:
        mock_send.return_value = {
            "status": "unhealthy",
            "error": "Database connection failed",
        }
        
        result = await client.health()
        
        assert result["status"] == "unhealthy"
        assert "error" in result


@pytest.mark.asyncio
async def test_health_check_client_daemon_client_healthy():
    """Test health check for healthy daemon client."""
    client = BdDaemonClient(socket_path="/tmp/bd.sock", working_dir="/tmp/test")
    
    with patch.object(client, 'ping', new_callable=AsyncMock) as mock_ping:
        mock_ping.return_value = {"message": "pong", "version": "0.9.10"}
        
        result = await _health_check_client(client)
        
        assert result is True
        mock_ping.assert_called_once()


@pytest.mark.asyncio
async def test_health_check_client_daemon_client_unhealthy():
    """Test health check for unhealthy daemon client."""
    client = BdDaemonClient(socket_path="/tmp/bd.sock", working_dir="/tmp/test")
    
    with patch.object(client, 'ping', new_callable=AsyncMock) as mock_ping:
        mock_ping.side_effect = DaemonConnectionError("Connection failed")
        
        result = await _health_check_client(client)
        
        assert result is False


@pytest.mark.asyncio
async def test_health_check_client_cli_client():
    """Test health check for CLI client (always returns True)."""
    from beads_mcp.bd_client import BdClient
    
    client = BdClient(bd_path="/usr/bin/bd", beads_db="/tmp/test.db")
    
    result = await _health_check_client(client)
    
    # CLI clients don't have ping, so they're always considered healthy
    assert result is True


@pytest.mark.asyncio
async def test_reconnect_client_success():
    """Test successful reconnection after failure."""
    from beads_mcp.bd_client import create_bd_client
    
    with (
        patch('beads_mcp.tools.create_bd_client') as mock_create,
        patch('beads_mcp.tools._health_check_client', new_callable=AsyncMock) as mock_health,
        patch('beads_mcp.tools._register_client_for_cleanup') as mock_register,
    ):
        mock_client = MagicMock()
        mock_create.return_value = mock_client
        mock_health.return_value = True
        
        result = await _reconnect_client("/tmp/test")
        
        assert result == mock_client
        mock_create.assert_called_once_with(prefer_daemon=True, working_dir="/tmp/test")
        mock_register.assert_called_once_with(mock_client)


@pytest.mark.asyncio
async def test_reconnect_client_retry_with_backoff():
    """Test reconnection with exponential backoff on failure."""
    # Need to patch asyncio.sleep in the actual module where it's called
    import beads_mcp.tools as tools_module
    
    with (
        patch.object(tools_module, 'create_bd_client') as mock_create,
        patch.object(tools_module, '_health_check_client', new_callable=AsyncMock) as mock_health,
        patch.object(tools_module, '_register_client_for_cleanup') as mock_register,
    ):
        mock_client = MagicMock()
        
        # Raise exception first two times, succeed third time
        mock_create.side_effect = [
            Exception("Connection failed"),
            Exception("Connection failed"),
            mock_client,
        ]
        mock_health.return_value = True
        
        # Mock asyncio.sleep to track calls
        sleep_calls = []
        async def mock_sleep(duration):
            sleep_calls.append(duration)
            # Don't actually sleep in tests
            return
        
        with patch.object(asyncio, 'sleep', side_effect=mock_sleep):
            result = await _reconnect_client("/tmp/test", max_retries=3)
        
        assert result == mock_client
        assert mock_create.call_count == 3
        assert len(sleep_calls) == 2
        
        # Verify exponential backoff: 0.1s, 0.2s
        assert sleep_calls[0] == 0.1
        assert sleep_calls[1] == 0.2


@pytest.mark.asyncio
async def test_reconnect_client_max_retries_exceeded():
    """Test reconnection failure after max retries."""
    with (
        patch('beads_mcp.tools.create_bd_client') as mock_create,
        patch('beads_mcp.tools._health_check_client', new_callable=AsyncMock) as mock_health,
        patch('asyncio.sleep', new_callable=AsyncMock),
    ):
        mock_client = MagicMock()
        mock_create.return_value = mock_client
        mock_health.return_value = False  # Always fail health check
        
        with pytest.raises(BdError, match="Failed to connect to daemon after 3 attempts"):
            await _reconnect_client("/tmp/test", max_retries=3)
        
        assert mock_create.call_count == 3


@pytest.mark.asyncio
async def test_get_client_uses_cached_healthy_client(monkeypatch):
    """Test that _get_client returns cached client if healthy."""
    from beads_mcp import tools
    
    # Set up environment
    monkeypatch.setenv("BEADS_WORKING_DIR", "/tmp/test")
    
    mock_client = MagicMock()
    mock_client._check_version = AsyncMock()
    
    with (
        patch('beads_mcp.tools._canonicalize_path', return_value="/tmp/test"),
        patch('beads_mcp.tools._health_check_client', new_callable=AsyncMock) as mock_health,
    ):
        mock_health.return_value = True
        
        # Add mock client to pool and mark as version checked
        tools._connection_pool["/tmp/test"] = mock_client
        tools._version_checked.add("/tmp/test")
        
        result = await _get_client()
        
        assert result == mock_client
        mock_health.assert_called_once_with(mock_client)


@pytest.mark.asyncio
async def test_get_client_reconnects_on_stale_connection(monkeypatch):
    """Test that _get_client reconnects when cached client is stale."""
    from beads_mcp import tools
    
    # Set up environment
    monkeypatch.setenv("BEADS_WORKING_DIR", "/tmp/test")
    
    old_client = MagicMock()
    new_client = MagicMock()
    new_client._check_version = AsyncMock()
    
    with (
        patch('beads_mcp.tools._canonicalize_path', return_value="/tmp/test"),
        patch('beads_mcp.tools._health_check_client', new_callable=AsyncMock) as mock_health,
        patch('beads_mcp.tools._reconnect_client', new_callable=AsyncMock) as mock_reconnect,
    ):
        # First health check fails (stale), reconnect returns new client
        mock_health.return_value = False
        mock_reconnect.return_value = new_client
        
        # Add old client to pool
        tools._connection_pool["/tmp/test"] = old_client
        tools._version_checked.add("/tmp/test")
        
        result = await _get_client()
        
        assert result == new_client
        assert tools._connection_pool["/tmp/test"] == new_client
        # Version check is performed after reconnect, so it's back in the set
        assert "/tmp/test" in tools._version_checked
        mock_reconnect.assert_called_once_with("/tmp/test")


@pytest.mark.asyncio
async def test_get_client_creates_new_client_if_not_cached(monkeypatch):
    """Test that _get_client creates new client if not in pool."""
    from beads_mcp import tools
    
    # Clear pool
    tools._connection_pool.clear()
    tools._version_checked.clear()
    
    # Set up environment
    monkeypatch.setenv("BEADS_WORKING_DIR", "/tmp/test")
    
    mock_client = MagicMock()
    mock_client._check_version = AsyncMock()
    
    with (
        patch('beads_mcp.tools._canonicalize_path', return_value="/tmp/test"),
        patch('beads_mcp.tools.create_bd_client', return_value=mock_client) as mock_create,
        patch('beads_mcp.tools._register_client_for_cleanup') as mock_register,
    ):
        result = await _get_client()
        
        assert result == mock_client
        assert tools._connection_pool["/tmp/test"] == mock_client
        mock_create.assert_called_once_with(prefer_daemon=True, working_dir="/tmp/test")
        mock_register.assert_called_once_with(mock_client)


@pytest.mark.asyncio
async def test_get_client_no_workspace_error():
    """Test that _get_client raises error if no workspace is set."""
    from beads_mcp import tools
    
    # Clear context
    tools.current_workspace.set(None)
    
    with patch.dict('os.environ', {}, clear=True):
        # Mock auto-detection to fail
        with patch("beads_mcp.tools._find_beads_db_in_tree", return_value=None):
            with pytest.raises(BdError, match="No beads workspace found"):
                await _get_client()
