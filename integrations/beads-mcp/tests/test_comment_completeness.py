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
