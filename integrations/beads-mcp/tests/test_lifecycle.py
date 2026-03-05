"""Tests for MCP server lifecycle management."""

import asyncio
import signal
import sys
from unittest.mock import MagicMock, patch

import pytest


def test_cleanup_handlers_registered():
    """Test that cleanup handlers are registered on server import."""
    # Server is already imported, so handlers are already registered
    # We can verify the cleanup function and signal handler exist
    import beads_mcp.server as server

    # Verify cleanup function exists and is callable
    assert hasattr(server, 'cleanup')
    assert callable(server.cleanup)

    # Verify signal handler exists and is callable
    assert hasattr(server, 'signal_handler')
    assert callable(server.signal_handler)

    # Verify global state exists
    assert hasattr(server, '_cleanup_done')


def test_cleanup_function_safe_to_call_multiple_times():
    """Test that cleanup function can be called multiple times safely."""
    from beads_mcp.server import cleanup

    import beads_mcp.server as server
    server._cleanup_done = False

    # Call cleanup multiple times - should not raise
    cleanup()
    cleanup()
    cleanup()


def test_signal_handler_calls_cleanup():
    """Test that signal handler calls cleanup and exits."""
    from beads_mcp.server import signal_handler

    with patch('beads_mcp.server.cleanup') as mock_cleanup:
        with patch('sys.exit') as mock_exit:
            # Call signal handler
            signal_handler(signal.SIGTERM, None)

            # Verify cleanup was called
            assert mock_cleanup.called

            # Verify exit was called
            assert mock_exit.called


def test_cleanup_logs_lifecycle_events(caplog):
    """Test that cleanup logs informative messages."""
    import logging
    from beads_mcp.server import cleanup

    # Reset state
    import beads_mcp.server as server
    server._cleanup_done = False

    with caplog.at_level(logging.INFO):
        cleanup()

    # Check for lifecycle log messages
    log_messages = [record.message for record in caplog.records]
    assert any("Cleaning up" in msg for msg in log_messages)
    assert any("Cleanup complete" in msg for msg in log_messages)


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
