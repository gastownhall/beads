#!/usr/bin/env python3
"""Maintain `go_srcs` filegroups for tests that scan Go source under Bazel.

Some guards parse Go source at test time (journal completeness, role census,
import boundaries, contract-leg registry). Under `bazel test`, and above all
under remote execution, a test sees only its declared runfiles, so the source
it scans must be declared as `data`. Bazel globs stop at package boundaries,
so a scan over a directory tree needs one filegroup per package plus an
aggregate. Gazelle does not generate either; this script does, restricted to
the packages listed below, so only tests that need source pay for it.

For every package in PACKAGES, and every package under a root in TREES, the
BUILD.bazel file gets a managed block holding

    filegroup(name = "go_srcs", srcs = glob(["**/*.go"]))

(the package's own Go files plus those in non-package subdirectories). Each
TREES root also gets `tree_go_srcs`, aggregating every `go_srcs` under it.
Blocks in packages that are no longer listed are removed.

Run from the repository root after gazelle (see `make bazel-sync`). Output is
deterministic and gazelle-stable, so a clean sync leaves git clean.

Every package also gets a managed `repo_files` block: a filegroup of all of
the package's own files (`glob(["**"])` less local build and editor debris),
and the root package's aggregates every package's into `//:repo_files`. That
is the checkout as Bazel sees it, the `data` of the repository-policy tests
that walk the whole tree (every BUILD file, every test file, every tracked
Markdown file). A package without the block would drop out of their view and
pass them vacuously, which is why `make bazel-sync-check` (bazel.yml's BUILD
sync step, on every PR) fails on a missing or stale block. Trees in
.bazelignore (.beads, website, the nested example modules, agent worktrees,
node_modules) are outside Bazel and so outside //:repo_files.

With --check nothing is written: stale blocks are printed as a diff and the
exit status is 1 (see `make bazel-sync-check`).
"""

from __future__ import annotations

import difflib
import os
import re
import sys

# Packages whose own Go source a test scans.
PACKAGES = (
    "backend",  # //backend:backend_test (public alias census)
    "backend/conformance",  # //backend/conformance, //internal/storage
    "beadserrors",  # role facade alias targets
    "cmd/bd",  # //cmd/bd:bd_test (capability registry, journal, serve scans)
    "cmd/bd/doctor",  # //cmd/bd:bd_test (events-journal construction scan)
    "cmd/bd/doctor/fix",  # //cmd/bd:bd_test (events-journal construction scan)
    "internal/httpclient",  # //internal/httpclient/wire:wire_test (handshake/skew AST sweep)
    "internal/types",  # role facade alias targets
    "issueops",  # //backend/conformance (role facade census)
    "journalops",  # //backend/conformance (role facade census)
    "memoryops",  # //backend/conformance (role facade census)
)

# Roots whose whole Go tree a test scans.
TREES = (
    "internal/storage",  # //internal/storage walks every *_test.go below it
)

BEGIN = "# --- begin go_srcs (managed by tools/bazel/go_srcs.py; run `make bazel-sync`) ---"
END = "# --- end go_srcs ---"
REPO_BEGIN = "# --- begin repo_files (managed by tools/bazel/go_srcs.py; run `make bazel-sync`) ---"
REPO_END = "# --- end repo_files ---"
BLOCK_RES = tuple(
    re.compile(r"\n*" + re.escape(begin) + r".*?" + re.escape(end) + r"\n*", re.DOTALL)
    for begin, end in ((BEGIN, END), (REPO_BEGIN, REPO_END))
)
SKIP_DIRS = {"testdata", "node_modules"}

# Untracked build output and debris (.gitignore's patterns that can appear in
# any directory) that must not become test inputs on a developer machine. No
# tracked file matches them.
REPO_FILES_EXCLUDE = (
    "**/*.db",
    "**/*.exe",
    "**/*.out",
    "**/*.prof",
    "**/*.pyc",
    "**/*.test",
    "**/__pycache__/**",
    "**/node_modules/**",
)

# The same at the repository root only: the git directory, Bazel's convenience
# symlinks (a glob follows them into the output tree), local binaries,
# per-developer Bazel rc files (they hold remote endpoints) and tool state.
ROOT_REPO_FILES_EXCLUDE = (
    ".agents/**",
    ".claude/*.lock",
    ".claude/*.log",
    ".claude/settings.local.json",
    ".amp/**",
    ".augment/**",
    ".bazelrc.local",
    ".codex/**",
    ".cursor/**",
    ".direnv/**",
    ".envrc",
    ".git/**",
    ".idea/**",
    ".logs/**",
    ".vscode/**",
    "bazel-*/**",
    "bd",
    "bd-fixed",
    "bd-original",
    "bd-test",
    "bd_test",
    "beads",
    "go.work",
    "go.work.sum",
    "history/**",
    "mcp_agent_mail/**",
    "npm-package/bin/*.tar.gz",
    "npm-package/bin/*.zip",
    "npm-package/bin/CHANGELOG.md",
    "npm-package/bin/LICENSE",
    "npm-package/bin/README.md",
    "npm-package/bin/bd",
    "npm-package/package-lock.json",
    "output",
    "result",
    "state.json",
    "user.bazelrc",
)

# Who may read //:repo_files: the repository-policy tests.
REPO_FILES_VISIBILITY = (
    "//scripts:__pkg__",
    "//scripts/repochecks:__pkg__",
    "//test/docsync:__pkg__",
)


def packages_under(root: str) -> list[str]:
    found = []
    for dirpath, dirnames, files in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS and not d.startswith("."))
        if "BUILD.bazel" in files:
            found.append(os.path.relpath(dirpath).replace(os.sep, "/"))
    return sorted(found)


def block(pkg: str, tree_members: list[str] | None) -> str:
    lines = [
        BEGIN,
        "",
        "filegroup(",
        '    name = "go_srcs",',
        '    srcs = glob(',
        '        ["**/*.go"],',
        "        allow_empty = True,",
        "    ),",
        '    visibility = ["//:__subpackages__"],',
        ")",
    ]
    if tree_members is not None:
        lines += [
            "",
            "filegroup(",
            '    name = "tree_go_srcs",',
            "    srcs = [",
            '        ":go_srcs",',
        ]
        lines += [f'        "//{m}:go_srcs",' for m in tree_members if m != pkg]
        lines += [
            "    ],",
            '    visibility = ["//:__subpackages__"],',
            ")",
        ]
    lines += ["", END]
    return "\n".join(lines) + "\n"


def repo_files_block(pkg: str, packages: list[str]) -> str:
    """The repo_files block of pkg ("." is the root, which aggregates)."""
    root = pkg == "."
    exclude = REPO_FILES_EXCLUDE + (ROOT_REPO_FILES_EXCLUDE if root else ())
    lines = [
        REPO_BEGIN,
        "",
        "filegroup(",
        '    name = "repo_files",',
        "    srcs = glob(",
        '        ["**"],',
        "        exclude = [",
    ]
    lines += [f'            "{e}",' for e in sorted(exclude)]
    # No allow_empty: every package holds at least its BUILD.bazel.
    lines += ["        ],"]
    if root:
        lines += ["    ) + ["]
        lines += [f'        "//{p}:repo_files",' for p in packages if p != "."]
        lines += ["    ],", "    visibility = ["]
        lines += [f'        "{v}",' for v in REPO_FILES_VISIBILITY]
        lines += ["    ],"]
    else:
        lines += ["    ),", '    visibility = ["//:__pkg__"],']
    lines += [")", "", REPO_END]
    return "\n".join(lines) + "\n"


def rewrite(path: str, new_blocks: list[str], check: bool) -> bool:
    """Bring path's managed blocks up to date; return True if any was stale.

    In check mode the file is left alone and the needed change is printed.
    """
    with open(path) as f:
        src = f.read()
    stripped = src
    for block_re in BLOCK_RES:
        stripped = block_re.sub("\n", stripped)
    out = stripped.rstrip("\n") + "\n"
    for new_block in new_blocks:
        out += "\n" + new_block
    if out == src:
        return False
    if check:
        sys.stdout.writelines(
            difflib.unified_diff(
                src.splitlines(keepends=True),
                out.splitlines(keepends=True),
                fromfile=f"a/{path}",
                tofile=f"b/{path}",
            )
        )
    else:
        with open(path, "w") as f:
            f.write(out)
    return True


def main(argv: list[str]) -> int:
    check = False
    for arg in argv:
        if arg == "--check":
            check = True
        else:
            print(f"usage: go_srcs.py [--check] (unknown argument {arg!r})", file=sys.stderr)
            return 2
    if not (os.path.exists("MODULE.bazel") and os.path.exists("BUILD.bazel")):
        print("run from the repository root", file=sys.stderr)
        return 1
    wanted: dict[str, list[str] | None] = {}
    for pkg in PACKAGES:
        if not os.path.exists(os.path.join(pkg, "BUILD.bazel")):
            print(f"go_srcs.py: {pkg} has no BUILD.bazel", file=sys.stderr)
            return 1
        wanted[pkg] = None
    for root in TREES:
        members = packages_under(root)
        if root not in members:
            print(f"go_srcs.py: tree root {root} has no BUILD.bazel", file=sys.stderr)
            return 1
        for pkg in members:
            wanted.setdefault(pkg, None)
        wanted[root] = members
    stale = []
    packages = packages_under(".")
    for pkg in packages:
        path = os.path.join(pkg, "BUILD.bazel")
        blocks = [block(pkg, wanted[pkg])] if pkg in wanted else []
        blocks.append(repo_files_block(pkg, packages))
        changed = rewrite(path, blocks, check)
        if changed:
            stale.append(path)
    if check and stale:
        print(
            f"go_srcs.py: {len(stale)} stale managed block(s): {', '.join(stale)}; run `make bazel-sync`",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
