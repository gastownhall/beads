"""The public show surface documents comment completeness (bd-kwe4ze).

AC-7: clients inspecting the MCP show schema or get_tool_info('show') must
see include_comments exposed with default false, and the descriptions must
explain the count-only defaults (comment_count/comments_omitted, with null
meaning unknown) and the opt-in bodies.
"""

import pytest

from beads_mcp import server


@pytest.mark.asyncio
async def test_show_tool_schema_exposes_include_comments():
    """The public schema carries the flag with its default and a description."""
    tool = await server.mcp.get_tool("show")
    props = tool.parameters["properties"]
    assert "include_comments" in props
    prop = props["include_comments"]
    assert prop["type"] == "boolean"
    assert prop["default"] is False
    desc = (prop.get("description") or "").lower()
    assert "comment" in desc
    assert "bodies" in desc or "chronology" in desc


@pytest.mark.asyncio
async def test_show_tool_description_explains_comment_defaults():
    """The tool description itself explains count/omission and opt-in bodies."""
    tool = await server.mcp.get_tool("show")
    desc = (tool.description or "").lower()
    assert "comment_count" in desc
    assert "comments_omitted" in desc
    assert "include_comments" in desc


@pytest.mark.asyncio
async def test_get_tool_info_show_documents_include_comments():
    """The lazy catalog documents the flag and the count-only default."""
    info = await server.get_tool_info("show")
    assert "include_comments" in info["parameters"]
    param_text = info["parameters"]["include_comments"].lower()
    assert "false" in param_text
    assert "comment" in param_text
    blob = f"{info['description']} {info['returns']}".lower()
    assert "comment_count" in blob
    assert "comments_omitted" in blob


@pytest.mark.asyncio
async def test_get_tool_info_comments_reader_mentions_opt_in():
    """The comment-reader entry points at the opt-in and stays the body reader."""
    info = await server.get_tool_info("comments")
    blob = " ".join(str(v) for v in info.values()).lower()
    assert "include_comments" in blob or "count-only" in blob
