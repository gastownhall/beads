"""Real integration tests for BdClient using actual bd binary."""

import os
import shutil
import tempfile
from pathlib import Path

import pytest

from beads_mcp.bd_client import BdClient, BdCommandError
from beads_mcp.models import (
    AddDependencyParams,
    CloseIssueParams,
    CreateIssueParams,
    ListIssuesParams,
    ReadyWorkParams,
    ReopenIssueParams,
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
async def test_reopen_issue(bd_client):
    """Test reopening a closed issue with real bd."""
    # Create issue
    create_params = CreateIssueParams(
        title="BG's issue to reopen",
        priority=1,
        issue_type="bug",
    )
    created = await bd_client.create(create_params)

    # Close issue
    close_params = CloseIssueParams(issue_id=created.id, reason="Testing complete")
    await bd_client.close(close_params)

    # Reopen issue
    reopen_params = ReopenIssueParams(issue_ids=[created.id])
    reopened_issues = await bd_client.reopen(reopen_params)

    assert len(reopened_issues) >= 1
    reopened = reopened_issues[0]
    assert reopened.id == created.id
    assert reopened.status == "open"
    assert reopened.closed_at is None


@pytest.mark.asyncio
async def test_reopen_multiple_issues(bd_client):
    """Test reopening multiple closed issues with real bd."""
    # Create and close two issues
    issue1 = await bd_client.create(CreateIssueParams(title="Issue 1 to reopen", priority=1, issue_type="task"))
    issue2 = await bd_client.create(CreateIssueParams(title="Issue 2 to reopen", priority=1, issue_type="task"))

    await bd_client.close(CloseIssueParams(issue_id=issue1.id, reason="Done"))
    await bd_client.close(CloseIssueParams(issue_id=issue2.id, reason="Done"))

    # Reopen both issues
    reopen_params = ReopenIssueParams(issue_ids=[issue1.id, issue2.id])
    reopened_issues = await bd_client.reopen(reopen_params)

    assert len(reopened_issues) == 2
    reopened_ids = {issue.id for issue in reopened_issues}
    assert issue1.id in reopened_ids
    assert issue2.id in reopened_ids
    assert all(issue.status == "open" for issue in reopened_issues)
    assert all(issue.closed_at is None for issue in reopened_issues)


@pytest.mark.asyncio
async def test_reopen_with_reason(bd_client):
    """Test reopening an issue with reason parameter."""
    # Create and close issue
    created = await bd_client.create(
        CreateIssueParams(title="Issue to reopen with reason", priority=1, issue_type="bug")
    )
    await bd_client.close(CloseIssueParams(issue_id=created.id, reason="Done"))

    # Reopen with reason
    reopen_params = ReopenIssueParams(issue_ids=[created.id], reason="BG found a regression in production")
    reopened_issues = await bd_client.reopen(reopen_params)

    assert len(reopened_issues) >= 1
    reopened = reopened_issues[0]
    assert reopened.id == created.id
    assert reopened.status == "open"
    assert reopened.closed_at is None


@pytest.mark.asyncio
async def test_add_dependency(bd_client):
    """Test adding dependencies with real bd."""
    # Create two issues
    issue1 = await bd_client.create(CreateIssueParams(title="Issue 1", priority=1, issue_type="task"))
    issue2 = await bd_client.create(CreateIssueParams(title="Issue 2", priority=1, issue_type="task"))

    # Add dependency: issue2 blocks issue1
    params = AddDependencyParams(issue_id=issue1.id, depends_on_id=issue2.id, dep_type="blocks")
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
    ready_issue = await bd_client.create(CreateIssueParams(title="Ready issue", priority=1, issue_type="task"))

    # Create blocked issue
    blocking_issue = await bd_client.create(
        CreateIssueParams(title="Blocking issue", priority=1, issue_type="task")
    )
    blocked_issue = await bd_client.create(CreateIssueParams(title="Blocked issue", priority=1, issue_type="task"))

    # Add blocking dependency
    await bd_client.add_dependency(
        AddDependencyParams(
            issue_id=blocked_issue.id,
            depends_on_id=blocking_issue.id,
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
    issue1 = await bd_client.create(CreateIssueParams(title="Issue 1", priority=1, issue_type="task"))
    issue2 = await bd_client.create(CreateIssueParams(title="Issue 2", priority=1, issue_type="task"))

    # Test related dependency
    params = AddDependencyParams(issue_id=issue1.id, depends_on_id=issue2.id, dep_type="related")
    await bd_client.add_dependency(params)

    # Verify
    show_params = ShowIssueParams(issue_id=issue1.id)
    shown = await bd_client.show(show_params)
    assert len(shown.dependencies) > 0


@pytest.mark.asyncio
async def test_init_creates_beads_directory(bd_executable):
    """Test that init creates .beads directory in current working directory.

    This is a critical test for the bug where init was using --db flag
    and creating the database in the wrong location.
    """
    from beads_mcp.bd_client import BdClient
    from beads_mcp.models import InitParams

    # Create a temporary directory to test in
    with tempfile.TemporaryDirectory(prefix="beads_init_test_", dir="/tmp") as temp_dir:
        temp_path = Path(temp_dir)
        beads_dir = temp_path / ".beads"

        # Ensure .beads doesn't exist yet
        assert not beads_dir.exists()

        # Create client WITHOUT beads_db set and WITH working_dir set to temp_dir
        client = BdClient(bd_path=bd_executable, beads_db=None, working_dir=temp_dir)

        # Initialize with custom prefix (no need to chdir!)
        params = InitParams(prefix="test")
        result = await client.init(params)

        # Verify .beads directory was created in temp directory
        assert beads_dir.exists(), f".beads directory not created in {temp_dir}"
        assert beads_dir.is_dir(), ".beads exists but is not a directory"

        # Verify database file was created (always named beads.db, prefix is for issue IDs)
        db_files = list(beads_dir.glob("*.db"))
        assert len(db_files) > 0, "No database file created in .beads/"
        assert any("beads.db" == db.name for db in db_files), (
            f"Expected beads.db database file: {[db.name for db in db_files]}"
        )

        # Verify success message
        assert "initialized" in result.lower() or "created" in result.lower()
