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

from beads_mcp import server
from beads_mcp.bd_client import BdCliClient
from beads_mcp.models import BlockedIssue, Issue, ShowIssueParams


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


# =============================================================================
# AC-3: validation derives count/omission from bodies, keeps unknowns unknown
# =============================================================================


def _body(i: int) -> dict:
    return {
        "id": f"c{i}",
        "issue_id": "bd-1",
        "author": "a",
        "text": f"comment {i}",
        "created_at": "2026-01-01T00:00:00Z",
    }


def _issue(**overrides) -> dict:
    """Valid CLI issue JSON; keyword arguments add or replace fields."""
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
    return payload


def test_no_comment_information_stays_unknown():
    """Neither count nor bodies: unknown, never fabricated zero/false."""
    issue = Issue.model_validate(_issue())
    assert issue.comment_count is None
    assert issue.comments_omitted is None
    assert issue.comments is None


def test_zero_count_without_bodies_is_not_omitted():
    """A known zero count means no comments exist — nothing was withheld."""
    issue = Issue.model_validate(_issue(comment_count=0))
    assert issue.comment_count == 0
    assert issue.comments_omitted is False


def test_empty_bodies_array_derives_zero():
    """[] is supplied-empty (not unavailable): count 0, omission false."""
    issue = Issue.model_validate(_issue(comments=[]))
    assert issue.comment_count == 0
    assert issue.comments_omitted is False


def test_bodies_without_count_derive_count_and_omission():
    """Older CLIs omit comment_count; the array length is the count."""
    issue = Issue.model_validate(_issue(comments=[_body(1), _body(2)]))
    assert issue.comment_count == 2
    assert issue.comments_omitted is False


def test_ready_style_count_without_marker_derives_omission():
    """Reader.Ready rows carry a count but never the marker (be-ozp): a
    positive count with no bodies still means content was withheld."""
    issue = Issue.model_validate(_issue(comment_count=3))
    assert issue.comment_count == 3
    assert issue.comments_omitted is True


def test_fewer_bodies_than_count_is_omitted():
    """A partial chronology describes itself as partial."""
    issue = Issue.model_validate(_issue(comment_count=5, comments=[_body(1)]))
    assert issue.comment_count == 5
    assert issue.comments_omitted is True


def test_more_bodies_than_count_derives_not_omitted():
    """Inconsistent CLI data is reported as it arrives, not repaired."""
    issue = Issue.model_validate(_issue(comment_count=1, comments=[_body(1), _body(2)]))
    assert issue.comment_count == 1
    assert issue.comments_omitted is False


def test_explicit_omission_is_preserved_even_when_inconsistent():
    """Explicit markers win over derivation, including inconsistent ones."""
    full = Issue.model_validate(
        _issue(comment_count=2, comments_omitted=True, comments=[_body(1), _body(2)])
    )
    assert full.comments_omitted is True

    zero = Issue.model_validate(_issue(comment_count=0, comments_omitted=True))
    assert zero.comments_omitted is True

    kept_false = Issue.model_validate(_issue(comment_count=2, comments_omitted=False))
    assert kept_false.comments_omitted is False


def test_revalidation_is_idempotent():
    """model_dump -> validate again reaches the same values (no drift)."""
    first = Issue.model_validate(_issue(comments=[_body(1), _body(2)]))
    second = Issue.model_validate(first.model_dump())
    third = Issue.model_validate(second.model_dump())
    assert first.comment_count == second.comment_count == third.comment_count == 2
    assert first.comments_omitted is second.comments_omitted is third.comments_omitted is False
    assert first.comments == second.comments == third.comments


def test_truncate_description_keeps_derived_metadata():
    """server._truncate_description rebuilds via model_dump; derived comment
    metadata must survive that round-trip unchanged (idempotent validator)."""
    long_text = "x" * 50
    issue = Issue.model_validate(_issue(description=long_text, comments=[_body(1), _body(2)]))
    truncated = server._truncate_description(issue, 10)
    assert truncated.comment_count == 2
    assert truncated.comments_omitted is False
    assert truncated.description == "x" * 10 + "..."


def test_blocked_issue_without_metadata_stays_unknown():
    """Blocked rows carry no comment count (types.BlockedIssue): absence
    stays unknown — the validator adds no lookups and no guesses."""
    blocked = BlockedIssue.model_validate(
        {
            **_issue(),
            "blocked_by_count": 1,
            "blocked_by": ["bd-9"],
        }
    )
    assert blocked.comment_count is None
    assert blocked.comments_omitted is None
    assert blocked.comments is None


def test_blocked_issue_derives_from_bodies_like_issue():
    """BlockedIssue inherits the derivation: a supplied array is counted."""
    blocked = BlockedIssue.model_validate(
        {
            **_issue(comments=[_body(1)]),
            "blocked_by_count": 0,
            "blocked_by": [],
        }
    )
    assert blocked.comment_count == 1
    assert blocked.comments_omitted is False


# =============================================================================
# AC-4: minimal projections carry completeness; a row that drops bodies says so
# =============================================================================


def _list_row(i: int, with_marker: bool = True) -> dict:
    """A current count-only list/ready row (types.IssueWithCounts)."""
    row = {
        "id": f"bd-{i:04d}",
        "title": f"Issue {i}",
        "status": "open",
        "priority": 2,
        "issue_type": "task",
        "created_at": "2026-01-01T00:00:00Z",
        "updated_at": "2026-01-01T00:00:00Z",
        "dependency_count": 0,
        "dependent_count": 0,
        "comment_count": (i % 3) + 1,  # every row has a positive count here
    }
    if with_marker:
        row["comments_omitted"] = True
    return row


def test_to_minimal_passes_through_count_only_row():
    """List-row metadata survives the minimal projection untouched."""
    issue = Issue.model_validate(_list_row(1))
    minimal = server._to_minimal(issue)
    assert minimal.comment_count == 2
    assert minimal.comments_omitted is True


def test_to_minimal_ready_row_unspecified_marker_is_true():
    """Ready rows never carry the marker (be-ozp): a positive count alone
    still means the row withheld comment content."""
    issue = Issue.model_validate(_list_row(2, with_marker=False))
    minimal = server._to_minimal(issue)
    assert minimal.comment_count == 3
    assert minimal.comments_omitted is True


def test_to_minimal_known_zero_is_not_omitted():
    """A zero count with no bodies is a true empty, not a withheld one."""
    issue = Issue.model_validate({**_list_row(0), "comment_count": 0, "comments_omitted": False})
    minimal = server._to_minimal(issue)
    assert minimal.comment_count == 0
    assert minimal.comments_omitted is False


def test_to_minimal_unknown_stays_unknown():
    """No count, no bodies, no marker: the minimal row keeps the unknown."""
    issue = Issue.model_validate(_issue())
    minimal = server._to_minimal(issue)
    assert minimal.comment_count is None
    assert minimal.comments_omitted is None


def test_to_minimal_hydrated_source_marks_its_own_omission():
    """Invariant: a minimal row that removes supplied bodies must say so.

    Only show hydrates today, so a hydrated source reaching _to_minimal is a
    forward-looking path — but the projection must still describe itself.
    """
    issue = Issue.model_validate(
        _issue(comment_count=2, comments_omitted=False, comments=[_body(1), _body(2)])
    )
    minimal = server._to_minimal(issue)
    assert minimal.comment_count == 2
    assert minimal.comments_omitted is True
    # The projection does not rewrite its source.
    assert issue.comments_omitted is False
    assert issue.comments is not None and len(issue.comments) == 2


def test_to_minimal_hydrated_unknown_count_still_marks_omission():
    """Bodies present but no count: removing them is still an omission."""
    issue = Issue.model_validate(_issue(comments=[_body(1)]))
    minimal = server._to_minimal(issue)
    assert minimal.comment_count == 1  # derived by the model validator
    assert minimal.comments_omitted is True


@pytest.mark.asyncio
async def test_list_handler_rows_carry_metadata(monkeypatch):
    """Default list output: every minimal row keeps count and omission."""
    issues = [Issue.model_validate(_list_row(i)) for i in range(3)]
    mock_list = AsyncMock(return_value=issues)
    monkeypatch.setattr(server, "beads_list_issues", mock_list)

    rows = await server.list_issues()

    mock_list.assert_called_once()  # one CLI read; no per-row hydration
    assert all(r.comment_count == (i % 3) + 1 for i, r in enumerate(rows))
    assert all(r.comments_omitted is True for r in rows)


@pytest.mark.asyncio
async def test_ready_handler_rows_carry_metadata(monkeypatch):
    """Ready rows (marker never set by the CLI) still describe omission."""
    issues = [Issue.model_validate(_list_row(i, with_marker=False)) for i in range(3)]
    mock_ready = AsyncMock(return_value=issues)
    monkeypatch.setattr(server, "beads_ready_work", mock_ready)

    rows = await server.ready_work()

    mock_ready.assert_called_once()  # one CLI read; no per-row hydration
    assert all(r.comment_count == (i % 3) + 1 for i, r in enumerate(rows))
    assert all(r.comments_omitted is True for r in rows)


@pytest.mark.asyncio
async def test_compacted_preview_rows_carry_metadata(monkeypatch):
    """Over-threshold results compact; the preview rows keep completeness."""
    issues = [Issue.model_validate(_list_row(i)) for i in range(25)]
    mock_list = AsyncMock(return_value=issues)
    monkeypatch.setattr(server, "beads_list_issues", mock_list)

    result = await server.list_issues()

    assert isinstance(result, server.CompactedResult)
    mock_list.assert_called_once()
    assert result.total_count == 25
    assert result.preview_count == server.PREVIEW_COUNT
    assert len(result.preview) == server.PREVIEW_COUNT
    assert all(r.comment_count is not None for r in result.preview)
    assert all(r.comments_omitted is True for r in result.preview)


# =============================================================================
# AC-5: comment fields are projectable; comments comes with its omission
# =============================================================================


def test_filter_fields_accepts_all_three_comment_names():
    """comment_count, comments_omitted and comments are valid projections."""
    issue = Issue.model_validate(_issue(comment_count=2, comments_omitted=True))
    for field in ("comment_count", "comments_omitted", "comments"):
        projected = server._filter_fields(issue, [field])
        assert field in projected


def test_filter_fields_comments_carries_omission_companion():
    """A comments selection never leaves null bodies reading as 'no discussion':
    the projection adds comments_omitted even when it was not requested."""
    hydrated = Issue.model_validate(
        _issue(comment_count=2, comments_omitted=False, comments=[_body(1), _body(2)])
    )
    projected = server._filter_fields(hydrated, ["comments"])
    assert [c["text"] for c in projected["comments"]] == ["comment 1", "comment 2"]
    assert projected["comments_omitted"] is False
    assert set(projected) == {"comments", "comments_omitted"}

    count_only = Issue.model_validate(_issue(comment_count=2, comments_omitted=True))
    projected = server._filter_fields(count_only, ["comments"])
    assert projected["comments"] is None
    assert projected["comments_omitted"] is True


def test_filter_fields_explicit_omission_selection_not_duplicated():
    """Selecting both names keeps exactly the requested keys."""
    issue = Issue.model_validate(_issue(comment_count=1, comments_omitted=True))
    projected = server._filter_fields(issue, ["comments", "comments_omitted"])
    assert set(projected) == {"comments", "comments_omitted"}


def test_filter_fields_unknown_collection_metadata_passes_through():
    """A surface with no comment metadata projects the unknown as null."""
    issue = Issue.model_validate(_issue())
    projected = server._filter_fields(issue, ["comment_count", "comments_omitted"])
    assert projected["comment_count"] is None
    assert projected["comments_omitted"] is None


def test_filter_fields_invalid_rejection_unchanged():
    """Unknown fields still raise with the valid list (now including comments)."""
    issue = Issue.model_validate(_issue())
    with pytest.raises(ValueError, match="Invalid field"):
        server._filter_fields(issue, ["comment_count", "bogus_field"])


def test_filter_fields_non_comment_projection_unchanged():
    """Projections that name no comment fields keep exactly their keys."""
    issue = Issue.model_validate(_issue(comment_count=2, comments_omitted=True))
    projected = server._filter_fields(issue, ["id", "title"])
    assert set(projected) == {"id", "title"}


@pytest.mark.asyncio
async def test_list_fields_projection_uses_single_cli_read(monkeypatch):
    """Collection field projections use only the original CLI response: one
    read, no per-row hydration, whatever metadata the rows already had."""
    issues = [Issue.model_validate(_list_row(i)) for i in range(3)]
    mock_list = AsyncMock(return_value=issues)
    monkeypatch.setattr(server, "beads_list_issues", mock_list)

    rows = await server.list_issues(fields=["comment_count", "comments_omitted"])

    mock_list.assert_called_once()
    assert [r["comment_count"] for r in rows] == [1, 2, 3]
    assert all(r["comments_omitted"] is True for r in rows)


@pytest.mark.asyncio
async def test_ready_fields_projection_uses_single_cli_read(monkeypatch):
    """Ready rows (marker never set by the CLI) project their count as-is."""
    issues = [Issue.model_validate(_list_row(i, with_marker=False)) for i in range(3)]
    mock_ready = AsyncMock(return_value=issues)
    monkeypatch.setattr(server, "beads_ready_work", mock_ready)

    rows = await server.ready_work(fields=["comment_count"])

    mock_ready.assert_called_once()
    assert [r["comment_count"] for r in rows] == [1, 2, 3]


@pytest.mark.asyncio
async def test_show_fields_projection_hydrated_comments(monkeypatch):
    """show with fields=['comments'] returns the bodies (AC-2 hydrates) and
    the real projection adds the omission companion."""
    mock_show = AsyncMock(
        return_value=Issue.model_validate(
            _issue(comment_count=2, comments_omitted=False, comments=[_body(1), _body(2)])
        )
    )
    monkeypatch.setattr(server, "beads_show_issue", mock_show)

    result = await server.show_issue(issue_id="bd-1", fields=["comments"])

    mock_show.assert_called_once_with(issue_id="bd-1", include_comments=True)
    assert [c["text"] for c in result["comments"]] == ["comment 1", "comment 2"]
    assert result["comments_omitted"] is False
