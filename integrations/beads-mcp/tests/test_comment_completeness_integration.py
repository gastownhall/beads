"""Real CLI/FastMCP regression for comment completeness (bd-kwe4ze).

End-to-end coverage against a real bd binary (BEADS_TEST_BD_BINARY, else the
bd on PATH): default show is count-only with an honest omission marker,
include_comments=True (or a comments field selection) hydrates the full
chronology in CLI order, and list/ready rows carry their completeness
metadata. Every test runs in an isolated temporary workspace.
"""

import asyncio
import json
import os
import shutil
import tempfile

import pytest
from fastmcp.client import Client

from beads_mcp.server import mcp


@pytest.fixture(scope="session")
def bd_executable():
    """Verify bd is available: the candidate binary, else one on PATH."""
    bd_path = os.environ.get("BEADS_TEST_BD_BINARY") or shutil.which("bd")
    if not bd_path:
        pytest.fail(
            "bd executable not found. Set BEADS_TEST_BD_BINARY or add bd to PATH."
        )
    return bd_path


@pytest.fixture
async def temp_db(bd_executable):
    """Create a temporary workspace and initialize bd in it - fully hermetic."""
    temp_dir = tempfile.mkdtemp(prefix="beads_mcp_comments_", dir="/tmp")

    env = os.environ.copy()
    env.pop("BEADS_DB", None)
    env.pop("BEADS_DIR", None)
    env.pop("BEADS_WORKING_DIR", None)

    process = await asyncio.create_subprocess_exec(
        bd_executable,
        "init",
        "--prefix",
        "test",
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=env,
        cwd=temp_dir,
    )
    _stdout, stderr = await process.communicate()

    if process.returncode != 0:
        pytest.fail(f"Failed to initialize test database: {stderr.decode()}")

    beads_dir = os.path.join(temp_dir, ".beads")
    yield beads_dir

    shutil.rmtree(temp_dir, ignore_errors=True)


@pytest.fixture
async def mcp_client(bd_executable, temp_db, monkeypatch):
    """FastMCP client pointed at the temporary workspace."""
    from beads_mcp import tools

    tools._connection_pool.clear()
    for var in ("BEADS_CONTEXT_SET", "BEADS_WORKING_DIR", "BEADS_DB", "BEADS_DIR"):
        monkeypatch.delenv(var, raising=False)
    # The server's client resolves its bd via BEADS_PATH; pin it to the same
    # binary temp_db used for init, or an older PATH bd cannot open the
    # database the newer one created.
    monkeypatch.setenv("BEADS_PATH", bd_executable)

    workspace_root = os.path.dirname(temp_db)

    async with Client(mcp) as client:
        await client.call_tool("context", {"workspace_root": workspace_root})
        yield client

    tools._connection_pool.clear()


async def _create_issue_with_two_comments(mcp_client) -> str:
    """One issue, two comments — the fixture scenario from the design."""
    created = await mcp_client.call_tool(
        "create", {"title": "Commented issue", "priority": 2, "issue_type": "task", "brief": True}
    )
    issue_id = json.loads(created.content[0].text)["id"]
    await mcp_client.call_tool("comment", {"issue_id": issue_id, "text": "first"})
    await mcp_client.call_tool("comment", {"issue_id": issue_id, "text": "second"})
    return issue_id


def _payload(result) -> dict:
    return json.loads(result.content[0].text)


@pytest.mark.asyncio
async def test_default_show_is_count_only_with_omission_marker(mcp_client):
    """Default show: count 2, comments_omitted true, no bodies (AC-1)."""
    issue_id = await _create_issue_with_two_comments(mcp_client)

    shown = _payload(await mcp_client.call_tool("show", {"issue_id": issue_id}))

    assert shown["comment_count"] == 2
    assert shown["comments_omitted"] is True
    assert shown["comments"] is None


@pytest.mark.asyncio
async def test_include_comments_hydrates_full_chronology(mcp_client):
    """include_comments=True: both bodies in CLI order, omission false (AC-2)."""
    issue_id = await _create_issue_with_two_comments(mcp_client)

    shown = _payload(
        await mcp_client.call_tool("show", {"issue_id": issue_id, "include_comments": True})
    )

    assert [c["text"] for c in shown["comments"]] == ["first", "second"]
    assert shown["comment_count"] == 2
    assert shown["comments_omitted"] is False


@pytest.mark.asyncio
async def test_fields_comments_selection_hydrates(mcp_client):
    """fields=['comments'] selects the bodies plus the omission companion (AC-5)."""
    issue_id = await _create_issue_with_two_comments(mcp_client)

    shown = _payload(
        await mcp_client.call_tool("show", {"issue_id": issue_id, "fields": ["comments"]})
    )

    assert [c["text"] for c in shown["comments"]] == ["first", "second"]
    assert shown["comments_omitted"] is False


@pytest.mark.asyncio
async def test_list_rows_carry_completeness_metadata(mcp_client):
    """List rows keep count/omission; a commented row says true, a bare row false."""
    issue_id = await _create_issue_with_two_comments(mcp_client)
    await mcp_client.call_tool(
        "create", {"title": "Bare issue", "priority": 2, "issue_type": "task", "brief": True}
    )

    listed = _payload(await mcp_client.call_tool("list", {"status": "open"}))
    by_count = {row["comment_count"]: row for row in listed}

    assert by_count[2]["id"] == issue_id
    assert by_count[2]["comments_omitted"] is True
    assert by_count[0]["comments_omitted"] is False


@pytest.mark.asyncio
async def test_ready_rows_carry_completeness_metadata(mcp_client):
    """Ready rows (CLI never sets the marker there) still describe omission."""
    issue_id = await _create_issue_with_two_comments(mcp_client)

    ready = _payload(await mcp_client.call_tool("ready", {"limit": 10}))
    row = next(r for r in ready if r["id"] == issue_id)

    assert row["comment_count"] == 2
    assert row["comments_omitted"] is True


@pytest.mark.asyncio
async def test_comments_tool_reads_bodies(mcp_client):
    """The dedicated reader still returns the full chronology."""
    issue_id = await _create_issue_with_two_comments(mcp_client)

    comments = _payload(await mcp_client.call_tool("comments", {"issue_id": issue_id}))

    assert [c["text"] for c in comments] == ["first", "second"]
