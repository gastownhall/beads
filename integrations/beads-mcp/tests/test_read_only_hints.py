"""Tools that only read declare the MCP readOnlyHint annotation; every other tool does not."""

import pytest
from fastmcp.client import Client

from beads_mcp.server import mcp

READ_ONLY_TOOLS = {
    "discover_tools",
    "get_tool_info",
    "ready",
    "list",
    "show",
    "comments",
    "stats",
    "blocked",
}


@pytest.fixture
async def tools_by_name():
    async with Client(mcp) as client:
        return {tool.name: tool for tool in await client.list_tools()}


async def test_read_only_tools_declare_the_hint(tools_by_name):
    missing = READ_ONLY_TOOLS - tools_by_name.keys()
    assert not missing, f"expected tools are not registered: {sorted(missing)}"
    for name in sorted(READ_ONLY_TOOLS):
        annotations = tools_by_name[name].annotations
        assert annotations is not None, name
        assert annotations.read_only_hint is True, name


async def test_no_other_tool_declares_the_hint(tools_by_name):
    hinted = {
        name
        for name, tool in tools_by_name.items()
        if tool.annotations is not None and tool.annotations.read_only_hint is True
    }
    assert hinted == READ_ONLY_TOOLS


async def test_tools_with_a_writing_action_stay_unannotated(tools_by_name):
    # context can init a beads database and admin can repair or clean one.
    for name in ("context", "admin"):
        annotations = tools_by_name[name].annotations
        assert annotations is None or annotations.read_only_hint is not True, name
