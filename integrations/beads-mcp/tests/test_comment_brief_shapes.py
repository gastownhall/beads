"""Brief read shapes stay intact around comment completeness (bd-kwe4ze).

AC-6: brief_deps keeps the full Issue's comment metadata and hydrated bodies
(a brief dependency is compact, not incomplete), and brief mutation
OperationResults keep their exact shapes. The brief show/list/ready/blocked
shapes are covered in test_comment_completeness.py.
"""

from unittest.mock import AsyncMock

import pytest

from beads_mcp import server
from beads_mcp.models import Issue, LinkedIssue, OperationResult


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
    monkeypatch.setattr(server, "beads_create_issue", AsyncMock(return_value=_count_only_issue()))
    monkeypatch.setattr(server, "beads_claim_issue", AsyncMock(return_value=_count_only_issue()))
    monkeypatch.setattr(server, "beads_update_issue", AsyncMock(return_value=_count_only_issue()))
    monkeypatch.setattr(server, "beads_close_issue", AsyncMock(return_value=[_count_only_issue()]))

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
