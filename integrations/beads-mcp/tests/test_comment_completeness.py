"""Comment-completeness preservation in the MCP layer (bd-kwe4ze).

The CLI's --json surfaces describe their own comment completeness: list rows
carry comment_count/comments_omitted (types.IssueWithCounts), and show detail
carries comment_count/comments_omitted without bodies unless --include-comments
was passed (types.IssueDetails, ga-clgh). The MCP models must parse and keep
those signals instead of silently dropping the extra JSON fields.
"""

import json
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from beads_mcp.bd_client import BdCliClient
from beads_mcp.models import ShowIssueParams


@pytest.fixture
def bd_client():
    """Create a BdCliClient instance for testing."""
    return BdCliClient(bd_path="/usr/bin/bd", beads_db="/tmp/test.db")


@pytest.fixture
def mock_process():
    """Create a mock subprocess process."""
    process = MagicMock()
    process.returncode = 0
    process.communicate = AsyncMock(return_value=(b"", b""))
    return process


def _show_payload(
    comment_count: int | None = 2,
    comments_omitted: bool | None = True,
) -> dict:
    """Build CLI show --json output: count-only unless bodies are supplied."""
    issue = {
        "id": "bd-1",
        "title": "Test issue",
        "status": "open",
        "priority": 2,
        "issue_type": "task",
        "created_at": "2026-01-01T00:00:00Z",
        "updated_at": "2026-01-01T00:00:00Z",
    }
    if comment_count is not None:
        issue["comment_count"] = comment_count
    if comments_omitted is not None:
        issue["comments_omitted"] = comments_omitted
    return issue


# =============================================================================
# AC-1: default show preserves the CLI's completeness metadata
# =============================================================================


@pytest.mark.asyncio
async def test_show_default_preserves_comment_completeness(bd_client, mock_process):
    """Default show: count/omitted pass through, bodies stay null, no --include-comments."""
    mock_process.communicate = AsyncMock(return_value=(json.dumps(_show_payload()).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process) as exec_mock:
        issue = await bd_client.show(ShowIssueParams(issue_id="bd-1"))

    assert issue.comment_count == 2
    assert issue.comments_omitted is True
    assert issue.comments is None
    # Count-only read: the client must not ask the CLI for comment bodies.
    argv = exec_mock.call_args.args
    assert "--include-comments" not in argv


@pytest.mark.asyncio
async def test_show_array_envelope_preserves_comment_completeness(bd_client, mock_process):
    """bd show returns an array; the client unwraps it before validation."""
    mock_process.communicate = AsyncMock(return_value=(json.dumps([_show_payload()]).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process):
        issue = await bd_client.show(ShowIssueParams(issue_id="bd-1"))

    assert issue.comment_count == 2
    assert issue.comments_omitted is True
    assert issue.comments is None
