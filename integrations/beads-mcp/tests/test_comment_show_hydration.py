"""Opt-in comment hydration through the MCP show path (bd-kwe4ze).

AC-2: show is count-only by default (AC-1); include_comments=True or a
fields selection containing "comments" is the explicit opt-in that adds
--include-comments to the single bd show command, before the operand
separator, so the response carries the full comment chronology in CLI
order.
"""

import json
from datetime import UTC, datetime
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from beads_mcp import server, tools
from beads_mcp.bd_client import BdCliClient
from beads_mcp.models import Issue, ShowIssueParams

NOW = datetime(2026, 1, 1, 0, 0, 0, tzinfo=UTC)


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


def _hydrated_payload() -> dict:
    """CLI `bd show --json --include-comments` output for a two-comment issue."""
    return {
        "id": "bd-1",
        "title": "Test issue",
        "status": "open",
        "priority": 2,
        "issue_type": "task",
        "created_at": "2026-01-01T00:00:00Z",
        "updated_at": "2026-01-01T00:00:00Z",
        "comments": [
            {
                "id": "c1",
                "issue_id": "bd-1",
                "author": "a",
                "text": "first",
                "created_at": "2026-01-01T00:00:01Z",
            },
            {
                "id": "c2",
                "issue_id": "bd-1",
                "author": "a",
                "text": "second",
                "created_at": "2026-01-01T00:00:02Z",
            },
        ],
        "comment_count": 2,
    }


def _hydrated_issue() -> Issue:
    """An Issue as the client returns it from _hydrated_payload()."""
    return Issue.model_validate(_hydrated_payload())


# =============================================================================
# Client: argv construction
# =============================================================================


@pytest.mark.asyncio
async def test_show_include_comments_puts_flag_before_operand_separator(bd_client, mock_process):
    """--include-comments rides on the one show command, ahead of the '--' separator."""
    mock_process.communicate = AsyncMock(return_value=(json.dumps([_hydrated_payload()]).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process) as exec_mock:
        issue = await bd_client.show(ShowIssueParams(issue_id="bd-1", include_comments=True))

    argv = exec_mock.call_args.args
    assert argv[1] == "show"
    assert "--include-comments" in argv
    assert argv.index("--include-comments") < argv.index("--")
    exec_mock.assert_called_once()
    # The chronology arrives whole, in CLI order, on the full model.
    assert [c.text for c in (issue.comments or [])] == ["first", "second"]
    assert issue.comment_count == 2


@pytest.mark.asyncio
async def test_show_include_comments_false_omits_flag(bd_client, mock_process):
    """The default stays count-only: no flag, no second command."""
    payload = _hydrated_payload()
    del payload["comments"]
    payload["comments_omitted"] = True
    mock_process.communicate = AsyncMock(return_value=(json.dumps([payload]).encode(), b""))

    with patch("asyncio.create_subprocess_exec", return_value=mock_process) as exec_mock:
        await bd_client.show(ShowIssueParams(issue_id="bd-1", include_comments=False))

    argv = exec_mock.call_args.args
    assert "--include-comments" not in argv
    exec_mock.assert_called_once()


# =============================================================================
# Tools: parameter forwarding
# =============================================================================


@pytest.mark.asyncio
async def test_beads_show_issue_forwards_include_comments():
    """tools.beads_show_issue forwards include_comments into ShowIssueParams."""
    mock_client = AsyncMock()
    mock_client.show = AsyncMock(return_value=_hydrated_issue())

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        issue = await tools.beads_show_issue(issue_id="bd-1", include_comments=True)

    params = mock_client.show.call_args[0][0]
    assert params.include_comments is True
    assert [c.text for c in (issue.comments or [])] == ["first", "second"]


@pytest.mark.asyncio
async def test_beads_show_issue_defaults_to_count_only():
    """The tools default keeps the count-only read."""
    mock_client = AsyncMock()
    mock_client.show = AsyncMock(return_value=_hydrated_issue())

    with patch("beads_mcp.tools._get_client", return_value=mock_client):
        await tools.beads_show_issue(issue_id="bd-1")

    params = mock_client.show.call_args[0][0]
    assert params.include_comments is False


# =============================================================================
# Server tool: hydration decision and response shape
# =============================================================================


@pytest.mark.asyncio
async def test_show_tool_hydrates_when_include_comments(monkeypatch):
    """include_comments=True makes the server request bodies on its one show call."""
    shown = _hydrated_issue()
    mock_show = AsyncMock(return_value=shown)
    monkeypatch.setattr(server, "beads_show_issue", mock_show)

    result = await server.show_issue(issue_id="bd-1", include_comments=True)

    mock_show.assert_called_once_with(issue_id="bd-1", include_comments=True)
    assert [c.text for c in (result.comments or [])] == ["first", "second"]
    assert result.comment_count == 2


@pytest.mark.asyncio
async def test_show_tool_hydrates_when_fields_select_comments(monkeypatch):
    """A fields projection naming comments also opts into hydration.

    The projection itself (which fields it admits and the omission companion
    it adds) is covered with the real _filter_fields in the AC-5 tests; here
    the stub keeps this test about the hydration decision only.
    """
    mock_show = AsyncMock(return_value=_hydrated_issue())
    monkeypatch.setattr(server, "beads_show_issue", mock_show)

    def fake_filter(obj, fields):
        return {"comments": [c.model_dump() for c in obj.comments or []]}

    monkeypatch.setattr(server, "_filter_fields", fake_filter)

    result = await server.show_issue(issue_id="bd-1", fields=["comments"])

    mock_show.assert_called_once_with(issue_id="bd-1", include_comments=True)
    assert [c["text"] for c in result["comments"]] == ["first", "second"]


@pytest.mark.asyncio
async def test_show_tool_non_comment_fields_do_not_hydrate(monkeypatch):
    """A projection that never names comments stays count-only."""
    mock_show = AsyncMock(return_value=_hydrated_issue())
    monkeypatch.setattr(server, "beads_show_issue", mock_show)

    await server.show_issue(issue_id="bd-1", fields=["description"])

    mock_show.assert_called_once_with(issue_id="bd-1", include_comments=False)


@pytest.mark.asyncio
async def test_show_tool_default_does_not_hydrate(monkeypatch):
    """Default show stays count-only."""
    mock_show = AsyncMock(return_value=_hydrated_issue())
    monkeypatch.setattr(server, "beads_show_issue", mock_show)

    await server.show_issue(issue_id="bd-1")

    mock_show.assert_called_once_with(issue_id="bd-1", include_comments=False)
