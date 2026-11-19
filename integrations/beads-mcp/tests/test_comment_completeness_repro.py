"""Repro for bd-kwe4ze: MCP models must keep the CLI's comment completeness fields."""

from beads_mcp.models import Issue


def test_issue_model_preserves_comment_completeness() -> None:
    cli_json = {
        "id": "bd-1",
        "title": "t",
        "status": "open",
        "priority": 2,
        "issue_type": "task",
        "created_at": "2026-01-01T00:00:00Z",
        "updated_at": "2026-01-01T00:00:00Z",
        "comment_count": 3,
        "comments_omitted": True,
    }
    dumped = Issue.model_validate(cli_json).model_dump()
    assert dumped.get("comment_count") == 3
    assert dumped.get("comments_omitted") is True
