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
