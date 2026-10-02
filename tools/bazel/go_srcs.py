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
BLOCK_RE = re.compile(r"\n*" + re.escape(BEGIN) + r".*?" + re.escape(END) + r"\n*", re.DOTALL)
SKIP_DIRS = {"testdata", "node_modules"}


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


def rewrite(path: str, new_block: str | None, check: bool) -> bool:
    """Bring path's managed block up to date; return True if it was stale.

    In check mode the file is left alone and the needed change is printed.
    """
    with open(path) as f:
        src = f.read()
    stripped = BLOCK_RE.sub("\n", src).rstrip("\n") + "\n"
    out = stripped if new_block is None else stripped + "\n" + new_block
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
    for pkg in packages_under("."):
        path = os.path.join(pkg, "BUILD.bazel")
        if pkg in wanted:
            changed = rewrite(path, block(pkg, wanted[pkg]), check)
        else:
            with open(path) as f:
                changed = BEGIN in f.read() and rewrite(path, None, check)
        if changed:
            stale.append(path)
    if check and stale:
        print(
            f"go_srcs.py: {len(stale)} stale go_srcs block(s): {', '.join(stale)}; run `make bazel-sync`",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
