"""Integration tests for MCP tools."""

from unittest.mock import AsyncMock, patch

import pytest

from beads_mcp.bd_client import BdClient
from beads_mcp.models import BlockedIssue, Issue, IssueStatus, IssueType, Stats
from beads_mcp.tools import (
    beads_add_dependency,
    beads_blocked,
    beads_close_issue,
    beads_create_issue,
    beads_init,
    beads_list_issues,
    beads_quickstart,
    beads_ready_work,
    beads_show_issue,
    beads_stats,
    beads_update_issue,
)


@pytest.fixture(autouse=True)
def mock_client():
    """Mock the BdClient for all tests."""
    from beads_mcp import tools

    # Reset client before each test
    tools._client = None
    yield
    # Reset client after each test
    tools._client = None


@pytest.fixture
def sample_issue():
    """Create a sample issue for testing."""
    return Issue(
        id="bd-1",
        title="Test issue",
        description="Test description",
        status="open",
        priority=1,
        issue_type="bug",
        created_at="2024-01-01T00:00:00Z",
        updated_at="2024-01-01T00:00:00Z",
    )


@pytest.mark.asyncio
async def test_beads_ready_work(sample_issue):
    """Test beads_ready_work tool."""
    mock_client = AsyncMock()
    mock_client.ready = AsyncMock(return_value=[sample_issue])

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issues = await beads_ready_work(limit=10, priority=1)

    assert len(issues) == 1
    assert issues[0].id == "bd-1"
    mock_client.ready.assert_called_once()


@pytest.mark.asyncio
async def test_beads_ready_work_no_params():
    """Test beads_ready_work with default parameters."""
    mock_client = AsyncMock()
    mock_client.ready = AsyncMock(return_value=[])

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issues = await beads_ready_work()

    assert len(issues) == 0
    mock_client.ready.assert_called_once()


@pytest.mark.asyncio
async def test_beads_list_issues(sample_issue):
    """Test beads_list_issues tool."""
    mock_client = AsyncMock()
    mock_client.list_issues = AsyncMock(return_value=[sample_issue])

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issues = await beads_list_issues(status="open", priority=1)

    assert len(issues) == 1
    assert issues[0].id == "bd-1"
    mock_client.list_issues.assert_called_once()


@pytest.mark.asyncio
async def test_beads_show_issue(sample_issue):
    """Test beads_show_issue tool."""
    mock_client = AsyncMock()
    mock_client.show = AsyncMock(return_value=sample_issue)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issue = await beads_show_issue(issue_id="bd-1")

    assert issue.id == "bd-1"
    assert issue.title == "Test issue"
    mock_client.show.assert_called_once()


@pytest.mark.asyncio
async def test_beads_create_issue(sample_issue):
    """Test beads_create_issue tool."""
    mock_client = AsyncMock()
    mock_client.create = AsyncMock(return_value=sample_issue)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issue = await beads_create_issue(
            title="New issue",
            description="New description",
            priority=2,
            issue_type="feature",
        )

    assert issue.id == "bd-1"
    mock_client.create.assert_called_once()


@pytest.mark.asyncio
async def test_beads_create_issue_with_labels(sample_issue):
    """Test beads_create_issue with labels."""
    mock_client = AsyncMock()
    mock_client.create = AsyncMock(return_value=sample_issue)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issue = await beads_create_issue(
            title="New issue", labels=["bug", "urgent"]
        )

    assert issue.id == "bd-1"
    mock_client.create.assert_called_once()


@pytest.mark.asyncio
async def test_beads_update_issue(sample_issue):
    """Test beads_update_issue tool."""
    updated_issue = sample_issue.model_copy(
        update={"status": "in_progress"}
    )
    mock_client = AsyncMock()
    mock_client.update = AsyncMock(return_value=updated_issue)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issue = await beads_update_issue(issue_id="bd-1", status="in_progress")

    assert issue.status == "in_progress"
    mock_client.update.assert_called_once()


@pytest.mark.asyncio
async def test_beads_close_issue(sample_issue):
    """Test beads_close_issue tool."""
    closed_issue = sample_issue.model_copy(
        update={"status": "closed", "closed_at": "2024-01-02T00:00:00Z"}
    )
    mock_client = AsyncMock()
    mock_client.close = AsyncMock(return_value=[closed_issue])

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issues = await beads_close_issue(issue_id="bd-1", reason="Completed")

    assert len(issues) == 1
    assert issues[0].status == "closed"
    mock_client.close.assert_called_once()


@pytest.mark.asyncio
async def test_beads_add_dependency_success():
    """Test beads_add_dependency tool success."""
    mock_client = AsyncMock()
    mock_client.add_dependency = AsyncMock(return_value=None)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        result = await beads_add_dependency(
            from_id="bd-2", to_id="bd-1", dep_type="blocks"
        )

    assert "Added dependency" in result
    assert "bd-2" in result
    assert "bd-1" in result
    mock_client.add_dependency.assert_called_once()


@pytest.mark.asyncio
async def test_beads_add_dependency_error():
    """Test beads_add_dependency tool error handling."""
    from beads_mcp.bd_client import BdError

    mock_client = AsyncMock()
    mock_client.add_dependency = AsyncMock(
        side_effect=BdError("Dependency already exists")
    )

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        result = await beads_add_dependency(
            from_id="bd-2", to_id="bd-1", dep_type="blocks"
        )

    assert "Error" in result
    mock_client.add_dependency.assert_called_once()


@pytest.mark.asyncio
async def test_beads_quickstart():
    """Test beads_quickstart tool."""
    quickstart_text = "# Beads Quickstart\n\nWelcome to beads..."
    mock_client = AsyncMock()
    mock_client.quickstart = AsyncMock(return_value=quickstart_text)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        result = await beads_quickstart()

    assert "Beads Quickstart" in result
    mock_client.quickstart.assert_called_once()


@pytest.mark.asyncio
async def test_client_lazy_initialization():
    """Test that client is lazily initialized on first use."""
    from beads_mcp import tools

    # Clear client
    tools._client = None

    # Verify client is None before first use
    assert tools._client is None

    # Mock BdClient to avoid actual bd calls
    mock_client_instance = AsyncMock()
    mock_client_instance.ready = AsyncMock(return_value=[])

    with patch("beads_mcp.tools.BdClient") as MockBdClient:
        MockBdClient.return_value = mock_client_instance

        # First call should initialize client
        await beads_ready_work()

        # Verify BdClient was instantiated
        MockBdClient.assert_called_once()

        # Verify client is now set
        assert tools._client is not None

        # Second call should reuse client
        MockBdClient.reset_mock()
        await beads_ready_work()

        # Verify BdClient was NOT called again
        MockBdClient.assert_not_called()


@pytest.mark.asyncio
async def test_list_issues_with_all_filters(sample_issue):
    """Test beads_list_issues with all filter parameters."""
    mock_client = AsyncMock()
    mock_client.list_issues = AsyncMock(return_value=[sample_issue])

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issues = await beads_list_issues(
            status="open",
            priority=1,
            issue_type="bug",
            assignee="user1",
            limit=100,
        )

    assert len(issues) == 1
    mock_client.list_issues.assert_called_once()


@pytest.mark.asyncio
async def test_update_issue_multiple_fields(sample_issue):
    """Test beads_update_issue with multiple fields."""
    updated_issue = sample_issue.model_copy(
        update={
            "status": "in_progress",
            "priority": 0,
            "title": "Updated title",
        }
    )
    mock_client = AsyncMock()
    mock_client.update = AsyncMock(return_value=updated_issue)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issue = await beads_update_issue(
            issue_id="bd-1",
            status="in_progress",
            priority=0,
            title="Updated title",
        )

    assert issue.status == "in_progress"
    assert issue.priority == 0
    assert issue.title == "Updated title"
    mock_client.update.assert_called_once()


@pytest.mark.asyncio
async def test_beads_stats():
    """Test beads_stats tool."""
    stats_data = Stats(
        total_issues=10,
        open_issues=5,
        in_progress_issues=2,
        closed_issues=3,
        blocked_issues=1,
        ready_issues=4,
        average_lead_time_hours=24.5,
    )
    mock_client = AsyncMock()
    mock_client.stats = AsyncMock(return_value=stats_data)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        result = await beads_stats()

    assert result.total_issues == 10
    assert result.open_issues == 5
    mock_client.stats.assert_called_once()


@pytest.mark.asyncio
async def test_beads_blocked():
    """Test beads_blocked tool."""
    blocked_issue = BlockedIssue(
        id="bd-1",
        title="Blocked issue",
        description="",
        status="blocked",
        priority=1,
        issue_type="bug",
        created_at="2024-01-01T00:00:00Z",
        updated_at="2024-01-01T00:00:00Z",
        blocked_by_count=2,
        blocked_by=["bd-2", "bd-3"],
    )
    mock_client = AsyncMock()
    mock_client.blocked = AsyncMock(return_value=[blocked_issue])

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        result = await beads_blocked()

    assert len(result) == 1
    assert result[0].id == "bd-1"
    assert result[0].blocked_by_count == 2
    mock_client.blocked.assert_called_once()


@pytest.mark.asyncio
async def test_beads_init():
    """Test beads_init tool."""
    init_output = "bd initialized successfully!"
    mock_client = AsyncMock()
    mock_client.init = AsyncMock(return_value=init_output)

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        result = await beads_init(prefix="test")

    assert "bd initialized successfully!" in result
    mock_client.init.assert_called_once()
