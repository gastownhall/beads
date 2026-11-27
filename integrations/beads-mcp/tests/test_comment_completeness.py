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

# bd before v1.0 sent integer comment ids, and bd before v1.0.5 streams the
# bodies on every show.
PRE_1_0_COMMENTS: list[dict[str, Any]] = [{**c, "id": n} for n, c in enumerate(TWO_COMMENTS, start=7)]


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
        pytest.param({"comments": PRE_1_0_COMMENTS}, 2, False, id="bodies-without-count-integer-ids"),
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
async def test_default_show_accepts_pre_1_0_integer_comment_ids() -> None:
    rec = _Recorder([_issue(comments=PRE_1_0_COMMENTS)])

    with rec.patched(), rec.tools_patched():
        issue = await server.show_issue(issue_id="bd-1")

    assert [(c.id, c.text) for c in issue.comments] == [("7", "first comment"), ("8", "second comment")]
    assert issue.comment_count == 2
    assert issue.comments_omitted is False
    assert "--include-comments" not in rec.calls[0]


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


# AC-2: non-brief show hydrates bodies on request, in one show command.

HYDRATED = _issue(comments=TWO_COMMENTS, comment_count=2)


def _assert_one_hydrating_show(rec: _Recorder) -> None:
    assert len(rec.calls) == 1
    argv = rec.calls[0]
    assert argv[1] == "show"
    assert "--include-comments" in argv
    # The flag goes before the operand separator, the issue id after it.
    assert argv.index("--include-comments") < argv.index("--") < argv.index("bd-1")
    assert argv[-1] == "bd-1"


@pytest.mark.asyncio
async def test_client_show_include_comments_passes_flag_and_keeps_order() -> None:
    rec = _Recorder([HYDRATED])

    with rec.patched():
        issue = await rec.client.show(ShowIssueParams(issue_id="bd-1", include_comments=True))

    _assert_one_hydrating_show(rec)
    assert [(c.id, c.text) for c in issue.comments] == [
        ("c-first", "first comment"),
        ("c-second", "second comment"),
    ]
    assert issue.comment_count == 2
    assert issue.comments_omitted is False


@pytest.mark.asyncio
async def test_tools_show_forwards_include_comments() -> None:
    from beads_mcp.tools import beads_show_issue

    rec = _Recorder([HYDRATED])
    with rec.patched(), rec.tools_patched():
        issue = await beads_show_issue(issue_id="bd-1", include_comments=True)

    _assert_one_hydrating_show(rec)
    assert issue.comment_count == 2


@pytest.mark.asyncio
async def test_mcp_show_include_comments_returns_full_chronology() -> None:
    rec = _Recorder([HYDRATED])

    with rec.patched(), rec.tools_patched():
        issue = await server.show_issue(issue_id="bd-1", include_comments=True)

    _assert_one_hydrating_show(rec)
    assert [c.text for c in issue.comments] == ["first comment", "second comment"]
    assert issue.comment_count == 2
    assert issue.comments_omitted is False


@pytest.mark.asyncio
async def test_mcp_show_comments_field_hydrates_projection() -> None:
    rec = _Recorder([HYDRATED])

    with rec.patched(), rec.tools_patched():
        result = await server.show_issue(issue_id="bd-1", fields=["comments"])

    _assert_one_hydrating_show(rec)
    assert set(result) == {"comments", "comments_omitted"}
    assert [c["text"] for c in result["comments"]] == ["first comment", "second comment"]
    assert result["comments_omitted"] is False


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("kwargs", "omitted"),
    [
        pytest.param(
            {"include_comments": True, "fields": ["id", "comment_count", "comments_omitted"]},
            True,
            id="bodies-projected-away",
        ),
        pytest.param({"fields": ["comments", "comments_omitted"]}, False, id="bodies-kept"),
    ],
)
async def test_show_projection_marker_describes_its_own_bodies(kwargs: dict[str, Any], omitted: bool) -> None:
    rec = _Recorder([HYDRATED])

    with rec.patched(), rec.tools_patched():
        result = await server.show_issue(issue_id="bd-1", **kwargs)

    _assert_one_hydrating_show(rec)
    assert result["comments_omitted"] is omitted
    assert ("comments" in result) is not omitted


# bd drops an empty comments list from its JSON, so only a hydrated read can
# tell an issue with no comments from one whose bodies were not fetched.


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("include_comments", "bodies"),
    [pytest.param(True, [], id="hydrated"), pytest.param(False, None, id="count-only")],
)
async def test_client_show_zero_comments(include_comments: bool, bodies: list[Any] | None) -> None:
    rec = _Recorder([_issue(comment_count=0)])

    with rec.patched():
        issue = await rec.client.show(ShowIssueParams(issue_id="bd-1", include_comments=include_comments))

    assert issue.comments == bodies
    assert issue.comment_count == 0
    assert issue.comments_omitted is False


@pytest.mark.asyncio
async def test_mcp_show_comments_field_zero_comments() -> None:
    rec = _Recorder([_issue(comment_count=0)])

    with rec.patched(), rec.tools_patched():
        result = await server.show_issue(issue_id="bd-1", fields=["comments"])

    _assert_one_hydrating_show(rec)
    assert result == {"comments": [], "comments_omitted": False}


@pytest.mark.asyncio
async def test_mcp_show_include_comments_propagates_cli_errors() -> None:
    from beads_mcp.bd_client import BdCommandError

    rec = _Recorder([])

    async def failing_exec(*argv: str, **_kwargs: Any) -> MagicMock:
        rec.calls.append(argv)
        process = MagicMock()
        process.returncode = 1
        process.communicate = AsyncMock(return_value=(b"", b"Error: issue not found"))
        return process

    with (
        patch("asyncio.create_subprocess_exec", side_effect=failing_exec),
        rec.tools_patched(),
        pytest.raises(BdCommandError),
    ):
        await server.show_issue(issue_id="bd-1", include_comments=True)

    _assert_one_hydrating_show(rec)


# AC-6: brief reads keep exactly {id, title, status, priority} and never hydrate.

BRIEF_KEYS = {"id", "title", "status", "priority"}


def _keys(obj: Any) -> set[str]:
    return set(obj.model_dump() if hasattr(obj, "model_dump") else obj)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "kwargs",
    [
        pytest.param({}, id="plain"),
        pytest.param({"include_comments": True}, id="include_comments"),
        pytest.param({"fields": ["comments"]}, id="fields-comments"),
        pytest.param({"include_comments": True, "fields": ["comments"]}, id="both"),
    ],
)
async def test_brief_show_is_exact_and_never_hydrates(kwargs: dict[str, Any]) -> None:
    rec = _Recorder([_issue(comment_count=2, comments_omitted=True)])

    with rec.patched(), rec.tools_patched():
        result = await server.show_issue(issue_id="bd-1", brief=True, **kwargs)

    assert _keys(result) == BRIEF_KEYS
    assert len(rec.calls) == 1
    assert "--include-comments" not in rec.calls[0]


@pytest.mark.asyncio
@pytest.mark.parametrize("tool", ["list", "ready", "blocked"])
async def test_brief_collections_are_exact(tool: str) -> None:
    row = _issue(comment_count=2, comments_omitted=True, comments=TWO_COMMENTS)
    if tool == "blocked":
        row.update(blocked_by_count=1, blocked_by=["bd-2"])
    rec = _Recorder([row])

    with rec.patched(), rec.tools_patched():
        if tool == "list":
            rows = await server.list_issues(brief=True)
        elif tool == "ready":
            rows = await server.ready_work(brief=True)
        else:
            rows = await server.blocked(brief=True)

    assert [_keys(r) for r in rows] == [BRIEF_KEYS]
    assert len(rec.calls) == 1


@pytest.mark.asyncio
async def test_brief_deps_keeps_issue_comment_metadata() -> None:
    rec = _Recorder([_issue(comment_count=2, comments_omitted=True)])

    with rec.patched(), rec.tools_patched():
        data = await server.show_issue(issue_id="bd-1", brief_deps=True)

    assert data["comment_count"] == 2
    assert data["comments_omitted"] is True
    assert "--include-comments" not in rec.calls[0]


@pytest.mark.asyncio
async def test_brief_mutation_result_shape_unchanged() -> None:
    rec = _Recorder(_issue(comment_count=2, comments_omitted=True))

    with rec.patched(), rec.tools_patched():
        result = await server.claim_issue(issue_id="bd-1")

    assert _keys(result) == {"id", "action", "message"}


# AC-4: minimal list/ready rows and compacted previews keep count + omission.


@pytest.mark.asyncio
@pytest.mark.parametrize("tool", ["list", "ready"])
async def test_minimal_rows_carry_comment_completeness(tool: str) -> None:
    rec = _Recorder(
        [
            _issue(id="bd-1", comment_count=2, comments_omitted=True),
            _issue(id="bd-2", comment_count=3),  # ready rows: count without the marker
        ]
    )

    rows = await _collection(tool, rec)

    assert [(r.id, r.comment_count, r.comments_omitted) for r in rows] == [("bd-1", 2, True), ("bd-2", 3, True)]
    for row in rows:
        dumped = row.model_dump()
        assert "comments" not in dumped
        assert "description" not in dumped
    assert len(rec.calls) == 1
    assert "--include-comments" not in rec.calls[0]


@pytest.mark.asyncio
@pytest.mark.parametrize("tool", ["list", "ready"])
async def test_compacted_preview_carries_comment_completeness(tool: str) -> None:
    from beads_mcp.models import CompactedResult

    rows = [_issue(id=f"bd-{i}", comment_count=i + 1) for i in range(server.COMPACTION_THRESHOLD + 5)]
    rec = _Recorder(rows)

    result = await _collection(tool, rec, limit=100)

    assert isinstance(result, CompactedResult)
    assert result.total_count == len(rows)
    assert [(p.comment_count, p.comments_omitted) for p in result.preview] == [
        (i + 1, True) for i in range(server.PREVIEW_COUNT)
    ]
    assert "comments" not in result.preview[0].model_dump()
    assert len(rec.calls) == 1


@pytest.mark.parametrize(
    ("extra", "count", "omitted"),
    [
        pytest.param(
            {"comments": TWO_COMMENTS, "comments_omitted": False}, 2, True, id="removed-bodies-turn-false-true"
        ),
        pytest.param({"comments": TWO_COMMENTS}, 2, True, id="removed-derived-bodies"),
        pytest.param({"comment_count": 2, "comments_omitted": True}, 2, True, id="explicit-true"),
        pytest.param({"comments": [], "comments_omitted": True}, 0, True, id="explicit-true-over-zero"),
        pytest.param({"comments": []}, 0, False, id="known-zero"),
        pytest.param({}, None, None, id="unknown"),
        pytest.param({"comments_omitted": False}, None, False, id="source-false-without-count"),
    ],
)
def test_to_minimal_describes_its_own_omission(
    extra: dict[str, Any], count: int | None, omitted: bool | None
) -> None:
    from beads_mcp.models import Issue

    issue = Issue.model_validate(_issue(**extra))
    before = issue.model_dump()

    minimal = server._to_minimal(issue)

    assert minimal.comment_count == count
    assert minimal.comments_omitted is omitted
    assert "comments" not in minimal.model_dump()
    # The projection adjusts only its own row, never the source Issue.
    assert issue.model_dump() == before


# AC-7: the public show schema and tool catalog expose include_comments.


@pytest.mark.asyncio
async def test_show_schema_exposes_include_comments() -> None:
    from fastmcp.client import Client

    async with Client(server.mcp) as client:
        tools = {tool.name: tool for tool in await client.list_tools()}

    show = tools["show"]
    prop = show.input_schema["properties"]["include_comments"]
    assert prop["type"] == "boolean"
    assert prop["default"] is False
    assert "include_comments" not in show.input_schema.get("required", [])
    for phrase in ("comment_count", "comments_omitted", "include_comments"):
        assert phrase in show.description


@pytest.mark.asyncio
async def test_tool_info_documents_include_comments() -> None:
    info = await server.get_tool_info("show")

    param = info["parameters"]["include_comments"]
    assert param.startswith("bool (default false)")
    text = json.dumps(info)
    for phrase in ("comment_count", "comments_omitted", "fields=['comments']"):
        assert phrase in text


@pytest.mark.asyncio
async def test_get_tool_info_comments_reader_mentions_opt_in() -> None:
    """The comment-reader entry points at the opt-in and stays the body reader."""
    info = await server.get_tool_info("comments")
    blob = " ".join(str(v) for v in info.values()).lower()
    assert "include_comments" in blob or "count-only" in blob
    assert "but not the bodies" not in blob
