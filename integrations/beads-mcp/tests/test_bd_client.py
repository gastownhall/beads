"""Unit tests for BdClient."""

import asyncio
import json
from unittest.mock import AsyncMock, MagicMock, patch

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


@pytest.fixture
def bd_client():
    """Create a BdClient instance for testing."""
    return BdClient(bd_path="/usr/bin/bd", beads_db="/tmp/test.db")


@pytest.fixture
def mock_process():
    """Create a mock subprocess process."""
    process = MagicMock()
    process.returncode = 0
    process.communicate = AsyncMock(return_value=(b"", b""))
    return process


@pytest.mark.asyncio
async def test_bd_client_initialization():
    """Test BdClient initialization."""
    client = BdClient(bd_path="/usr/bin/bd", beads_db="/tmp/test.db")
    assert client.bd_path == "/usr/bin/bd"
    assert client.beads_db == "/tmp/test.db"


@pytest.mark.asyncio
async def test_bd_client_without_db():
    """Test BdClient initialization without database."""
    client = BdClient(bd_path="/usr/bin/bd")
    assert client.bd_path == "/usr/bin/bd"
    assert client.beads_db is None


@pytest.mark.asyncio
async def test_run_command_success(bd_client, mock_process):
    """Test successful command execution."""
    result_data = {"id": "bd-1", "title": "Test issue"}
    mock_process.communicate = AsyncMock(return_value=(json.dumps(result_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        result = await bd_client._run_command("show", "bd-1")

    assert result == result_data


@pytest.mark.asyncio
async def test_run_command_not_found(bd_client):
    """Test command execution when bd executable not found."""
    with (
        patch("asyncio.create_subprocess_exec", side_effect=FileNotFoundError()),
        pytest.raises(BdNotFoundError, match="bd command not found"),
    ):
        await bd_client._run_command("show", "bd-1")


@pytest.mark.asyncio
async def test_run_command_failure(bd_client, mock_process):
    """Test command execution failure."""
    mock_process.returncode = 1
    mock_process.communicate = AsyncMock(return_value=(b"", b"Error: Issue not found"))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="bd command failed"),
    ):
        await bd_client._run_command("show", "bd-999")


@pytest.mark.asyncio
async def test_run_command_invalid_json(bd_client, mock_process):
    """Test command execution with invalid JSON output."""
    mock_process.communicate = AsyncMock(return_value=(b"invalid json", b""))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="Failed to parse bd JSON output"),
    ):
        await bd_client._run_command("show", "bd-1")


@pytest.mark.asyncio
async def test_run_command_empty_output(bd_client, mock_process):
    """Test command execution with empty output."""
    mock_process.communicate = AsyncMock(return_value=(b"", b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        result = await bd_client._run_command("show", "bd-1")

    assert result == {}


@pytest.mark.asyncio
async def test_ready(bd_client, mock_process):
    """Test ready method."""
    issues_data = [
        {
            "id": "bd-1",
            "title": "Issue 1",
            "status": "open",
            "priority": 1,
            "issue_type": "bug",
            "created_at": "2025-01-25T00:00:00Z",
            "updated_at": "2025-01-25T00:00:00Z",
        },
        {
            "id": "bd-2",
            "title": "Issue 2",
            "status": "open",
            "priority": 2,
            "issue_type": "feature",
            "created_at": "2025-01-25T00:00:00Z",
            "updated_at": "2025-01-25T00:00:00Z",
        },
    ]
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issues_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = ReadyWorkParams(limit=10, priority=1)
        issues = await bd_client.ready(params)

    assert len(issues) == 2
    assert issues[0].id == "bd-1"
    assert issues[1].id == "bd-2"


@pytest.mark.asyncio
async def test_ready_with_assignee(bd_client, mock_process):
    """Test ready method with assignee filter."""
    issues_data = [
        {
            "id": "bd-1",
            "title": "Issue 1",
            "status": "open",
            "priority": 1,
            "issue_type": "bug",
            "created_at": "2024-01-01T00:00:00Z",
            "updated_at": "2024-01-01T00:00:00Z",
        },
    ]
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issues_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = ReadyWorkParams(limit=10, assignee="alice")
        issues = await bd_client.ready(params)

    assert len(issues) == 1
    assert issues[0].id == "bd-1"


@pytest.mark.asyncio
async def test_ready_invalid_response(bd_client, mock_process):
    """Test ready method with invalid response type."""
    mock_process.communicate = AsyncMock(
        return_value=(json.dumps({"error": "not a list"}).encode(), b"")
    )

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = ReadyWorkParams(limit=10)
        issues = await bd_client.ready(params)

    assert issues == []


@pytest.mark.asyncio
async def test_list_issues(bd_client, mock_process):
    """Test list_issues method."""
    issues_data = [
        {
            "id": "bd-1",
            "title": "Issue 1",
            "status": "open",
            "priority": 1,
            "issue_type": "bug",
            "created_at": "2024-01-01T00:00:00Z",
            "updated_at": "2024-01-01T00:00:00Z",
        },
    ]
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issues_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = ListIssuesParams(status="open", priority=1)
        issues = await bd_client.list_issues(params)

    assert len(issues) == 1
    assert issues[0].id == "bd-1"


@pytest.mark.asyncio
async def test_list_issues_invalid_response(bd_client, mock_process):
    """Test list_issues method with invalid response type."""
    mock_process.communicate = AsyncMock(
        return_value=(json.dumps({"error": "not a list"}).encode(), b"")
    )

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = ListIssuesParams(status="open")
        issues = await bd_client.list_issues(params)

    assert issues == []


@pytest.mark.asyncio
async def test_show(bd_client, mock_process):
    """Test show method."""
    issue_data = {
        "id": "bd-1",
        "title": "Test issue",
        "description": "Test description",
        "status": "open",
        "priority": 1,
        "issue_type": "bug",
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2024-01-01T00:00:00Z",
    }
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issue_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = ShowIssueParams(issue_id="bd-1")
        issue = await bd_client.show(params)

    assert issue.id == "bd-1"
    assert issue.title == "Test issue"


@pytest.mark.asyncio
async def test_show_invalid_response(bd_client, mock_process):
    """Test show method with invalid response type."""
    mock_process.communicate = AsyncMock(return_value=(json.dumps(["not a dict"]).encode(), b""))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="Invalid response for show"),
    ):
        params = ShowIssueParams(issue_id="bd-1")
        await bd_client.show(params)


@pytest.mark.asyncio
async def test_create(bd_client, mock_process):
    """Test create method."""
    issue_data = {
        "id": "bd-5",
        "title": "New issue",
        "description": "New description",
        "status": "open",
        "priority": 2,
        "issue_type": "feature",
        "created_at": "2024-01-01T00:00:00Z",
        "updated_at": "2025-01-25T00:00:00Z",
    }
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issue_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = CreateIssueParams(
            title="New issue",
            description="New description",
            priority=2,
            issue_type="feature",
        )
        issue = await bd_client.create(params)

    assert issue.id == "bd-5"
    assert issue.title == "New issue"


@pytest.mark.asyncio
async def test_create_with_optional_fields(bd_client, mock_process):
    """Test create method with all optional fields."""
    issue_data = {
        "id": "test-42",
        "title": "New issue",
        "description": "Full description",
        "design": "Design notes",
        "acceptance_criteria": "Acceptance criteria",
        "external_ref": "gh-123",
        "status": "open",
        "priority": 1,
        "issue_type": "feature",
        "created_at": "2025-01-25T00:00:00Z",
        "updated_at": "2025-01-25T00:00:00Z",
    }
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issue_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = CreateIssueParams(
            title="New issue",
            description="Full description",
            design="Design notes",
            acceptance="Acceptance criteria",
            external_ref="gh-123",
            priority=1,
            issue_type="feature",
            id="test-42",
            deps=["bd-1", "bd-2"],
        )
        issue = await bd_client.create(params)

    assert issue.id == "test-42"
    assert issue.title == "New issue"


@pytest.mark.asyncio
async def test_create_invalid_response(bd_client, mock_process):
    """Test create method with invalid response type."""
    mock_process.communicate = AsyncMock(return_value=(json.dumps(["not a dict"]).encode(), b""))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="Invalid response for create"),
    ):
        params = CreateIssueParams(title="Test", priority=1, issue_type="task")
        await bd_client.create(params)


@pytest.mark.asyncio
async def test_update(bd_client, mock_process):
    """Test update method."""
    issue_data = {
        "id": "bd-1",
        "title": "Updated title",
        "status": "in_progress",
        "priority": 1,
        "issue_type": "bug",
        "created_at": "2025-01-25T00:00:00Z",
        "updated_at": "2025-01-25T00:00:00Z",
    }
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issue_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = UpdateIssueParams(issue_id="bd-1", status="in_progress", title="Updated title")
        issue = await bd_client.update(params)

    assert issue.id == "bd-1"
    assert issue.status == "in_progress"


@pytest.mark.asyncio
async def test_update_with_optional_fields(bd_client, mock_process):
    """Test update method with all optional fields."""
    issue_data = {
        "id": "bd-1",
        "title": "Updated title",
        "design": "Design notes",
        "acceptance_criteria": "Acceptance criteria",
        "notes": "Additional notes",
        "external_ref": "gh-456",
        "status": "in_progress",
        "priority": 0,
        "issue_type": "bug",
        "created_at": "2025-01-25T00:00:00Z",
        "updated_at": "2025-01-25T00:00:00Z",
    }
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issue_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = UpdateIssueParams(
            issue_id="bd-1",
            assignee="alice",
            design="Design notes",
            acceptance_criteria="Acceptance criteria",
            notes="Additional notes",
            external_ref="gh-456",
        )
        issue = await bd_client.update(params)

    assert issue.id == "bd-1"
    assert issue.title == "Updated title"


@pytest.mark.asyncio
async def test_update_invalid_response(bd_client, mock_process):
    """Test update method with invalid response type."""
    mock_process.communicate = AsyncMock(return_value=(json.dumps(["not a dict"]).encode(), b""))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="Invalid response for update"),
    ):
        params = UpdateIssueParams(issue_id="bd-1", status="in_progress")
        await bd_client.update(params)


@pytest.mark.asyncio
async def test_close(bd_client, mock_process):
    """Test close method."""
    issues_data = [
        {
            "id": "bd-1",
            "title": "Closed issue",
            "status": "closed",
            "priority": 1,
            "issue_type": "bug",
            "created_at": "2025-01-25T00:00:00Z",
            "updated_at": "2025-01-25T00:00:00Z",
            "closed_at": "2025-01-25T01:00:00Z",
        }
    ]
    mock_process.communicate = AsyncMock(return_value=(json.dumps(issues_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = CloseIssueParams(issue_id="bd-1", reason="Completed")
        issues = await bd_client.close(params)

    assert len(issues) == 1
    assert issues[0].status == "closed"


@pytest.mark.asyncio
async def test_close_invalid_response(bd_client, mock_process):
    """Test close method with invalid response type."""
    mock_process.communicate = AsyncMock(
        return_value=(json.dumps({"error": "not a list"}).encode(), b"")
    )

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="Invalid response for close"),
    ):
        params = CloseIssueParams(issue_id="bd-1", reason="Test")
        await bd_client.close(params)


@pytest.mark.asyncio
async def test_add_dependency(bd_client, mock_process):
    """Test add_dependency method."""
    mock_process.communicate = AsyncMock(return_value=(b"Dependency added\n", b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        params = AddDependencyParams(from_id="bd-2", to_id="bd-1", dep_type="blocks")
        await bd_client.add_dependency(params)

    # Should complete without raising an exception


@pytest.mark.asyncio
async def test_add_dependency_failure(bd_client, mock_process):
    """Test add_dependency with failure."""
    mock_process.returncode = 1
    mock_process.communicate = AsyncMock(return_value=(b"", b"Dependency already exists"))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="bd dep add failed"),
    ):
        params = AddDependencyParams(from_id="bd-2", to_id="bd-1", dep_type="blocks")
        await bd_client.add_dependency(params)


@pytest.mark.asyncio
async def test_add_dependency_not_found(bd_client):
    """Test add_dependency when bd executable not found."""
    with (
        patch("asyncio.create_subprocess_exec", side_effect=FileNotFoundError()),
        pytest.raises(BdNotFoundError, match="bd command not found"),
    ):
        params = AddDependencyParams(from_id="bd-2", to_id="bd-1", dep_type="blocks")
        await bd_client.add_dependency(params)


@pytest.mark.asyncio
async def test_quickstart(bd_client, mock_process):
    """Test quickstart method."""
    quickstart_text = "# Beads Quickstart\n\nWelcome to beads..."
    mock_process.communicate = AsyncMock(return_value=(quickstart_text.encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        result = await bd_client.quickstart()

    assert result == quickstart_text


@pytest.mark.asyncio
async def test_quickstart_failure(bd_client, mock_process):
    """Test quickstart with failure."""
    mock_process.returncode = 1
    mock_process.communicate = AsyncMock(return_value=(b"", b"Command not found"))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="bd quickstart failed"),
    ):
        await bd_client.quickstart()


@pytest.mark.asyncio
async def test_quickstart_not_found(bd_client):
    """Test quickstart when bd executable not found."""
    with (
        patch("asyncio.create_subprocess_exec", side_effect=FileNotFoundError()),
        pytest.raises(BdNotFoundError, match="bd command not found"),
    ):
        await bd_client.quickstart()


@pytest.mark.asyncio
async def test_stats(bd_client, mock_process):
    """Test stats method."""
    stats_data = {
        "total_issues": 10,
        "open_issues": 5,
        "in_progress_issues": 2,
        "closed_issues": 3,
        "blocked_issues": 1,
        "ready_issues": 4,
        "average_lead_time_hours": 24.5,
    }
    mock_process.communicate = AsyncMock(return_value=(json.dumps(stats_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        result = await bd_client.stats()

    assert result.total_issues == 10
    assert result.open_issues == 5


@pytest.mark.asyncio
async def test_stats_invalid_response(bd_client, mock_process):
    """Test stats method with invalid response type."""
    mock_process.communicate = AsyncMock(return_value=(json.dumps(["not a dict"]).encode(), b""))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="Invalid response for stats"),
    ):
        await bd_client.stats()


@pytest.mark.asyncio
async def test_blocked(bd_client, mock_process):
    """Test blocked method."""
    blocked_data = [
        {
            "id": "bd-1",
            "title": "Blocked issue",
            "status": "blocked",
            "priority": 1,
            "issue_type": "bug",
            "created_at": "2025-01-25T00:00:00Z",
            "updated_at": "2025-01-25T00:00:00Z",
            "blocked_by_count": 2,
            "blocked_by": ["bd-2", "bd-3"],
        }
    ]
    mock_process.communicate = AsyncMock(return_value=(json.dumps(blocked_data).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        result = await bd_client.blocked()

    assert len(result) == 1
    assert result[0].id == "bd-1"
    assert result[0].blocked_by_count == 2


@pytest.mark.asyncio
async def test_blocked_invalid_response(bd_client, mock_process):
    """Test blocked method with invalid response type."""
    mock_process.communicate = AsyncMock(
        return_value=(json.dumps({"error": "not a list"}).encode(), b"")
    )

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        result = await bd_client.blocked()

    assert result == []


@pytest.mark.asyncio
async def test_init(bd_client, mock_process):
    """Test init method."""
    init_output = "bd initialized successfully!"
    mock_process.communicate = AsyncMock(return_value=(init_output.encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        from beads_mcp.models import InitParams

        params = InitParams(prefix="test")
        result = await bd_client.init(params)

    assert "bd initialized successfully!" in result


@pytest.mark.asyncio
async def test_init_failure(bd_client, mock_process):
    """Test init method with command failure."""
    mock_process.returncode = 1
    mock_process.communicate = AsyncMock(return_value=(b"", b"Failed to initialize"))

    with (
        patch("asyncio.create_subprocess_exec", return_value=mock_process),
        pytest.raises(BdCommandError, match="bd init failed"),
    ):
        await bd_client.init()
