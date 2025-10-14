"""Real integration tests for BdClient using actual bd binary."""

import os
import shutil
import tempfile
from pathlib import Path

import pytest

from beads_mcp.bd_client import BdClient, BdCommandError, BdNotFoundError
from beads_mcp.models import (
    AddDependencyParams,
    CloseIssueParams,
    CreateIssueParams,
    DependencyType,
    IssueStatus,
    IssueType,
    ListIssuesParams,
    ReadyWorkParams,
    ShowIssueParams,
    UpdateIssueParams,
)


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
def temp_db():
    """Create a temporary database file."""
    fd, db_path = tempfile.mkstemp(suffix=".db", prefix="beads_test_", dir="/tmp")
    os.close(fd)
    # Remove the file so bd init can create it
    os.unlink(db_path)
    yield db_path
    # Cleanup
    if os.path.exists(db_path):
        os.unlink(db_path)


@pytest.fixture
async def bd_client(bd_executable, temp_db):
    """Create BdClient with temporary database - fully hermetic."""
    client = BdClient(bd_path=bd_executable, beads_db=temp_db)

    # Initialize database with explicit BEADS_DB - no chdir needed!
    env = os.environ.copy()
    # Clear any existing BEADS_DB to ensure we use only temp_db
    env.pop("BEADS_DB", None)
    env["BEADS_DB"] = temp_db

    import asyncio

    # Use temp dir for subprocess to run in (prevents .beads/ discovery)
    with tempfile.TemporaryDirectory(prefix="beads_test_workspace_", dir="/tmp") as temp_dir:
        process = await asyncio.create_subprocess_exec(
            bd_executable,
            "init",
            "--prefix",
            "test",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=env,
            cwd=temp_dir,  # Run in temp dir, not project dir
        )
        stdout, stderr = await process.communicate()

        if process.returncode != 0:
            pytest.fail(f"Failed to initialize test database: {stderr.decode()}")

        yield client


@pytest.mark.asyncio
async def test_create_and_show_issue(bd_client):
    """Test creating and showing an issue with real bd."""
    # Create issue
    params = CreateIssueParams(
        title="Test integration issue",
        description="This is a real integration test",
        priority=1,
        issue_type="bug",
    )
    created = await bd_client.create(params)

    assert created.id is not None
    assert created.title == "Test integration issue"
    assert created.description == "This is a real integration test"
    assert created.priority == 1
    assert created.issue_type == "bug"
    assert created.status == "open"

    # Show issue
    show_params = ShowIssueParams(issue_id=created.id)
    shown = await bd_client.show(show_params)

    assert shown.id == created.id
    assert shown.title == created.title
    assert shown.description == created.description


@pytest.mark.asyncio
async def test_list_issues(bd_client):
    """Test listing issues with real bd."""
    # Create multiple issues
    for i in range(3):
        params = CreateIssueParams(
            title=f"Test issue {i}",
            priority=i,
            issue_type="task",
        )
        await bd_client.create(params)

    # List all issues
    params = ListIssuesParams()
    issues = await bd_client.list_issues(params)

    assert len(issues) >= 3

    # List with status filter
    params = ListIssuesParams(status="open")
    issues = await bd_client.list_issues(params)

    assert all(issue.status == "open" for issue in issues)


@pytest.mark.asyncio
async def test_update_issue(bd_client):
    """Test updating an issue with real bd."""
    # Create issue
    create_params = CreateIssueParams(
        title="Issue to update",
        priority=2,
        issue_type="feature",
    )
    created = await bd_client.create(create_params)

    # Update issue
    update_params = UpdateIssueParams(
        issue_id=created.id,
        status="in_progress",
        priority=0,
        title="Updated title",
    )
    updated = await bd_client.update(update_params)

    assert updated.id == created.id
    assert updated.status == "in_progress"
    assert updated.priority == 0
    assert updated.title == "Updated title"


@pytest.mark.asyncio
async def test_close_issue(bd_client):
    """Test closing an issue with real bd."""
    # Create issue
    create_params = CreateIssueParams(
        title="Issue to close",
        priority=1,
        issue_type="bug",
    )
    created = await bd_client.create(create_params)

    # Close issue
    close_params = CloseIssueParams(issue_id=created.id, reason="Testing complete")
    closed_issues = await bd_client.close(close_params)

    assert len(closed_issues) >= 1
    closed = closed_issues[0]
    assert closed.id == created.id
    assert closed.status == "closed"
    assert closed.closed_at is not None


@pytest.mark.asyncio
async def test_add_dependency(bd_client):
    """Test adding dependencies with real bd."""
    # Create two issues
    issue1 = await bd_client.create(
        CreateIssueParams(title="Issue 1", priority=1, issue_type="task")
    )
    issue2 = await bd_client.create(
        CreateIssueParams(title="Issue 2", priority=1, issue_type="task")
    )

    # Add dependency: issue2 blocks issue1
    params = AddDependencyParams(
        from_id=issue1.id, to_id=issue2.id, dep_type="blocks"
    )
    await bd_client.add_dependency(params)

    # Verify dependency by showing issue1
    show_params = ShowIssueParams(issue_id=issue1.id)
    shown = await bd_client.show(show_params)

    assert len(shown.dependencies) > 0
    assert any(dep.id == issue2.id for dep in shown.dependencies)


@pytest.mark.asyncio
async def test_ready_work(bd_client):
    """Test getting ready work with real bd."""
    # Create issue with no dependencies (should be ready)
    ready_issue = await bd_client.create(
        CreateIssueParams(title="Ready issue", priority=1, issue_type="task")
    )

    # Create blocked issue
    blocking_issue = await bd_client.create(
        CreateIssueParams(title="Blocking issue", priority=1, issue_type="task")
    )
    blocked_issue = await bd_client.create(
        CreateIssueParams(title="Blocked issue", priority=1, issue_type="task")
    )

    # Add blocking dependency
    await bd_client.add_dependency(
        AddDependencyParams(
            from_id=blocked_issue.id,
            to_id=blocking_issue.id,
            dep_type="blocks",
        )
    )

    # Get ready work
    params = ReadyWorkParams(limit=100)
    ready_issues = await bd_client.ready(params)

    # ready_issue should be in ready work
    ready_ids = [issue.id for issue in ready_issues]
    assert ready_issue.id in ready_ids

    # blocked_issue should NOT be in ready work
    assert blocked_issue.id not in ready_ids


@pytest.mark.asyncio
async def test_quickstart(bd_client):
    """Test quickstart command with real bd."""
    result = await bd_client.quickstart()

    assert len(result) > 0
    assert "beads" in result.lower() or "bd" in result.lower()


@pytest.mark.asyncio
async def test_create_with_labels(bd_client):
    """Test creating issue with labels."""
    params = CreateIssueParams(
        title="Issue with labels",
        priority=1,
        issue_type="feature",
        labels=["urgent", "backend"],
    )
    created = await bd_client.create(params)

    # Note: bd currently doesn't return labels in JSON output
    # This test verifies the command succeeds with labels parameter
    assert created.id is not None
    assert created.title == "Issue with labels"


@pytest.mark.asyncio
async def test_create_with_assignee(bd_client):
    """Test creating issue with assignee."""
    params = CreateIssueParams(
        title="Assigned issue",
        priority=1,
        issue_type="task",
        assignee="testuser",
    )
    created = await bd_client.create(params)

    assert created.assignee == "testuser"


@pytest.mark.asyncio
async def test_list_with_filters(bd_client):
    """Test listing issues with multiple filters."""
    # Create issues with different attributes
    await bd_client.create(
        CreateIssueParams(
            title="Bug P0",
            priority=0,
            issue_type="bug",
            assignee="alice",
        )
    )
    await bd_client.create(
        CreateIssueParams(
            title="Feature P1",
            priority=1,
            issue_type="feature",
            assignee="bob",
        )
    )

    # Filter by priority
    params = ListIssuesParams(priority=0)
    issues = await bd_client.list_issues(params)
    assert all(issue.priority == 0 for issue in issues)

    # Filter by type
    params = ListIssuesParams(issue_type="bug")
    issues = await bd_client.list_issues(params)
    assert all(issue.issue_type == "bug" for issue in issues)

    # Filter by assignee
    params = ListIssuesParams(assignee="alice")
    issues = await bd_client.list_issues(params)
    assert all(issue.assignee == "alice" for issue in issues)


@pytest.mark.asyncio
async def test_invalid_issue_id(bd_client):
    """Test showing non-existent issue."""
    params = ShowIssueParams(issue_id="test-999")

    with pytest.raises(BdCommandError, match="bd command failed"):
        await bd_client.show(params)


@pytest.mark.asyncio
async def test_dependency_types(bd_client):
    """Test different dependency types."""
    issue1 = await bd_client.create(
        CreateIssueParams(title="Issue 1", priority=1, issue_type="task")
    )
    issue2 = await bd_client.create(
        CreateIssueParams(title="Issue 2", priority=1, issue_type="task")
    )

    # Test related dependency
    params = AddDependencyParams(
        from_id=issue1.id, to_id=issue2.id, dep_type="related"
    )
    await bd_client.add_dependency(params)

    # Verify
    show_params = ShowIssueParams(issue_id=issue1.id)
    shown = await bd_client.show(show_params)
    assert len(shown.dependencies) > 0
