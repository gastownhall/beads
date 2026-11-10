"""Comment completeness across MCP reads (bd-kwe4ze).

The bd CLI reports how complete an issue's comment payload is: comment_count,
comments_omitted, and, when asked with --include-comments, the comment bodies.
The MCP layer must keep those signals so a read never looks complete when its
comments were left out.
"""

import json
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from beads_mcp import server
from beads_mcp.bd_client import BdClient
from beads_mcp.models import ShowIssueParams

BASE_ISSUE: dict[str, Any] = {
    "id": "bd-1",
    "title": "Comment completeness",
    "description": "body",
    "status": "open",
    "priority": 2,
    "issue_type": "task",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:00:00Z",
}

TWO_COMMENTS: list[dict[str, Any]] = [
    {
        "id": "c-first",
        "issue_id": "bd-1",
        "author": "alice",
        "text": "first comment",
        "created_at": "2026-01-01T01:00:00Z",
    },
    {
        "id": "c-second",
        "issue_id": "bd-1",
        "author": "bob",
        "text": "second comment",
        "created_at": "2026-01-01T02:00:00Z",
    },
]


def _issue(**extra: Any) -> dict[str, Any]:
    return {**BASE_ISSUE, **extra}


class _Recorder:
    """Runs a real BdClient against canned CLI stdout and records every argv."""

    def __init__(self, payload: Any) -> None:
        self.payload = payload
        self.calls: list[tuple[str, ...]] = []
        self.client = BdClient(bd_path="/usr/bin/bd", beads_db="/tmp/test.db")

    async def _exec(self, *argv: str, **_kwargs: Any) -> MagicMock:
        self.calls.append(argv)
        process = MagicMock()
        process.returncode = 0
        process.communicate = AsyncMock(return_value=(json.dumps(self.payload).encode(), b""))
        return process

    def patched(self) -> Any:
        return patch("asyncio.create_subprocess_exec", side_effect=self._exec)

    def tools_patched(self) -> Any:
        return patch("beads_mcp.tools._get_client", AsyncMock(return_value=self.client))


# AC-1: default show keeps the CLI's count-only completeness signals.


@pytest.mark.asyncio
@pytest.mark.parametrize("envelope", ["array", "object"])
async def test_client_show_preserves_count_only_metadata(envelope: str) -> None:
    issue = _issue(comment_count=2, comments_omitted=True)
    rec = _Recorder([issue] if envelope == "array" else issue)

    with rec.patched():
        result = await rec.client.show(ShowIssueParams(issue_id="bd-1"))

    assert result.comment_count == 2
    assert result.comments_omitted is True
    assert result.comments is None
    assert len(rec.calls) == 1
    assert "--include-comments" not in rec.calls[0]


@pytest.mark.asyncio
async def test_mcp_default_show_returns_count_only_metadata() -> None:
    rec = _Recorder([_issue(comment_count=2, comments_omitted=True)])

    with rec.patched(), rec.tools_patched():
        result = await server.show_issue(issue_id="bd-1")

    dumped = result.model_dump() if hasattr(result, "model_dump") else result
    assert dumped["comment_count"] == 2
    assert dumped["comments_omitted"] is True
    assert dumped["comments"] is None
    assert len(rec.calls) == 1
    assert "--include-comments" not in rec.calls[0]


# AC-3: the Issue model derives missing completeness from supplied bodies and
# keeps unknowns unknown.


@pytest.mark.parametrize(
    ("extra", "count", "omitted"),
    [
        pytest.param({}, None, None, id="no-information"),
        pytest.param({"comments": TWO_COMMENTS}, 2, False, id="bodies-without-count"),
        pytest.param({"comments": []}, 0, False, id="empty-array"),
        pytest.param({"comment_count": 0}, 0, False, id="zero-count"),
        pytest.param({"comment_count": 3}, 3, True, id="ready-style-count-without-marker"),
        pytest.param({"comment_count": 3, "comments": TWO_COMMENTS[:1]}, 3, True, id="fewer-bodies-than-count"),
        pytest.param({"comment_count": 1, "comments": TWO_COMMENTS}, 1, False, id="more-bodies-than-count"),
        pytest.param({"comment_count": 0, "comments_omitted": True}, 0, True, id="explicit-true-kept"),
        pytest.param({"comment_count": 5, "comments_omitted": False}, 5, False, id="explicit-false-kept"),
        pytest.param({"comments_omitted": True}, None, True, id="marker-without-count"),
    ],
)
@pytest.mark.parametrize("model_name", ["Issue", "BlockedIssue"])
def test_model_derives_comment_completeness(
    model_name: str, extra: dict[str, Any], count: int | None, omitted: bool | None
) -> None:
    from beads_mcp import models

    payload = _issue(**extra)
    if model_name == "BlockedIssue":
        payload.update(blocked_by_count=1, blocked_by=["bd-2"])
    issue = getattr(models, model_name).model_validate(payload)

    assert issue.comment_count == count
    assert issue.comments_omitted is omitted
    if "comments" in extra:
        assert [c.text for c in issue.comments] == [c["text"] for c in extra["comments"]]
    else:
        assert issue.comments is None

    # Revalidation (model_dump round trips, description truncation) is idempotent.
    again = type(issue).model_validate(issue.model_dump())
    assert again == issue
    assert server._truncate_description(issue, 2).comment_count == count
    assert server._truncate_description(issue, 2).comments_omitted is omitted


@pytest.mark.asyncio
async def test_blocked_without_metadata_stays_unknown() -> None:
    rec = _Recorder([_issue(blocked_by_count=1, blocked_by=["bd-2"])])

    with rec.patched(), rec.tools_patched():
        result = await server.blocked()

    assert len(rec.calls) == 1
    assert result[0].comment_count is None
    assert result[0].comments_omitted is None
    assert result[0].comments is None


# AC-5: field projections admit the comment fields; selecting comments also
# reports whether bodies were omitted, and collections never fetch extra data.


async def _collection(tool: str, rec: _Recorder, **kwargs: Any) -> Any:
    with rec.patched(), rec.tools_patched():
        if tool == "list":
            return await server.list_issues(**kwargs)
        return await server.ready_work(**kwargs)


@pytest.mark.asyncio
@pytest.mark.parametrize("tool", ["list", "ready"])
async def test_collection_fields_accept_comment_names(tool: str) -> None:
    rec = _Recorder([_issue(comment_count=2, comments_omitted=True), _issue(id="bd-2")])

    rows = await _collection(tool, rec, fields=["id", "comment_count", "comments_omitted"])

    assert rows == [
        {"id": "bd-1", "comment_count": 2, "comments_omitted": True},
        {"id": "bd-2", "comment_count": None, "comments_omitted": None},
    ]
    assert len(rec.calls) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize("tool", ["list", "ready"])
async def test_collection_comments_selection_carries_omission(tool: str) -> None:
    rec = _Recorder([_issue(comment_count=2), _issue(id="bd-2"), _issue(id="bd-3", comment_count=0)])

    rows = await _collection(tool, rec, fields=["id", "comments"])

    assert rows == [
        {"id": "bd-1", "comments": None, "comments_omitted": True},
        {"id": "bd-2", "comments": None, "comments_omitted": None},
        {"id": "bd-3", "comments": None, "comments_omitted": False},
    ]
    # Collections use only their original CLI response: no hydration reads.
    assert len(rec.calls) == 1
    assert "--include-comments" not in rec.calls[0]


@pytest.mark.asyncio
async def test_show_fields_accept_count_and_marker() -> None:
    rec = _Recorder([_issue(comment_count=2, comments_omitted=True)])

    with rec.patched(), rec.tools_patched():
        result = await server.show_issue(issue_id="bd-1", fields=["comment_count", "comments_omitted"])

    assert result == {"comment_count": 2, "comments_omitted": True}
    assert len(rec.calls) == 1
    assert "--include-comments" not in rec.calls[0]


@pytest.mark.asyncio
async def test_non_comment_projection_and_invalid_fields_unchanged() -> None:
    rec = _Recorder([_issue(comment_count=2, comments_omitted=True)])

    rows = await _collection("list", rec, fields=["id", "title"])
    assert rows == [{"id": "bd-1", "title": "Comment completeness"}]

    with pytest.raises(ValueError, match="Invalid field"):
        await _collection("list", rec, fields=["id", "comment_bodies"])
