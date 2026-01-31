"""Real integration tests for MCP server using fastmcp.Client."""

import os
import shutil
import tempfile

import pytest
from fastmcp.client import Client

from beads_mcp.server import mcp


@pytest.fixture(scope="session")
def bd_executable():
    """Verify bd is available in PATH."""
    bd_path = shutil.which("bd")
    if not bd_path:
        pytest.fail(
            "bd executable not found in PATH. "
            "Please install bd or add it to your PATH before running integration tests."
        )
    return bd_path


@pytest.fixture
async def temp_db(bd_executable):
    """Create a temporary database file and initialize it - fully hermetic."""
    # Create temp directory that will serve as the workspace root
    temp_dir = tempfile.mkdtemp(prefix="beads_mcp_test_", dir="/tmp")

    # Initialize database in this directory (creates .beads/ subdirectory)
    import asyncio

    env = os.environ.copy()
    # Clear any existing BEADS_DIR/BEADS_DB to ensure clean state
    env.pop("BEADS_DB", None)
    env.pop("BEADS_DIR", None)

    # Run bd init in the temp directory - it will create .beads/ subdirectory
    process = await asyncio.create_subprocess_exec(
        bd_executable,
        "init",
        "--prefix",
        "test",
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=env,
        cwd=temp_dir,  # Run in temp dir - bd init creates .beads/ here
    )
    stdout, stderr = await process.communicate()

    if process.returncode != 0:
        pytest.fail(f"Failed to initialize test database: {stderr.decode()}")

    # Return the .beads directory path (not the db file)
    beads_dir = os.path.join(temp_dir, ".beads")
    
    yield beads_dir

    # Cleanup
    shutil.rmtree(temp_dir, ignore_errors=True)


@pytest.fixture
async def mcp_client(bd_executable, temp_db, monkeypatch):
    """Create MCP client with temporary database."""
    from beads_mcp import tools

    # Reset connection pool before test
    tools._connection_pool.clear()

    # Reset context environment variables
    os.environ.pop("BEADS_CONTEXT_SET", None)
    os.environ.pop("BEADS_WORKING_DIR", None)
    os.environ.pop("BEADS_DB", None)
    os.environ.pop("BEADS_DIR", None)

    # temp_db is now the .beads directory path
    # The workspace root is the parent directory
    workspace_root = os.path.dirname(temp_db)

    # Disable daemon mode for tests (prevents daemon accumulation and timeouts)
    os.environ["BEADS_NO_DAEMON"] = "1"

    # Create test client
    async with Client(mcp) as client:
        # Automatically set context for the tests
        await client.call_tool("context", {"workspace_root": workspace_root})
        yield client

    # Reset connection pool and context after test
    tools._connection_pool.clear()
    os.environ.pop("BEADS_CONTEXT_SET", None)
    os.environ.pop("BEADS_WORKING_DIR", None)
    os.environ.pop("BEADS_DB", None)
    os.environ.pop("BEADS_DIR", None)
    os.environ.pop("BEADS_NO_DAEMON", None)


@pytest.mark.asyncio
async def test_quickstart_resource(mcp_client):
    """Test beads://quickstart resource."""
    result = await mcp_client.read_resource("beads://quickstart")

    assert result is not None
    content = result[0].text
    assert len(content) > 0
    assert "beads" in content.lower() or "bd" in content.lower()


@pytest.mark.asyncio
async def test_create_issue_tool(mcp_client):
    """Test create_issue tool."""
    result = await mcp_client.call_tool(
        "create",
        {
            "title": "Test MCP issue",
            "description": "Created via MCP server",
            "priority": 1,
            "issue_type": "bug",
            "brief": False,  # Get full Issue object
        },
    )

    # Parse the JSON response from CallToolResult
    import json

    issue_data = json.loads(result.content[0].text)
    assert issue_data["title"] == "Test MCP issue"
    assert issue_data["description"] == "Created via MCP server"
    assert issue_data["priority"] == 1
    assert issue_data["issue_type"] == "bug"
    assert issue_data["status"] == "open"
    assert "id" in issue_data

    return issue_data["id"]


@pytest.mark.asyncio
async def test_show_issue_tool(mcp_client):
    """Test show_issue tool."""
    # First create an issue
    create_result = await mcp_client.call_tool(
        "create",
        {"title": "Issue to show", "priority": 2, "issue_type": "task", "brief": False},
    )
    import json

    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Show the issue
    show_result = await mcp_client.call_tool("show", {"issue_id": issue_id})

    issue = json.loads(show_result.content[0].text)
    assert issue["id"] == issue_id
    assert issue["title"] == "Issue to show"


@pytest.mark.asyncio
async def test_list_issues_tool(mcp_client):
    """Test list_issues tool."""
    # Create some issues first
    await mcp_client.call_tool(
        "create", {"title": "Issue 1", "priority": 0, "issue_type": "bug", "brief": False}
    )
    await mcp_client.call_tool(
        "create", {"title": "Issue 2", "priority": 1, "issue_type": "feature", "brief": False}
    )

    # List all issues
    result = await mcp_client.call_tool("list", {})

    import json

    issues = json.loads(result.content[0].text)
    assert len(issues) >= 2

    # List with status filter
    result = await mcp_client.call_tool("list", {"status": "open"})
    issues = json.loads(result.content[0].text)
    assert all(issue["status"] == "open" for issue in issues)


@pytest.mark.asyncio
async def test_update_issue_tool(mcp_client):
    """Test update_issue tool."""
    import json

    # Create issue
    create_result = await mcp_client.call_tool(
        "create", {"title": "Issue to update", "priority": 2, "issue_type": "task", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Update issue
    update_result = await mcp_client.call_tool(
        "update",
        {
            "issue_id": issue_id,
            "status": "in_progress",
            "priority": 0,
            "title": "Updated title",
            "brief": False,  # Get full Issue object
        },
    )

    updated = json.loads(update_result.content[0].text)
    assert updated["id"] == issue_id
    assert updated["status"] == "in_progress"
    assert updated["priority"] == 0
    assert updated["title"] == "Updated title"


@pytest.mark.asyncio
async def test_close_issue_tool(mcp_client):
    """Test close_issue tool."""
    import json

    # Create issue
    create_result = await mcp_client.call_tool(
        "create", {"title": "Issue to close", "priority": 1, "issue_type": "bug", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Close issue with brief=False to get full Issue object
    close_result = await mcp_client.call_tool(
        "close", {"issue_id": issue_id, "reason": "Test complete", "brief": False}
    )

    closed_issues = json.loads(close_result.content[0].text)
    assert len(closed_issues) >= 1
    closed = closed_issues[0]
    assert closed["id"] == issue_id
    assert closed["status"] == "closed"
    assert closed["closed_at"] is not None


@pytest.mark.asyncio
async def test_reopen_issue_tool(mcp_client):
    """Test reopen_issue tool."""
    import json

    # Create and close issue
    create_result = await mcp_client.call_tool(
        "create", {"title": "Issue to reopen", "priority": 1, "issue_type": "bug", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    await mcp_client.call_tool(
        "close", {"issue_id": issue_id, "reason": "Done"}
    )

    # Reopen issue with brief=False to get full Issue object
    reopen_result = await mcp_client.call_tool(
        "reopen", {"issue_ids": [issue_id], "brief": False}
    )

    reopened_issues = json.loads(reopen_result.content[0].text)
    assert len(reopened_issues) >= 1
    reopened = reopened_issues[0]
    assert reopened["id"] == issue_id
    assert reopened["status"] == "open"
    assert reopened["closed_at"] is None


@pytest.mark.asyncio
async def test_reopen_multiple_issues_tool(mcp_client):
    """Test reopening multiple issues via MCP tool."""
    import json

    # Create and close two issues
    issue1_result = await mcp_client.call_tool(
        "create", {"title": "Issue 1 to reopen", "priority": 1, "issue_type": "task", "brief": False}
    )
    issue1 = json.loads(issue1_result.content[0].text)

    issue2_result = await mcp_client.call_tool(
        "create", {"title": "Issue 2 to reopen", "priority": 1, "issue_type": "task", "brief": False}
    )
    issue2 = json.loads(issue2_result.content[0].text)

    await mcp_client.call_tool("close", {"issue_id": issue1["id"], "reason": "Done"})
    await mcp_client.call_tool("close", {"issue_id": issue2["id"], "reason": "Done"})

    # Reopen both issues with brief=False
    reopen_result = await mcp_client.call_tool(
        "reopen", {"issue_ids": [issue1["id"], issue2["id"]], "brief": False}
    )

    reopened_issues = json.loads(reopen_result.content[0].text)
    assert len(reopened_issues) == 2
    reopened_ids = {issue["id"] for issue in reopened_issues}
    assert issue1["id"] in reopened_ids
    assert issue2["id"] in reopened_ids
    assert all(issue["status"] == "open" for issue in reopened_issues)
    assert all(issue["closed_at"] is None for issue in reopened_issues)


@pytest.mark.asyncio
async def test_reopen_with_reason_tool(mcp_client):
    """Test reopening issue with reason parameter via MCP tool."""
    import json

    # Create and close issue
    create_result = await mcp_client.call_tool(
        "create", {"title": "Issue to reopen with reason", "priority": 1, "issue_type": "bug", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    await mcp_client.call_tool("close", {"issue_id": issue_id, "reason": "Done"})

    # Reopen with reason and brief=False
    reopen_result = await mcp_client.call_tool(
        "reopen",
        {"issue_ids": [issue_id], "reason": "Found regression", "brief": False}
    )

    reopened_issues = json.loads(reopen_result.content[0].text)
    assert len(reopened_issues) >= 1
    reopened = reopened_issues[0]
    assert reopened["id"] == issue_id
    assert reopened["status"] == "open"
    assert reopened["closed_at"] is None


@pytest.mark.asyncio
async def test_ready_work_tool(mcp_client):
    """Test ready_work tool."""
    import json

    # Create a ready issue (no dependencies)
    ready_result = await mcp_client.call_tool(
        "create", {"title": "Ready work", "priority": 1, "issue_type": "task", "brief": False}
    )
    ready_issue = json.loads(ready_result.content[0].text)

    # Create blocked issue
    blocking_result = await mcp_client.call_tool(
        "create", {"title": "Blocking issue", "priority": 1, "issue_type": "task", "brief": False}
    )
    blocking_issue = json.loads(blocking_result.content[0].text)

    blocked_result = await mcp_client.call_tool(
        "create", {"title": "Blocked issue", "priority": 1, "issue_type": "task", "brief": False}
    )
    blocked_issue = json.loads(blocked_result.content[0].text)

    # Add blocking dependency
    await mcp_client.call_tool(
        "dep",
        {
            "issue_id": blocked_issue["id"],
            "depends_on_id": blocking_issue["id"],
            "dep_type": "blocks",
        },
    )

    # Get ready work
    result = await mcp_client.call_tool("ready", {"limit": 100})
    ready_issues = json.loads(result.content[0].text)

    ready_ids = [issue["id"] for issue in ready_issues]
    assert ready_issue["id"] in ready_ids
    assert blocked_issue["id"] not in ready_ids


@pytest.mark.asyncio
async def test_add_dependency_tool(mcp_client):
    """Test add_dependency tool."""
    import json

    # Create two issues
    issue1_result = await mcp_client.call_tool(
        "create", {"title": "Issue 1", "priority": 1, "issue_type": "task", "brief": False}
    )
    issue1 = json.loads(issue1_result.content[0].text)

    issue2_result = await mcp_client.call_tool(
        "create", {"title": "Issue 2", "priority": 1, "issue_type": "task", "brief": False}
    )
    issue2 = json.loads(issue2_result.content[0].text)

    # Add dependency
    result = await mcp_client.call_tool(
        "dep",
        {"issue_id": issue1["id"], "depends_on_id": issue2["id"], "dep_type": "blocks"},
    )

    message = result.content[0].text
    assert "Added dependency" in message
    assert issue1["id"] in message
    assert issue2["id"] in message


@pytest.mark.asyncio
async def test_create_with_all_fields(mcp_client):
    """Test create_issue with all optional fields."""
    import json

    result = await mcp_client.call_tool(
        "create",
        {
            "title": "Full issue",
            "description": "Complete description",
            "priority": 0,
            "issue_type": "feature",
            "assignee": "testuser",
            "labels": ["urgent", "backend"],
            "brief": False,  # Get full Issue object
        },
    )

    issue = json.loads(result.content[0].text)
    assert issue["title"] == "Full issue"
    assert issue["description"] == "Complete description"
    assert issue["priority"] == 0
    assert issue["issue_type"] == "feature"
    assert issue["assignee"] == "testuser"


@pytest.mark.asyncio
async def test_list_with_filters(mcp_client):
    """Test list_issues with various filters."""
    import json

    # Create issues with different attributes
    await mcp_client.call_tool(
        "create",
        {
            "title": "Bug P0",
            "priority": 0,
            "issue_type": "bug",
            "assignee": "alice",
            "brief": False,
        },
    )
    await mcp_client.call_tool(
        "create",
        {
            "title": "Feature P1",
            "priority": 1,
            "issue_type": "feature",
            "assignee": "bob",
            "brief": False,
        },
    )

    # Filter by priority
    result = await mcp_client.call_tool("list", {"priority": 0})
    issues = json.loads(result.content[0].text)
    assert all(issue["priority"] == 0 for issue in issues)

    # Filter by type
    result = await mcp_client.call_tool("list", {"issue_type": "bug"})
    issues = json.loads(result.content[0].text)
    assert all(issue["issue_type"] == "bug" for issue in issues)

    # Filter by assignee
    result = await mcp_client.call_tool("list", {"assignee": "alice"})
    issues = json.loads(result.content[0].text)
    assert all(issue["assignee"] == "alice" for issue in issues)


@pytest.mark.asyncio
async def test_ready_work_with_priority_filter(mcp_client):
    """Test ready_work with priority filter."""
    import json

    # Create issues with different priorities
    await mcp_client.call_tool(
        "create", {"title": "P0 issue", "priority": 0, "issue_type": "bug", "brief": False}
    )
    await mcp_client.call_tool(
        "create", {"title": "P1 issue", "priority": 1, "issue_type": "task", "brief": False}
    )

    # Get ready work with priority filter
    result = await mcp_client.call_tool("ready", {"priority": 0, "limit": 100})
    issues = json.loads(result.content[0].text)
    assert all(issue["priority"] == 0 for issue in issues)


@pytest.mark.asyncio
async def test_update_partial_fields(mcp_client):
    """Test update_issue with partial field updates."""
    import json

    # Create issue
    create_result = await mcp_client.call_tool(
        "create",
        {
            "title": "Original title",
            "description": "Original description",
            "priority": 2,
            "issue_type": "task",
            "brief": False,
        },
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Update only status with brief=False to get full Issue
    update_result = await mcp_client.call_tool(
        "update", {"issue_id": issue_id, "status": "in_progress", "brief": False}
    )
    updated = json.loads(update_result.content[0].text)
    assert updated["status"] == "in_progress"
    assert updated["title"] == "Original title"  # Unchanged
    assert updated["priority"] == 2  # Unchanged


@pytest.mark.asyncio
async def test_dependency_types(mcp_client):
    """Test different dependency types."""
    import json

    # Create issues
    issue1_result = await mcp_client.call_tool(
        "create", {"title": "Issue 1", "priority": 1, "issue_type": "task", "brief": False}
    )
    issue1 = json.loads(issue1_result.content[0].text)

    issue2_result = await mcp_client.call_tool(
        "create", {"title": "Issue 2", "priority": 1, "issue_type": "task", "brief": False}
    )
    issue2 = json.loads(issue2_result.content[0].text)

    # Test related dependency
    result = await mcp_client.call_tool(
        "dep",
        {"issue_id": issue1["id"], "depends_on_id": issue2["id"], "dep_type": "related"},
    )

    message = result.content[0].text
    assert "Added dependency" in message
    assert "related" in message


@pytest.mark.asyncio
async def test_stats_tool(mcp_client):
    """Test stats tool."""
    import json

    # Create some issues to get stats
    await mcp_client.call_tool(
        "create", {"title": "Stats test 1", "priority": 1, "issue_type": "bug", "brief": False}
    )
    await mcp_client.call_tool(
        "create", {"title": "Stats test 2", "priority": 2, "issue_type": "task", "brief": False}
    )

    # Get stats
    result = await mcp_client.call_tool("stats", {})
    stats = json.loads(result.content[0].text)

    assert "summary" in stats
    assert "total_issues" in stats["summary"]
    assert "open_issues" in stats["summary"]
    assert stats["summary"]["total_issues"] >= 2


@pytest.mark.asyncio
async def test_blocked_tool(mcp_client):
    """Test blocked tool."""
    import json

    # Create two issues
    blocking_result = await mcp_client.call_tool(
        "create", {"title": "Blocking issue", "priority": 1, "issue_type": "task", "brief": False}
    )
    blocking_issue = json.loads(blocking_result.content[0].text)

    blocked_result = await mcp_client.call_tool(
        "create", {"title": "Blocked issue", "priority": 1, "issue_type": "task", "brief": False}
    )
    blocked_issue = json.loads(blocked_result.content[0].text)

    # Add blocking dependency
    await mcp_client.call_tool(
        "dep",
        {
            "issue_id": blocked_issue["id"],
            "depends_on_id": blocking_issue["id"],
            "dep_type": "blocks",
        },
    )

    # Get blocked issues
    result = await mcp_client.call_tool("blocked", {})
    blocked_issues = json.loads(result.content[0].text)

    # Should have at least the one we created
    blocked_ids = [issue["id"] for issue in blocked_issues]
    assert blocked_issue["id"] in blocked_ids

    # Find our blocked issue and verify it has blocking info
    our_blocked = next(issue for issue in blocked_issues if issue["id"] == blocked_issue["id"])
    assert our_blocked["blocked_by_count"] >= 1
    assert blocking_issue["id"] in our_blocked["blocked_by"]


@pytest.mark.asyncio
async def test_context_init_action(bd_executable):
    """Test context tool with init action.

    Note: This test validates that context(action='init') can be called successfully via MCP.
    Uses a fresh temp directory without an existing database.
    """
    import os
    import tempfile
    import shutil
    from beads_mcp import tools
    from beads_mcp.server import mcp

    # Reset connection pool and context
    tools._connection_pool.clear()
    os.environ.pop("BEADS_CONTEXT_SET", None)
    os.environ.pop("BEADS_WORKING_DIR", None)
    os.environ.pop("BEADS_DB", None)
    os.environ.pop("BEADS_DIR", None)
    os.environ["BEADS_NO_DAEMON"] = "1"

    # Create a fresh temp directory without any beads database
    temp_dir = tempfile.mkdtemp(prefix="beads_init_test_")
    try:
        async with Client(mcp) as client:
            # First set context to the fresh directory
            await client.call_tool("context", {"workspace_root": temp_dir})

            # Call context tool with init action
            result = await client.call_tool("context", {"action": "init", "prefix": "test-init"})
            output = result.content[0].text

            # Verify output contains success message
            assert "bd initialized successfully!" in output
            assert "test-init" in output
    finally:
        tools._connection_pool.clear()
        shutil.rmtree(temp_dir, ignore_errors=True)
        os.environ.pop("BEADS_CONTEXT_SET", None)
        os.environ.pop("BEADS_WORKING_DIR", None)


@pytest.mark.asyncio
async def test_context_show_action(mcp_client, temp_db):
    """Test context tool with show action.

    Verifies that context(action='show') returns workspace information.
    """
    # Call context tool with show action (default when no args)
    result = await mcp_client.call_tool("context", {"action": "show"})
    output = result.content[0].text

    # Verify output contains workspace info
    assert "Workspace root:" in output
    assert "Database:" in output


@pytest.mark.asyncio
async def test_context_default_show(mcp_client, temp_db):
    """Test context tool defaults to show when no args provided."""
    # Call context tool with no args - should default to show
    result = await mcp_client.call_tool("context", {})
    output = result.content[0].text

    # Verify output contains workspace info (same as show action)
    assert "Workspace root:" in output
    assert "Database:" in output


# =============================================================================
# OUTPUT CONTROL PARAMETER TESTS
# =============================================================================


@pytest.mark.asyncio
async def test_create_brief_default(mcp_client):
    """Test create returns OperationResult by default (brief=True)."""
    import json

    result = await mcp_client.call_tool(
        "create",
        {"title": "Brief test issue", "priority": 2, "issue_type": "task"},
    )

    data = json.loads(result.content[0].text)
    # Default brief=True returns OperationResult
    assert "id" in data
    assert data["action"] == "created"
    # Should NOT have full Issue fields
    assert "title" not in data
    assert "description" not in data


@pytest.mark.asyncio
async def test_create_brief_false(mcp_client):
    """Test create returns full Issue when brief=False."""
    import json

    result = await mcp_client.call_tool(
        "create",
        {
            "title": "Full issue test",
            "description": "Full description",
            "priority": 1,
            "issue_type": "bug",
            "brief": False,
        },
    )

    data = json.loads(result.content[0].text)
    # brief=False returns full Issue
    assert data["title"] == "Full issue test"
    assert data["description"] == "Full description"
    assert data["priority"] == 1
    assert data["issue_type"] == "bug"
    assert data["status"] == "open"


@pytest.mark.asyncio
async def test_update_brief_default(mcp_client):
    """Test update returns OperationResult by default (brief=True)."""
    import json

    # Create issue first
    create_result = await mcp_client.call_tool(
        "create", {"title": "Update brief test", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Update with default brief=True
    update_result = await mcp_client.call_tool(
        "update", {"issue_id": issue_id, "status": "in_progress"}
    )

    data = json.loads(update_result.content[0].text)
    assert data["id"] == issue_id
    assert data["action"] == "updated"
    assert "title" not in data


@pytest.mark.asyncio
async def test_update_brief_false(mcp_client):
    """Test update returns full Issue when brief=False."""
    import json

    # Create issue first
    create_result = await mcp_client.call_tool(
        "create", {"title": "Update full test", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Update with brief=False
    update_result = await mcp_client.call_tool(
        "update", {"issue_id": issue_id, "status": "in_progress", "brief": False}
    )

    data = json.loads(update_result.content[0].text)
    assert data["id"] == issue_id
    assert data["status"] == "in_progress"
    assert data["title"] == "Update full test"


@pytest.mark.asyncio
async def test_close_brief_default(mcp_client):
    """Test close returns OperationResult by default (brief=True)."""
    import json

    # Create issue first
    create_result = await mcp_client.call_tool(
        "create", {"title": "Close brief test", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Close with default brief=True
    close_result = await mcp_client.call_tool(
        "close", {"issue_id": issue_id, "reason": "Done"}
    )

    data = json.loads(close_result.content[0].text)
    assert isinstance(data, list)
    assert len(data) == 1
    assert data[0]["id"] == issue_id
    assert data[0]["action"] == "closed"


@pytest.mark.asyncio
async def test_close_brief_false(mcp_client):
    """Test close returns full Issue when brief=False."""
    import json

    # Create issue first
    create_result = await mcp_client.call_tool(
        "create", {"title": "Close full test", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Close with brief=False
    close_result = await mcp_client.call_tool(
        "close", {"issue_id": issue_id, "reason": "Done", "brief": False}
    )

    data = json.loads(close_result.content[0].text)
    assert isinstance(data, list)
    assert len(data) >= 1
    assert data[0]["id"] == issue_id
    assert data[0]["status"] == "closed"
    assert data[0]["title"] == "Close full test"


@pytest.mark.asyncio
async def test_reopen_brief_default(mcp_client):
    """Test reopen returns OperationResult by default (brief=True)."""
    import json

    # Create and close issue first
    create_result = await mcp_client.call_tool(
        "create", {"title": "Reopen brief test", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    await mcp_client.call_tool("close", {"issue_id": issue_id})

    # Reopen with default brief=True
    reopen_result = await mcp_client.call_tool(
        "reopen", {"issue_ids": [issue_id]}
    )

    data = json.loads(reopen_result.content[0].text)
    assert isinstance(data, list)
    assert len(data) == 1
    assert data[0]["id"] == issue_id
    assert data[0]["action"] == "reopened"


@pytest.mark.asyncio
async def test_show_brief(mcp_client):
    """Test show with brief=True returns BriefIssue."""
    import json

    # Create issue first
    create_result = await mcp_client.call_tool(
        "create",
        {"title": "Show brief test", "description": "Long description", "brief": False},
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Show with brief=True
    show_result = await mcp_client.call_tool(
        "show", {"issue_id": issue_id, "brief": True}
    )

    data = json.loads(show_result.content[0].text)
    # BriefIssue has only: id, title, status, priority
    assert data["id"] == issue_id
    assert data["title"] == "Show brief test"
    assert data["status"] == "open"
    assert "priority" in data
    # Should NOT have full Issue fields
    assert "description" not in data
    assert "dependencies" not in data


@pytest.mark.asyncio
async def test_show_fields_projection(mcp_client):
    """Test show with fields parameter for custom projection."""
    import json

    # Create issue first
    create_result = await mcp_client.call_tool(
        "create",
        {
            "title": "Fields test",
            "description": "Test description",
            "priority": 1,
            "brief": False,
        },
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Show with specific fields
    show_result = await mcp_client.call_tool(
        "show", {"issue_id": issue_id, "fields": ["id", "title", "priority"]}
    )

    data = json.loads(show_result.content[0].text)
    # Should have only requested fields
    assert data["id"] == issue_id
    assert data["title"] == "Fields test"
    assert data["priority"] == 1
    # Should NOT have other fields
    assert "description" not in data
    assert "status" not in data


@pytest.mark.asyncio
async def test_show_fields_invalid(mcp_client):
    """Test show with invalid fields raises error."""
    import json
    from fastmcp.exceptions import ToolError

    # Create issue first
    create_result = await mcp_client.call_tool(
        "create", {"title": "Invalid fields test", "brief": False}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Show with invalid field should raise ToolError
    with pytest.raises(ToolError) as exc_info:
        await mcp_client.call_tool(
            "show", {"issue_id": issue_id, "fields": ["id", "nonexistent_field"]}
        )

    # Verify error message mentions invalid field
    assert "Invalid field" in str(exc_info.value)


@pytest.mark.asyncio
async def test_show_max_description_length(mcp_client):
    """Test show with max_description_length truncates description."""
    import json

    # Create issue with long description
    long_desc = "A" * 200
    create_result = await mcp_client.call_tool(
        "create",
        {"title": "Truncate test", "description": long_desc, "brief": False},
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Show with truncation
    show_result = await mcp_client.call_tool(
        "show", {"issue_id": issue_id, "max_description_length": 50}
    )

    data = json.loads(show_result.content[0].text)
    # Description should be truncated
    assert len(data["description"]) <= 53  # 50 + "..."
    assert data["description"].endswith("...")


@pytest.mark.asyncio
async def test_list_brief(mcp_client):
    """Test list with brief=True returns BriefIssue format."""
    import json

    # Create some issues
    await mcp_client.call_tool(
        "create", {"title": "List brief 1", "priority": 1, "brief": False}
    )
    await mcp_client.call_tool(
        "create", {"title": "List brief 2", "priority": 2, "brief": False}
    )

    # List with brief=True
    result = await mcp_client.call_tool("list", {"brief": True})
    issues = json.loads(result.content[0].text)

    assert len(issues) >= 2
    for issue in issues:
        # BriefIssue has only: id, title, status, priority
        assert "id" in issue
        assert "title" in issue
        assert "status" in issue
        assert "priority" in issue
        # Should NOT have full Issue fields
        assert "description" not in issue
        assert "issue_type" not in issue


@pytest.mark.asyncio
async def test_ready_brief(mcp_client):
    """Test ready with brief=True returns BriefIssue format."""
    import json

    # Create a ready issue
    await mcp_client.call_tool(
        "create", {"title": "Ready brief test", "priority": 1, "brief": False}
    )

    # Ready with brief=True
    result = await mcp_client.call_tool("ready", {"brief": True, "limit": 100})
    issues = json.loads(result.content[0].text)

    assert len(issues) >= 1
    for issue in issues:
        # BriefIssue has only: id, title, status, priority
        assert "id" in issue
        assert "title" in issue
        assert "status" in issue
        assert "priority" in issue
        # Should NOT have full Issue fields
        assert "description" not in issue


@pytest.mark.asyncio
async def test_blocked_brief(mcp_client):
    """Test blocked with brief=True returns BriefIssue format."""
    import json

    # Create blocking dependency
    blocking_result = await mcp_client.call_tool(
        "create", {"title": "Blocker for brief test", "brief": False}
    )
    blocking = json.loads(blocking_result.content[0].text)

    blocked_result = await mcp_client.call_tool(
        "create", {"title": "Blocked for brief test", "brief": False}
    )
    blocked = json.loads(blocked_result.content[0].text)

    await mcp_client.call_tool(
        "dep",
        {"issue_id": blocked["id"], "depends_on_id": blocking["id"], "dep_type": "blocks"},
    )

    # Blocked with brief=True
    result = await mcp_client.call_tool("blocked", {"brief": True})
    issues = json.loads(result.content[0].text)

    # Find our blocked issue
    our_blocked = [i for i in issues if i["id"] == blocked["id"]]
    assert len(our_blocked) == 1
    # BriefIssue format
    assert "title" in our_blocked[0]
    assert "status" in our_blocked[0]
    # Should NOT have BlockedIssue-specific fields
    assert "blocked_by" not in our_blocked[0]


@pytest.mark.asyncio
async def test_show_brief_deps(mcp_client):
    """Test show with brief_deps=True returns compact dependencies."""
    import json

    # Create two issues with dependency
    dep_result = await mcp_client.call_tool(
        "create", {"title": "Dependency issue", "brief": False}
    )
    dep_issue = json.loads(dep_result.content[0].text)

    main_result = await mcp_client.call_tool(
        "create", {"title": "Main issue", "brief": False}
    )
    main_issue = json.loads(main_result.content[0].text)

    await mcp_client.call_tool(
        "dep",
        {"issue_id": main_issue["id"], "depends_on_id": dep_issue["id"], "dep_type": "blocks"},
    )

    # Show with brief_deps=True
    show_result = await mcp_client.call_tool(
        "show", {"issue_id": main_issue["id"], "brief_deps": True}
    )

    data = json.loads(show_result.content[0].text)
    # Full issue data
    assert data["id"] == main_issue["id"]
    assert data["title"] == "Main issue"
    # Dependencies should be compact (BriefDep format)
    assert len(data["dependencies"]) >= 1
    dep = data["dependencies"][0]
    assert "id" in dep
    assert "title" in dep
    assert "status" in dep
    # BriefDep should NOT have full LinkedIssue fields
    assert "description" not in dep
