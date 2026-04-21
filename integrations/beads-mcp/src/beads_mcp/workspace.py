"""Shared workspace path resolution helpers for beads-mcp."""

from __future__ import annotations

import logging
import os
import subprocess
import sys

logger = logging.getLogger(__name__)


def get_git_workspace_roots(path: str) -> tuple[str, str] | None:
    """Return (worktree_root, main_repo_root) for a git path."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel", "--git-common-dir"],
            cwd=path,
            capture_output=True,
            text=True,
            check=False,
            shell=sys.platform == "win32",
            stdin=subprocess.DEVNULL,
        )
        if result.returncode == 0:
            lines = [line.strip() for line in result.stdout.splitlines() if line.strip()]
            if len(lines) >= 2:
                worktree_root = os.path.realpath(lines[0])
                common_dir = lines[1]

                if not os.path.isabs(common_dir):
                    common_dir = os.path.join(path, common_dir)
                common_dir = os.path.realpath(common_dir)

                main_repo_root = (
                    os.path.dirname(common_dir) if os.path.basename(common_dir) == ".git" else common_dir
                )
                return (worktree_root, main_repo_root)

    except Exception as exc:
        logger.debug("Git detection failed for %s: %s", path, exc)

    return None


def resolve_workspace_root(path: str) -> str:
    """Resolve a path to the repo that owns the active beads workspace."""
    roots = get_git_workspace_roots(path)
    if roots is not None:
        worktree_root, main_repo_root = roots

        local_beads = os.path.join(worktree_root, ".beads")
        main_beads = os.path.join(main_repo_root, ".beads")
        if (
            worktree_root != main_repo_root
            and not os.path.isdir(local_beads)
            and os.path.isdir(main_beads)
        ):
            return main_repo_root

        return worktree_root

    return os.path.abspath(path)
