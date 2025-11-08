"""Integration tests for multi-project MCP context switching.

Tests verify:
- Concurrent multi-project calls work correctly
- No cross-project data leakage
- Path canonicalization handles symlinks and submodules
- Connection pool prevents race conditions
"""

import asyncio
import os
import tempfile
from pathlib import Path
from unittest.mock import AsyncMock, patch

import pytest

from beads_mcp.tools import (
    _canonicalize_path,
    _connection_pool,
    _pool_lock,
    _resolve_workspace_root,
    beads_create_issue,
    beads_list_issues,
    beads_ready_work,
    current_workspace,
)


@pytest.fixture(autouse=True)
def reset_connection_pool():
    """Reset connection pool before each test."""
    _connection_pool.clear()
    yield
    _connection_pool.clear()


@pytest.fixture
def temp_projects():
    """Create temporary project directories for testing."""
    with tempfile.TemporaryDirectory() as tmpdir:
        project_a = Path(tmpdir) / "project_a"
        project_b = Path(tmpdir) / "project_b"
        project_c = Path(tmpdir) / "project_c"
        
        project_a.mkdir()
        project_b.mkdir()
        project_c.mkdir()
        
        # Create .beads directories to simulate initialized projects
        (project_a / ".beads").mkdir()
        (project_b / ".beads").mkdir()
        (project_c / ".beads").mkdir()
        
        yield {
            "a": str(project_a),
            "b": str(project_b),
            "c": str(project_c),
        }


@pytest.fixture
def mock_client_factory():
    """Factory for creating mock clients per project."""
    clients = {}
    
    def get_mock_client(workspace_root: str):
        if workspace_root not in clients:
            client = AsyncMock()
            client.ready = AsyncMock(return_value=[])
            client.list_issues = AsyncMock(return_value=[])
            client.create = AsyncMock(return_value={
                "id": f"issue-{len(clients)}",
                "title": "Test",
                "workspace": workspace_root,
            })
            clients[workspace_root] = client
        return clients[workspace_root]
    
    return get_mock_client, clients


class TestConcurrentMultiProject:
    """Test concurrent access to multiple projects."""
    
    @pytest.mark.asyncio
    async def test_concurrent_calls_different_projects(self, temp_projects, mock_client_factory):
        """Test concurrent calls to different projects use different clients."""
        get_mock, clients = mock_client_factory
        
        async def call_ready(workspace: str):
            """Call ready with workspace context set."""
            token = current_workspace.set(workspace)
            try:
                with patch("beads_mcp.tools.create_bd_client", side_effect=lambda **kwargs: get_mock(kwargs["working_dir"])):
                    return await beads_ready_work()
            finally:
                current_workspace.reset(token)
        
        # Call all three projects concurrently
        results = await asyncio.gather(
            call_ready(temp_projects["a"]),
            call_ready(temp_projects["b"]),
            call_ready(temp_projects["c"]),
        )
        
        # Verify we created separate clients for each project
        assert len(clients) == 3
        # Note: paths might be canonicalized (e.g., /var -> /private/var on macOS)
        canonical_a = os.path.realpath(temp_projects["a"])
        canonical_b = os.path.realpath(temp_projects["b"])
        canonical_c = os.path.realpath(temp_projects["c"])
        assert canonical_a in clients
        assert canonical_b in clients
        assert canonical_c in clients
        
        # Verify each client was called
        for client in clients.values():
            client.ready.assert_called_once()
    
    @pytest.mark.asyncio
    async def test_concurrent_calls_same_project_reuse_client(self, temp_projects, mock_client_factory):
        """Test concurrent calls to same project reuse the same client."""
        get_mock, clients = mock_client_factory
        
        async def call_ready(workspace: str):
            """Call ready with workspace context set."""
            token = current_workspace.set(workspace)
            try:
                with patch("beads_mcp.tools.create_bd_client", side_effect=lambda **kwargs: get_mock(kwargs["working_dir"])):
                    return await beads_ready_work()
            finally:
                current_workspace.reset(token)
        
        # Call same project multiple times concurrently
        results = await asyncio.gather(
            call_ready(temp_projects["a"]),
            call_ready(temp_projects["a"]),
            call_ready(temp_projects["a"]),
        )
        
        # Verify only one client created (connection pool working)
        assert len(clients) == 1
        canonical_a = os.path.realpath(temp_projects["a"])
        assert canonical_a in clients
        
        # Verify client was called 3 times
        clients[canonical_a].ready.assert_called()
        assert clients[canonical_a].ready.call_count == 3
    
    @pytest.mark.asyncio
    async def test_pool_lock_prevents_race_conditions(self, temp_projects, mock_client_factory):
        """Test that pool lock prevents race conditions during client creation."""
        get_mock, clients = mock_client_factory
        
        # Track creation count (the lock should ensure only 1)
        creation_count = []
        
        async def call_with_delay(workspace: str):
            """Call ready and track concurrent creation attempts."""
            token = current_workspace.set(workspace)
            try:
                def slow_create(**kwargs):
                    """Slow client creation to expose race conditions."""
                    creation_count.append(1)
                    import time
                    time.sleep(0.01)  # Simulate slow creation
                    return get_mock(kwargs["working_dir"])
                
                with patch("beads_mcp.tools.create_bd_client", side_effect=slow_create):
                    return await beads_ready_work()
            finally:
                current_workspace.reset(token)
        
        # Race: three calls to same project
        await asyncio.gather(
            call_with_delay(temp_projects["a"]),
            call_with_delay(temp_projects["a"]),
            call_with_delay(temp_projects["a"]),
        )
        
        # Pool lock should ensure only one client created
        assert len(clients) == 1
        # Only one creation should have happened (due to pool lock)
        assert len(creation_count) == 1


class TestPathCanonicalization:
    """Test path canonicalization for symlinks and submodules."""
    
    def test_canonicalize_with_beads_dir(self, temp_projects):
        """Test canonicalization prefers local .beads directory."""
        project_a = temp_projects["a"]
        
        # Should return the project path itself (has .beads)
        canonical = _canonicalize_path(project_a)
        assert canonical == os.path.realpath(project_a)
    
    def test_canonicalize_symlink_deduplication(self):
        """Test symlinks to same directory deduplicate to same canonical path."""
        with tempfile.TemporaryDirectory() as tmpdir:
            real_dir = Path(tmpdir) / "real"
            real_dir.mkdir()
            (real_dir / ".beads").mkdir()
            
            symlink = Path(tmpdir) / "symlink"
            symlink.symlink_to(real_dir)
            
            # Both paths should canonicalize to same path
            canonical_real = _canonicalize_path(str(real_dir))
            canonical_symlink = _canonicalize_path(str(symlink))
            
            assert canonical_real == canonical_symlink
            assert canonical_real == str(real_dir.resolve())
    
    def test_canonicalize_submodule_with_beads(self):
        """Test submodule with own .beads uses local directory, not parent."""
        with tempfile.TemporaryDirectory() as tmpdir:
            parent = Path(tmpdir) / "parent"
            parent.mkdir()
            (parent / ".beads").mkdir()
            
            submodule = parent / "submodule"
            submodule.mkdir()
            (submodule / ".beads").mkdir()
            
            # Submodule should use its own .beads, not parent's
            canonical = _canonicalize_path(str(submodule))
            assert canonical == str(submodule.resolve())
            # NOT parent's path
            assert canonical != str(parent.resolve())
    
    def test_canonicalize_no_beads_uses_git_toplevel(self):
        """Test path without .beads falls back to git toplevel."""
        with tempfile.TemporaryDirectory() as tmpdir:
            project = Path(tmpdir) / "project"
            project.mkdir()
            
            # Mock git toplevel to return project dir
            with patch("beads_mcp.tools.subprocess.run") as mock_run:
                mock_run.return_value.returncode = 0
                mock_run.return_value.stdout = str(project)
                
                canonical = _canonicalize_path(str(project))
                
                # Should use git toplevel
                assert canonical == str(project)
                mock_run.assert_called_once()
    
    def test_resolve_workspace_root_git_repo(self):
        """Test _resolve_workspace_root returns git toplevel."""
        with tempfile.TemporaryDirectory() as tmpdir:
            project = Path(tmpdir) / "repo"
            project.mkdir()
            
            with patch("beads_mcp.tools.subprocess.run") as mock_run:
                mock_run.return_value.returncode = 0
                mock_run.return_value.stdout = str(project)
                
                resolved = _resolve_workspace_root(str(project))
                
                assert resolved == str(project)
    
    def test_resolve_workspace_root_not_git(self):
        """Test _resolve_workspace_root falls back to abspath if not git repo."""
        with tempfile.TemporaryDirectory() as tmpdir:
            project = Path(tmpdir) / "not-git"
            project.mkdir()
            
            with patch("beads_mcp.tools.subprocess.run") as mock_run:
                mock_run.return_value.returncode = 1
                mock_run.return_value.stdout = ""
                
                resolved = _resolve_workspace_root(str(project))
                
                # Compare as realpath to handle macOS /var -> /private/var
                assert os.path.realpath(resolved) == os.path.realpath(str(project))


class TestCrossProjectIsolation:
    """Test that projects don't leak data to each other."""
    
    @pytest.mark.asyncio
    async def test_no_cross_project_data_leakage(self, temp_projects, mock_client_factory):
        """Test operations in project A don't affect project B."""
        get_mock, clients = mock_client_factory
        
        # Mock different responses for each project
        canonical_a = os.path.realpath(temp_projects["a"])
        canonical_b = os.path.realpath(temp_projects["b"])
        
        def create_client_with_data(**kwargs):
            client = get_mock(kwargs["working_dir"])
            workspace = os.path.realpath(kwargs["working_dir"])
            
            # Project A returns issues, B returns empty
            if workspace == canonical_a:
                client.list_issues = AsyncMock(return_value=[
                    {"id": "a-1", "title": "Issue from A"}
                ])
            else:
                client.list_issues = AsyncMock(return_value=[])
            
            return client
        
        async def list_from_project(workspace: str):
            token = current_workspace.set(workspace)
            try:
                with patch("beads_mcp.tools.create_bd_client", side_effect=create_client_with_data):
                    return await beads_list_issues()
            finally:
                current_workspace.reset(token)
        
        # List from both projects
        issues_a = await list_from_project(temp_projects["a"])
        issues_b = await list_from_project(temp_projects["b"])
        
        # Project A has issues, B is empty
        assert len(issues_a) == 1
        assert issues_a[0]["id"] == "a-1"
        assert len(issues_b) == 0
    
    @pytest.mark.asyncio
    async def test_stress_test_many_parallel_calls(self, temp_projects, mock_client_factory):
        """Stress test: many parallel calls across multiple repos."""
        get_mock, clients = mock_client_factory
        
        async def random_call(workspace: str, call_id: int):
            """Random call to project."""
            token = current_workspace.set(workspace)
            try:
                with patch("beads_mcp.tools.create_bd_client", side_effect=lambda **kwargs: get_mock(kwargs["working_dir"])):
                    # Alternate between ready and list calls
                    if call_id % 2 == 0:
                        return await beads_ready_work()
                    else:
                        return await beads_list_issues()
            finally:
                current_workspace.reset(token)
        
        # 100 parallel calls across 3 projects
        workspaces = [temp_projects["a"], temp_projects["b"], temp_projects["c"]]
        tasks = [
            random_call(workspaces[i % 3], i)
            for i in range(100)
        ]
        
        results = await asyncio.gather(*tasks)
        
        # Verify all calls completed
        assert len(results) == 100
        
        # Verify only 3 clients created (one per project)
        assert len(clients) == 3


class TestContextVarBehavior:
    """Test ContextVar behavior and edge cases."""
    
    @pytest.mark.asyncio
    async def test_contextvar_isolated_per_request(self, temp_projects):
        """Test ContextVar is isolated per async call."""
        
        async def get_current_workspace():
            """Get current workspace from ContextVar."""
            return current_workspace.get()
        
        # Set different contexts in parallel
        async def call_with_context(workspace: str):
            token = current_workspace.set(workspace)
            try:
                # Simulate some async work
                await asyncio.sleep(0.01)
                return await get_current_workspace()
            finally:
                current_workspace.reset(token)
        
        results = await asyncio.gather(
            call_with_context(temp_projects["a"]),
            call_with_context(temp_projects["b"]),
            call_with_context(temp_projects["c"]),
        )
        
        # Each call should see its own workspace
        assert temp_projects["a"] in results
        assert temp_projects["b"] in results
        assert temp_projects["c"] in results
    
    @pytest.mark.asyncio
    async def test_contextvar_reset_after_call(self, temp_projects):
        """Test ContextVar is properly reset after call."""
        # No context initially
        assert current_workspace.get() is None
        
        token = current_workspace.set(temp_projects["a"])
        assert current_workspace.get() == temp_projects["a"]
        
        current_workspace.reset(token)
        assert current_workspace.get() is None
    
    @pytest.mark.asyncio
    async def test_contextvar_fallback_to_env(self, temp_projects):
        """Test ContextVar falls back to BEADS_WORKING_DIR."""
        import os
        
        # Set env var
        canonical_a = os.path.realpath(temp_projects["a"])
        os.environ["BEADS_WORKING_DIR"] = temp_projects["a"]
        
        try:
            # ContextVar not set, should use env
            with patch("beads_mcp.tools.create_bd_client") as mock_create:
                mock_client = AsyncMock()
                mock_client.ready = AsyncMock(return_value=[])
                mock_create.return_value = mock_client
                
                await beads_ready_work()
                
                # Should have created client with env workspace (canonicalized)
                mock_create.assert_called_once()
                assert os.path.realpath(mock_create.call_args.kwargs["working_dir"]) == canonical_a
        finally:
            os.environ.pop("BEADS_WORKING_DIR", None)


class TestEdgeCases:
    """Test edge cases and error handling."""
    
    @pytest.mark.asyncio
    async def test_no_workspace_raises_error(self):
        """Test calling without workspace raises helpful error."""
        import os
        from beads_mcp import tools
        
        # Clear context and env
        tools.current_workspace.set(None)
        os.environ.pop("BEADS_WORKING_DIR", None)
        
        # No ContextVar set, no env var, and auto-detect fails
        with pytest.raises(Exception) as exc_info:
            with patch("beads_mcp.tools._find_beads_db_in_tree", return_value=None):
                await beads_ready_work()
        
        assert "No beads workspace found" in str(exc_info.value)
    
    def test_canonicalize_path_cached(self, temp_projects):
        """Test path canonicalization is cached for performance."""
        # Clear cache
        _canonicalize_path.cache_clear()
        
        # First call
        result1 = _canonicalize_path(temp_projects["a"])
        
        # Second call should hit cache
        result2 = _canonicalize_path(temp_projects["a"])
        
        assert result1 == result2
        
        # Verify cache hit
        cache_info = _canonicalize_path.cache_info()
        assert cache_info.hits >= 1
