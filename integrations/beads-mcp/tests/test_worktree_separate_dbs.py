"""Real isolated Dolt worktrees, native history sync and passive JSONL export."""

import json
import os
import shutil
import subprocess

import pytest
from fastmcp.client import Client

from beads_mcp import server, tools
from beads_mcp.server import mcp


def _git(cwd, *args):
    return subprocess.run(
        ["git", *args], cwd=cwd, capture_output=True, text=True, check=True, timeout=120
    ).stdout.strip()


def _bd(binary, cwd, *args, env=None):
    return subprocess.run(
        [binary, *args], cwd=cwd, env=env, capture_output=True, text=True, check=True, timeout=120
    ).stdout.strip()


def _create(binary, cwd, title):
    return json.loads(_bd(binary, cwd, "create", f"--title={title}", "--json"))


def _ids(binary, cwd):
    return {issue["id"] for issue in json.loads(_bd(binary, cwd, "list", "--json"))}


@pytest.fixture(scope="session")
def bd_executable():
    binary = shutil.which("bd")
    assert binary is not None, "The canonical package gate must supply its real bd binary"
    return binary


@pytest.fixture
async def git_worktree_with_separate_dbs(bd_executable, tmp_path, monkeypatch, request):
    """Explicit local .beads roots opt each worktree into a separate embedded DB."""
    previous_context_env = {
        key: os.environ.get(key) for key in ("BEADS_WORKING_DIR", "BEADS_CONTEXT_SET", "BEADS_DB")
    }
    for key in list(os.environ):
        if key.startswith(("BEADS_", "BD_", "GIT_")):
            monkeypatch.delenv(key)
    monkeypatch.setenv("BEADS_TEST_MODE", "1")
    monkeypatch.setenv("GIT_CONFIG_GLOBAL", os.devnull)
    monkeypatch.setenv("GIT_CONFIG_NOSYSTEM", "1")
    monkeypatch.setenv("GIT_CONFIG_COUNT", "1")
    monkeypatch.setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
    monkeypatch.setenv("GIT_CONFIG_VALUE_0", "always")
    monkeypatch.setenv("GOMAXPROCS", "1")
    main_repo = tmp_path.resolve() / "main"
    worktree = tmp_path.resolve() / "worktree"
    main_repo.mkdir()
    previous_context = server._workspace_context.copy()
    tools._connection_pool.clear()
    token = tools.current_workspace.set(None)

    def init(root, prefix, remote=None):
        args = ["init", f"--prefix={prefix}", "--non-interactive", "--skip-agents", "--skip-hooks"]
        if remote is not None:
            args.append(f"--remote={remote}")
        env = {**os.environ, "BEADS_DIR": str(root / ".beads")}
        _bd(bd_executable, root, *args, env=env)
        metadata = json.loads((root / ".beads" / "metadata.json").read_text())
        assert (metadata.get("backend") or metadata.get("database")) == "dolt"
        assert metadata["dolt_mode"] == "embedded"

    try:
        _git(main_repo, "init", "--initial-branch=main")
        _git(main_repo, "config", "user.email", "test@example.com")
        _git(main_repo, "config", "user.name", "Test User")
        (main_repo / "README.md").write_text("# Isolated worktree fixture\n")
        _git(main_repo, "add", "README.md")
        _git(main_repo, "commit", "-m", "Fixture source baseline")
        _git(main_repo, "worktree", "add", str(worktree), "-b", "feature")
        init(main_repo, "main")

        # Exercise the old cached fallback before a separate local DB exists.
        if request.node.name == "test_mcp_works_with_separate_databases":
            assert tools._canonicalize_path(str(worktree)) == str(main_repo)

        if getattr(request, "param", None) == "native-sync":
            remote = tmp_path.resolve() / "remote.git"
            _git(main_repo, "init", "--bare", "--initial-branch=main", str(remote))
            remote_url = remote.as_uri()
            _git(main_repo, "push", remote_url, "HEAD:refs/heads/main")
            _bd(bd_executable, main_repo, "dolt", "remote", "add", "origin", remote_url)
            _bd(bd_executable, main_repo, "dolt", "push")
            # Clone common Dolt ancestry; merging independently created databases is not sync.
            init(worktree, "feature", remote_url)
        else:
            init(worktree, "feature")
        assert (main_repo / ".beads").resolve() != (worktree / ".beads").resolve()
        yield main_repo, worktree, tmp_path.resolve()
    finally:
        tools._connection_pool.clear()
        tools.current_workspace.reset(token)
        server._workspace_context.clear()
        server._workspace_context.update(previous_context)
        for key, value in previous_context_env.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
        if worktree.exists():
            _git(main_repo, "worktree", "remove", "--force", str(worktree))


async def test_separate_databases_are_isolated(git_worktree_with_separate_dbs, bd_executable):
    main_repo, worktree, _ = git_worktree_with_separate_dbs
    main_issue = _create(bd_executable, main_repo, "Main repo issue")
    feature_issue = _create(bd_executable, worktree, "Worktree issue")
    assert main_issue["id"].startswith("main-")
    assert feature_issue["id"].startswith("feature-")
    assert _ids(bd_executable, main_repo) == {main_issue["id"]}
    assert _ids(bd_executable, worktree) == {feature_issue["id"]}


@pytest.mark.parametrize("git_worktree_with_separate_dbs", ["native-sync"], indirect=True)
async def test_changes_sync_via_git(git_worktree_with_separate_dbs, bd_executable):
    main_repo, worktree, base = git_worktree_with_separate_dbs
    remote = base / "remote.git"
    source_ref = _git(remote, "rev-parse", "refs/heads/main")
    previous_data_ref = _git(remote, "rev-parse", "refs/dolt/data")
    issue = _create(bd_executable, main_repo, "Shared issue")
    assert issue["id"] not in _ids(bd_executable, worktree)
    _bd(bd_executable, main_repo, "dolt", "push")
    assert _git(remote, "rev-parse", "refs/dolt/data") != previous_data_ref
    _bd(bd_executable, worktree, "dolt", "pull")
    assert issue["id"] in _ids(bd_executable, worktree)
    assert _git(remote, "rev-parse", "refs/heads/main") == source_ref
    shown = json.loads(_bd(bd_executable, worktree, "show", issue["id"], "--json"))[0]
    assert shown["title"] == issue["title"]


async def test_mcp_works_with_separate_databases(git_worktree_with_separate_dbs, bd_executable):
    main_repo, worktree, _ = git_worktree_with_separate_dbs
    expected = {main_repo: set(), worktree: set()}
    async with Client(mcp) as client:
        for root, prefix in [(worktree, "feature"), (main_repo, "main"), (worktree, "feature")]:
            context = await client.call_tool("context", {"workspace_root": str(root)})
            assert not context.is_error
            created = await client.call_tool("create", {"title": f"{prefix} MCP issue", "brief": False})
            assert not created.is_error
            issue = json.loads(created.content[0].text)
            assert issue["id"].startswith(prefix + "-")
            expected[root].add(issue["id"])
            listed = await client.call_tool("list", {"brief": True})
            assert not listed.is_error
            assert {row["id"] for row in json.loads(listed.content[0].text)} == expected[root]
            for workspace, ids in expected.items():
                assert _ids(bd_executable, workspace) == ids


async def test_worktree_database_discovery(git_worktree_with_separate_dbs, bd_executable):
    main_repo, worktree, _ = git_worktree_with_separate_dbs
    for root in [main_repo, worktree]:
        issue = _create(bd_executable, root, "Discovery sentinel")
        nested = root / "nested"
        nested.mkdir()
        assert _ids(bd_executable, root) == {issue["id"]}
        assert _ids(bd_executable, nested) == {issue["id"]}


async def test_jsonl_export_works_in_worktrees(git_worktree_with_separate_dbs, bd_executable):
    main_repo, worktree, _ = git_worktree_with_separate_dbs
    _create(bd_executable, main_repo, "Main export control")
    feature = _create(bd_executable, worktree, "Feature issue")
    target = worktree / ".beads" / "issues.jsonl"
    _bd(bd_executable, worktree, "export", "--output", str(target))
    records = [json.loads(line) for line in target.read_text().splitlines() if line.strip()]
    assert {row["id"] for row in records} == {feature["id"]}
    assert records[0]["title"] == "Feature issue"
    assert _ids(bd_executable, worktree) == {feature["id"]}
