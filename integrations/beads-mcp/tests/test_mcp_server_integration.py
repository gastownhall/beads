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
    # Create temp directory for database
    temp_dir = tempfile.mkdtemp(prefix="beads_mcp_test_", dir="/tmp")
    db_path = os.path.join(temp_dir, "test.db")

    # Initialize database with explicit BEADS_DB - no chdir needed!
    import asyncio

    env = os.environ.copy()
    # Clear any existing BEADS_DB to ensure we use only temp db
    env.pop("BEADS_DB", None)
    env["BEADS_DB"] = db_path

    # Use temp workspace dir for subprocess (prevents .beads/ discovery)
    with tempfile.TemporaryDirectory(
        prefix="beads_mcp_test_workspace_", dir="/tmp"
    ) as temp_workspace:
        process = await asyncio.create_subprocess_exec(
            bd_executable,
            "init",
            "--prefix",
            "test",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=env,
            cwd=temp_workspace,  # Run in temp workspace, not project dir
        )
        stdout, stderr = await process.communicate()

        if process.returncode != 0:
            pytest.fail(f"Failed to initialize test database: {stderr.decode()}")

    yield db_path

    # Cleanup
    shutil.rmtree(temp_dir, ignore_errors=True)


@pytest.fixture
async def mcp_client(bd_executable, temp_db, monkeypatch):
    """Create MCP client with temporary database."""
    from beads_mcp import tools
    from beads_mcp.bd_client import BdClient

    # Reset client before test
    tools._client = None

    # Create a pre-configured client with explicit paths (bypasses config loading)
    tools._client = BdClient(bd_path=bd_executable, beads_db=temp_db)

    # Create test client
    async with Client(mcp) as client:
        yield client

    # Reset client after test
    tools._client = None


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
        {"title": "Issue to show", "priority": 2, "issue_type": "task"},
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
        "create", {"title": "Issue 1", "priority": 0, "issue_type": "bug"}
    )
    await mcp_client.call_tool(
        "create", {"title": "Issue 2", "priority": 1, "issue_type": "feature"}
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
        "create", {"title": "Issue to update", "priority": 2, "issue_type": "task"}
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
        "create", {"title": "Issue to close", "priority": 1, "issue_type": "bug"}
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Close issue
    close_result = await mcp_client.call_tool(
        "close", {"issue_id": issue_id, "reason": "Test complete"}
    )

    closed_issues = json.loads(close_result.content[0].text)
    assert len(closed_issues) >= 1
    closed = closed_issues[0]
    assert closed["id"] == issue_id
    assert closed["status"] == "closed"
    assert closed["closed_at"] is not None


@pytest.mark.asyncio
async def test_ready_work_tool(mcp_client):
    """Test ready_work tool."""
    import json

    # Create a ready issue (no dependencies)
    ready_result = await mcp_client.call_tool(
        "create", {"title": "Ready work", "priority": 1, "issue_type": "task"}
    )
    ready_issue = json.loads(ready_result.content[0].text)

    # Create blocked issue
    blocking_result = await mcp_client.call_tool(
        "create", {"title": "Blocking issue", "priority": 1, "issue_type": "task"}
    )
    blocking_issue = json.loads(blocking_result.content[0].text)

    blocked_result = await mcp_client.call_tool(
        "create", {"title": "Blocked issue", "priority": 1, "issue_type": "task"}
    )
    blocked_issue = json.loads(blocked_result.content[0].text)

    # Add blocking dependency
    await mcp_client.call_tool(
        "dep",
        {
            "from_id": blocked_issue["id"],
            "to_id": blocking_issue["id"],
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
        "create", {"title": "Issue 1", "priority": 1, "issue_type": "task"}
    )
    issue1 = json.loads(issue1_result.content[0].text)

    issue2_result = await mcp_client.call_tool(
        "create", {"title": "Issue 2", "priority": 1, "issue_type": "task"}
    )
    issue2 = json.loads(issue2_result.content[0].text)

    # Add dependency
    result = await mcp_client.call_tool(
        "dep",
        {"from_id": issue1["id"], "to_id": issue2["id"], "dep_type": "blocks"},
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
        },
    )
    await mcp_client.call_tool(
        "create",
        {
            "title": "Feature P1",
            "priority": 1,
            "issue_type": "feature",
            "assignee": "bob",
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
        "create", {"title": "P0 issue", "priority": 0, "issue_type": "bug"}
    )
    await mcp_client.call_tool(
        "create", {"title": "P1 issue", "priority": 1, "issue_type": "task"}
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
        },
    )
    created = json.loads(create_result.content[0].text)
    issue_id = created["id"]

    # Update only status
    update_result = await mcp_client.call_tool(
        "update", {"issue_id": issue_id, "status": "in_progress"}
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
        "create", {"title": "Issue 1", "priority": 1, "issue_type": "task"}
    )
    issue1 = json.loads(issue1_result.content[0].text)

    issue2_result = await mcp_client.call_tool(
        "create", {"title": "Issue 2", "priority": 1, "issue_type": "task"}
    )
    issue2 = json.loads(issue2_result.content[0].text)

    # Test related dependency
    result = await mcp_client.call_tool(
        "dep",
        {"from_id": issue1["id"], "to_id": issue2["id"], "dep_type": "related"},
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
        "create", {"title": "Stats test 1", "priority": 1, "issue_type": "bug"}
    )
    await mcp_client.call_tool(
        "create", {"title": "Stats test 2", "priority": 2, "issue_type": "task"}
    )

    # Get stats
    result = await mcp_client.call_tool("stats", {})
    stats = json.loads(result.content[0].text)

    assert "total_issues" in stats
    assert "open_issues" in stats
    assert stats["total_issues"] >= 2


@pytest.mark.asyncio
async def test_blocked_tool(mcp_client):
    """Test blocked tool."""
    import json

    # Create two issues
    blocking_result = await mcp_client.call_tool(
        "create", {"title": "Blocking issue", "priority": 1, "issue_type": "task"}
    )
    blocking_issue = json.loads(blocking_result.content[0].text)

    blocked_result = await mcp_client.call_tool(
        "create", {"title": "Blocked issue", "priority": 1, "issue_type": "task"}
    )
    blocked_issue = json.loads(blocked_result.content[0].text)

    # Add blocking dependency
    await mcp_client.call_tool(
        "dep",
        {
            "from_id": blocked_issue["id"],
            "to_id": blocking_issue["id"],
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
async def test_init_tool(mcp_client, bd_executable):
    """Test init tool."""
    import os
    import tempfile

    # Create a completely separate temp directory and database
    with tempfile.TemporaryDirectory(prefix="beads_init_test_", dir="/tmp") as temp_dir:
        new_db_path = os.path.join(temp_dir, "new_test.db")

        # Temporarily override the client's BEADS_DB for this test
        from beads_mcp import tools

        # Save original client
        original_client = tools._client

        # Create a new client pointing to the new database path
        from beads_mcp.bd_client import BdClient
        tools._client = BdClient(bd_path=bd_executable, beads_db=new_db_path)

        try:
            # Call init tool
            result = await mcp_client.call_tool("init", {"prefix": "test-init"})
            output = result.content[0].text

            # Verify output contains success message
            assert "bd initialized successfully!" in output
        finally:
            # Restore original client
            tools._client = original_client
