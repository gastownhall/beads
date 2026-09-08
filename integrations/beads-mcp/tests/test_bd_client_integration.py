"""Real integration tests for BdClient using actual bd binary."""

import os
import shutil
import subprocess
from pathlib import Path

import pytest

from beads_mcp.bd_client import BdClient, BdCommandError
from beads_mcp.models import (
    AddDependencyParams,
    ClaimIssueParams,
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


@pytest.fixture(scope="session")
def bd_init_isolation_flags_supported(bd_executable):
    """Skip integration tests when bd lacks the init flags these tests require."""
    result = subprocess.run(
        [bd_executable, "init", "--help"],
        capture_output=True,
        text=True,
        check=False,
    )
    help_text = f"{result.stdout}\n{result.stderr}"
    required_flags = ("--non-interactive", "--skip-agents", "--skip-hooks")
    missing_flags = [flag for flag in required_flags if flag not in help_text]
    if missing_flags:
        pytest.skip(
            "bd init does not support required test isolation flags: "
            + ", ".join(missing_flags)
            + ". Install a current bd from this repository before running integration tests."
        )


@pytest.fixture
def temp_workspace(tmp_path: Path) -> Path:
    """Create a per-test workspace isolated with its own BEADS_DIR."""
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    return workspace


@pytest.fixture
async def bd_client(bd_executable, temp_workspace, monkeypatch, bd_init_isolation_flags_supported):
    """Create BdClient with a temporary Dolt workspace - fully hermetic."""
    beads_dir = temp_workspace / ".beads"
    monkeypatch.delenv("BEADS_DB", raising=False)
    monkeypatch.delenv("BEADS_DIR", raising=False)
    monkeypatch.delenv("BD_DB", raising=False)
    monkeypatch.delenv("BEADS_WORKING_DIR", raising=False)

    client = BdClient(
        bd_path=bd_executable,
        beads_dir=str(beads_dir),
        beads_db="",
        working_dir=str(temp_workspace),
    )

    env = os.environ.copy()
    env["BEADS_DIR"] = str(beads_dir)
    env["BD_NON_INTERACTIVE"] = "1"

    import asyncio

    process = await asyncio.create_subprocess_exec(
        bd_executable,
        "init",
        "--prefix",
        "test",
        "--non-interactive",
        "--skip-agents",
        "--skip-hooks",
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=env,
        cwd=temp_workspace,
    )
    _stdout, stderr = await process.communicate()

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
    list_params = ListIssuesParams()
    issues = await bd_client.list_issues(list_params)

    assert len(issues) >= 3

    # List with status filter
    list_params_filtered = ListIssuesParams(status="open")
    issues = await bd_client.list_issues(list_params_filtered)

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
        status="blocked",
        priority=0,
        title="Updated title",
    )
    updated = await bd_client.update(update_params)

    assert updated.id == created.id
    assert updated.status == "blocked"
    assert updated.priority == 0
    assert updated.title == "Updated title"


@pytest.mark.asyncio
async def test_claim_issue(bd_client):
    """Test atomic claim with real bd."""
    created = await bd_client.create(CreateIssueParams(title="Issue to claim", priority=2, issue_type="task"))

    claimed = await bd_client.claim(ClaimIssueParams(issue_id=created.id))

    assert claimed.id == created.id
    assert claimed.status == "in_progress"
    assert claimed.assignee is not None


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
async def test_init_creates_beads_directory(
    bd_executable, tmp_path, monkeypatch, bd_init_isolation_flags_supported
):
    """Test that init creates .beads directory in current working directory.

    This is a critical test for the bug where init was using --db flag
    and creating the database in the wrong location.
    """
    from beads_mcp.bd_client import BdClient
    from beads_mcp.models import InitParams

    temp_path = tmp_path / "init-workspace"
    temp_path.mkdir()
    beads_dir = temp_path / ".beads"
    monkeypatch.setenv("BD_NON_INTERACTIVE", "1")
    monkeypatch.delenv("BEADS_DIR", raising=False)
    monkeypatch.delenv("BEADS_DB", raising=False)

    # Ensure .beads doesn't exist yet
    assert not beads_dir.exists()

    # Create client WITHOUT beads_db set and WITH working_dir set to temp_dir
    client = BdClient(
        bd_path=bd_executable,
        beads_dir=str(beads_dir),
        beads_db=None,
        working_dir=str(temp_path),
    )

    # Initialize with custom prefix (no need to chdir!)
    params = InitParams(prefix="test")
    result = await client.init(params)

    # Verify .beads directory was created in temp directory
    assert beads_dir.exists(), f".beads directory not created in {temp_path}"
    assert beads_dir.is_dir(), ".beads exists but is not a directory"

    # Verify Dolt backend files were created under the isolated .beads directory.
    assert (beads_dir / "metadata.json").is_file()
    assert (beads_dir / "config.yaml").is_file()
    embedded_dir = beads_dir / "embeddeddolt"
    assert embedded_dir.exists(), "No embedded Dolt directory created in .beads/"
    assert any(path.name == ".dolt" for path in embedded_dir.glob("*/.dolt")), (
        f"Expected an embedded Dolt database under {embedded_dir}"
    )

    # Verify success message
    assert "initialized" in result.lower() or "created" in result.lower()
