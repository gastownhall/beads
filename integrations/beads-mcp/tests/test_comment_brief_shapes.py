"""Brief read shapes stay intact around comment completeness (bd-kwe4ze).

AC-6: hydration and completeness metadata must not leak into brief reads.
Every BriefIssue has exactly {id, title, status, priority}; brief show never
requests bodies, even when include_comments or a comments field selection
accompanies it; brief_deps keeps the full Issue's comment metadata (a brief
dependency is compact, not incomplete); brief mutation OperationResults keep
their exact shapes.
"""

from datetime import UTC, datetime
from unittest.mock import AsyncMock

import pytest

from beads_mcp import server
from beads_mcp.models import BlockedIssue, Issue, LinkedIssue, OperationResult

NOW = datetime(2026, 1, 1, 0, 0, 0, tzinfo=UTC)
BRIEF_KEYS = {"id", "title", "status", "priority"}


def _body(i: int) -> dict:
    return {
        "id": f"c{i}",
        "issue_id": "bd-1",
        "author": "a",
        "text": f"comment {i}",
        "created_at": "2026-01-01T00:00:00Z",
    }


def _issue(**overrides) -> Issue:
    payload = {
        "id": "bd-1",
        "title": "Test issue",
        "status": "open",
        "priority": 2,
        "issue_type": "task",
        "created_at": "2026-01-01T00:00:00Z",
        "updated_at": "2026-01-01T00:00:00Z",
    }
    payload.update(overrides)
    return Issue.model_validate(payload)


def _count_only_issue() -> Issue:
    return _issue(comment_count=2, comments_omitted=True)


def _blocked(issue: Issue) -> BlockedIssue:
    return BlockedIssue.model_validate(
        {**issue.model_dump(), "blocked_by_count": 1, "blocked_by": ["bd-9"]}
    )


@pytest.mark.asyncio
async def test_brief_show_does_not_request_bodies_even_when_asked(monkeypatch):
    """brief wins over include_comments: no hydration, exactly four fields."""
    mock_show = AsyncMock(return_value=_count_only_issue())
    monkeypatch.setattr(server, "beads_show_issue", mock_show)

    result = await server.show_issue(issue_id="bd-1", brief=True, include_comments=True)

    mock_show.assert_called_once_with(issue_id="bd-1", include_comments=False)
    assert set(result.model_dump()) == BRIEF_KEYS


@pytest.mark.asyncio
async def test_brief_show_with_comments_fields_stays_brief(monkeypatch):
    """brief also wins over a field selection naming comments."""
    mock_show = AsyncMock(return_value=_count_only_issue())
    monkeypatch.setattr(server, "beads_show_issue", mock_show)

    result = await server.show_issue(issue_id="bd-1", brief=True, fields=["comments"])

    mock_show.assert_called_once_with(issue_id="bd-1", include_comments=False)
    assert set(result.model_dump()) == BRIEF_KEYS


@pytest.mark.asyncio
async def test_brief_list_and_ready_shapes(monkeypatch):
    """Brief collection rows keep exactly the four identification fields."""
    issues = [_count_only_issue(), _count_only_issue()]
    monkeypatch.setattr(server, "beads_list_issues", AsyncMock(return_value=issues))
    monkeypatch.setattr(server, "beads_ready_work", AsyncMock(return_value=issues))

    listed = await server.list_issues(brief=True)
    ready = await server.ready_work(brief=True)

    for row in [*listed, *ready]:
        assert set(row.model_dump()) == BRIEF_KEYS


@pytest.mark.asyncio
async def test_brief_blocked_shape(monkeypatch):
    """Brief blocked rows keep exactly the four identification fields."""
    blocked = [_blocked(_count_only_issue())]
    monkeypatch.setattr(server, "beads_blocked", AsyncMock(return_value=blocked))

    rows = await server.blocked(brief=True)

    assert len(rows) == 1
    assert set(rows[0].model_dump()) == BRIEF_KEYS


@pytest.mark.asyncio
async def test_brief_deps_retains_issue_comment_metadata(monkeypatch):
    """brief_deps compacts dependencies, not the issue: comment metadata and
    any hydrated bodies stay on the returned full-issue dict."""
    issue = _issue(
        comment_count=2,
        comments_omitted=False,
        comments=[_body(1), _body(2)],
        dependencies=[
            LinkedIssue.model_validate(
                {
                    "id": "bd-9",
                    "title": "dep",
                    "status": "open",
                    "priority": 2,
                    "issue_type": "task",
                    "created_at": "2026-01-01T00:00:00Z",
                    "updated_at": "2026-01-01T00:00:00Z",
                    "dependency_type": "blocks",
                }
            ).model_dump()
        ],
    )
    mock_show = AsyncMock(return_value=issue)
    monkeypatch.setattr(server, "beads_show_issue", mock_show)

    result = await server.show_issue(issue_id="bd-1", brief_deps=True, include_comments=True)

    mock_show.assert_called_once_with(issue_id="bd-1", include_comments=True)
    assert result["comment_count"] == 2
    assert result["comments_omitted"] is False
    assert [c["text"] for c in result["comments"]] == ["comment 1", "comment 2"]
    # The dependency itself is the compact shape.
    assert set(result["dependencies"][0]) == {
        "id",
        "title",
        "status",
        "priority",
        "dependency_type",
    }


@pytest.mark.asyncio
async def test_brief_mutation_operation_result_shapes_unchanged(monkeypatch):
    """Brief create/claim/update/close keep the exact OperationResult shape."""
    monkeypatch.setattr(
        server, "beads_create_issue", AsyncMock(return_value=_count_only_issue())
    )
    monkeypatch.setattr(
        server, "beads_claim_issue", AsyncMock(return_value=_count_only_issue())
    )
    monkeypatch.setattr(
        server, "beads_update_issue", AsyncMock(return_value=_count_only_issue())
    )
    monkeypatch.setattr(
        server, "beads_close_issue", AsyncMock(return_value=[_count_only_issue()])
    )

    created = await server.create_issue(title="t")
    claimed = await server.claim_issue(issue_id="bd-1")
    updated = await server.update_issue(issue_id="bd-1", status="in_progress")
    closed = await server.close_issue(issue_id="bd-1", reason="done")

    # The exact pre-existing shape: message is always present (None when unset).
    assert isinstance(created, OperationResult)
    assert isinstance(claimed, OperationResult)
    assert isinstance(updated, OperationResult)
    assert set(created.model_dump()) == {"id", "action", "message"}
    assert set(claimed.model_dump()) == {"id", "action", "message"}
    assert set(updated.model_dump()) == {"id", "action", "message"}
    assert all(set(c.model_dump()) == {"id", "action", "message"} for c in closed)
    assert closed[0].message == "done"
