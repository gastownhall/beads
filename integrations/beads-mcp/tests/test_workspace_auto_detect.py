"""Test workspace auto-detection from CWD (bd-8zf2)."""

import os
import pytest
import tempfile
from pathlib import Path
from unittest.mock import AsyncMock, patch

from beads_mcp.tools import _find_beads_db_in_tree, _get_client, current_workspace
from beads_mcp.bd_client import BdError


def test_find_beads_db_in_tree_direct():
    """Test finding .beads in current directory."""
    with tempfile.TemporaryDirectory() as tmpdir:
        # Create .beads/beads.db
        beads_dir = Path(tmpdir) / ".beads"
        beads_dir.mkdir()
        (beads_dir / "beads.db").touch()
        
        # Should find workspace root (use realpath for macOS symlink resolution)
        result = _find_beads_db_in_tree(tmpdir)
        assert result == os.path.realpath(tmpdir)


def test_find_beads_db_in_tree_parent():
    """Test finding .beads in parent directory."""
    with tempfile.TemporaryDirectory() as tmpdir:
        # Create .beads/beads.db in root
        beads_dir = Path(tmpdir) / ".beads"
        beads_dir.mkdir()
        (beads_dir / "beads.db").touch()
        
        # Create subdirectory
        subdir = Path(tmpdir) / "subdir" / "deep"
        subdir.mkdir(parents=True)
        
        # Should find workspace root (walks up from subdir)
        result = _find_beads_db_in_tree(str(subdir))
        assert result == os.path.realpath(tmpdir)


def test_find_beads_db_in_tree_not_found():
    """Test when no .beads directory exists."""
    with tempfile.TemporaryDirectory() as tmpdir:
        # No .beads directory
        result = _find_beads_db_in_tree(tmpdir)
        assert result is None


def test_find_beads_db_excludes_backups():
    """Test that backup .db files are ignored."""
    with tempfile.TemporaryDirectory() as tmpdir:
        beads_dir = Path(tmpdir) / ".beads"
        beads_dir.mkdir()
        
        # Only backup file exists
        (beads_dir / "beads.db.backup").touch()
        
        result = _find_beads_db_in_tree(tmpdir)
        assert result is None  # Should not find backup files
        
        # Add valid db file
        (beads_dir / "beads.db").touch()
        result = _find_beads_db_in_tree(tmpdir)
        assert result == os.path.realpath(tmpdir)


@pytest.mark.asyncio
async def test_get_client_auto_detect_from_cwd():
    """Test that _get_client() auto-detects workspace from CWD."""
    with tempfile.TemporaryDirectory() as tmpdir:
        # Create .beads/beads.db
        beads_dir = Path(tmpdir) / ".beads"
        beads_dir.mkdir()
        (beads_dir / "beads.db").touch()
        
        # Reset ContextVar for this test
        token = current_workspace.set(None)
        try:
            with patch.dict(os.environ, {}, clear=True):
                # Mock _find_beads_db_in_tree to return our tmpdir
                with patch("beads_mcp.tools._find_beads_db_in_tree", return_value=tmpdir):
                    # Mock create_bd_client to avoid actual connection
                    mock_client = AsyncMock()
                    mock_client.ping = AsyncMock(return_value=None)
                    
                    with patch("beads_mcp.tools.create_bd_client", return_value=mock_client):
                        # Should auto-detect and not raise error
                        client = await _get_client()
                        assert client is not None
        finally:
            current_workspace.reset(token)


@pytest.mark.asyncio
async def test_get_client_no_workspace_found():
    """Test that _get_client() raises helpful error when no workspace found."""
    # Reset ContextVar for this test
    token = current_workspace.set(None)
    try:
        with patch.dict(os.environ, {}, clear=True):
            with patch("beads_mcp.tools._find_beads_db_in_tree", return_value=None):
                with pytest.raises(BdError) as exc_info:
                    await _get_client()
                
                # Verify error message is helpful
                error_msg = str(exc_info.value)
                assert "No beads workspace found" in error_msg
                assert "set_context" in error_msg
                assert ".beads/" in error_msg
    finally:
        current_workspace.reset(token)


@pytest.mark.asyncio
async def test_get_client_prefers_context_var_over_auto_detect():
    """Test that explicit workspace_root parameter takes precedence."""
    explicit_workspace = "/explicit/path"
    
    token = current_workspace.set(explicit_workspace)
    try:
        with patch("beads_mcp.tools._canonicalize_path", return_value=explicit_workspace):
            mock_client = AsyncMock()
            mock_client.ping = AsyncMock(return_value=None)
            
            with patch("beads_mcp.tools.create_bd_client", return_value=mock_client) as mock_create:
                client = await _get_client()
                
                # Should use explicit workspace, not auto-detect
                mock_create.assert_called_once()
                # The working_dir parameter should be the canonicalized explicit path
                assert mock_create.call_args[1]["working_dir"] == explicit_workspace
    finally:
        current_workspace.reset(token)


@pytest.mark.asyncio  
async def test_get_client_env_var_over_auto_detect():
    """Test that BEADS_WORKING_DIR env var takes precedence over auto-detect."""
    env_workspace = "/env/path"
    
    token = current_workspace.set(None)
    try:
        with patch.dict(os.environ, {"BEADS_WORKING_DIR": env_workspace}):
            with patch("beads_mcp.tools._canonicalize_path", return_value=env_workspace):
                mock_client = AsyncMock()
                mock_client.ping = AsyncMock(return_value=None)
                
                with patch("beads_mcp.tools.create_bd_client", return_value=mock_client):
                    client = await _get_client()
                    
                    # Should use env var, not call auto-detect
                    assert client is not None
    finally:
        current_workspace.reset(token)
