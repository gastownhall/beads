"""repo_subset: a narrow, path-selected slice of the checkout as test data.

Repository guards (scripts/check-*.sh) read the tracked tree. Declaring all
of //:repo_files as their data would key every one of them to every file in
the repository, so an edit to any Markdown file re-runs the gofmt guard.
repo_subset selects, at analysis time, only the files a guard reads: the
test's action key then covers exactly those files.

Patterns match a file's repository-relative path:

    "**/*.ext"   the suffix, at any depth ("**/*.sh")
    "dir/**"     everything below dir
    "dir/*.ext"  files directly in dir with that suffix ("" dir: the root)
    "path"       exactly that file

Every pattern must match at least one file, so a renamed input fails the
build loudly instead of letting the guard pass over an empty tree.

The rule also writes a manifest, one "<tree path> <rlocation path>" line per
file, which scripts/repochecks/run_check.sh uses to lay the slice out as a
throwaway git checkout. `tree_prefix` places files from another repository
(e.g. @beads_formulas) at their checkout path.
"""

def _matches(path, pattern):
    if pattern.startswith("**/*"):
        return path.endswith(pattern[len("**/*"):])
    if pattern.endswith("/**"):
        return path.startswith(pattern[:-len("**")])
    if "*" in pattern:
        prefix, suffix = pattern.split("*", 1)
        if not (path.startswith(prefix) and path.endswith(suffix)):
            return False
        middle = path[len(prefix):len(path) - len(suffix)]
        return "/" not in middle and len(path) >= len(prefix) + len(suffix)
    return path == pattern

def _rel(f):
    """f's path within its own repository."""
    if f.short_path.startswith("../"):
        return f.short_path[len("../"):].split("/", 1)[1]
    return f.short_path

def _rlocation(ctx, f):
    """f's runfiles path (what $(rlocationpath) would give)."""
    if f.short_path.startswith("../"):
        return f.short_path[len("../"):]
    return ctx.workspace_name + "/" + f.short_path

def _repo_subset_impl(ctx):
    files = ctx.files.srcs
    picked = []
    unmatched = list(ctx.attr.include)
    for f in files:
        rel = _rel(f)
        if any([_matches(rel, p) for p in ctx.attr.exclude]):
            continue
        hit = False
        for p in ctx.attr.include:
            if _matches(rel, p):
                hit = True
                if p in unmatched:
                    unmatched.remove(p)
        if hit:
            picked.append(f)
    if unmatched:
        fail("repo_subset %s: no file matches %s" % (ctx.label, unmatched))

    lines = []
    for f in picked:
        lines.append("%s%s %s" % (ctx.attr.tree_prefix, _rel(f), _rlocation(ctx, f)))
    manifest = ctx.actions.declare_file(ctx.label.name + ".manifest")
    ctx.actions.write(manifest, "\n".join(sorted(lines)) + "\n")
    return [DefaultInfo(
        files = depset([manifest]),
        runfiles = ctx.runfiles(files = picked + [manifest]),
    )]

repo_subset = rule(
    implementation = _repo_subset_impl,
    attrs = {
        "srcs": attr.label_list(allow_files = True, mandatory = True),
        "include": attr.string_list(mandatory = True),
        "exclude": attr.string_list(),
        "tree_prefix": attr.string(doc = "Prepended to every file's checkout path."),
    },
)
