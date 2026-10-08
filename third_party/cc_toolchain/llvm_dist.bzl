"""llvm_dist: the slice of an official LLVM release that C/C++ builds use.

LLVM's Linux release archive unpacks to ~12GB (MLIR, Flang, LLDB, clangd,
...), and toolchains_llvm puts every bin/ file into each cgo action's inputs.
This repository keeps clang, lld, the binutils replacements and the clang
resource headers (~700MB), fetched from the same pinned archive by sha256,
and lays them out as toolchains_llvm's own distribution repository does, so
it plugs in through `llvm.toolchain_root`.
"""

def _llvm_dist_impl(rctx):
    if rctx.attr.sliced:
        # A members-only, zstd archive of the same release (tools/cc_toolchain/
        # repack_llvm.sh): the same bytes as the tar -xJf below, extracted by
        # Bazel itself in seconds instead of a minute-plus of single-threaded xz
        # on every cold CI runner. Action keys are unchanged (identical files).
        rctx.download_and_extract(url = rctx.attr.urls, sha256 = rctx.attr.sha256, type = "tar.zst")
        missing = [m for m in rctx.attr.members if not rctx.path(m).exists]
        if missing:
            fail("llvm_dist: the sliced archive lacks %s" % ", ".join(missing))
    else:
        archive = "_llvm.tar.xz"
        rctx.download(url = rctx.attr.urls, output = archive, sha256 = rctx.attr.sha256)
        res = rctx.execute(
            ["tar", "-xJf", archive, "--strip-components=1"] +
            [rctx.attr.strip_prefix + "/" + m for m in rctx.attr.members],
            timeout = 3600,
        )
        if res.return_code != 0:
            fail("llvm_dist: extracting %s failed:\n%s" % (rctx.attr.urls[0], res.stderr))
        rctx.delete(archive)

    # toolchains_llvm symlinks a fixed tool list into its toolchain package;
    # tools this repository leaves out get a stub that fails loudly if run.
    for tool in rctx.attr.stub_tools:
        rctx.file(
            "bin/" + tool,
            "#!/bin/sh\necho '%s is not part of this LLVM slice (third_party/cc_toolchain/llvm_dist.bzl)' >&2\nexit 1\n" % tool,
            executable = True,
        )

    major = rctx.attr.llvm_version.split(".")[0]
    rctx.file(
        "BUILD.bazel",
        rctx.read(Label("@toolchains_llvm//toolchain:BUILD.llvm_repo.tpl")).format(LLVM_VERSION = major),
    )
    return rctx.repo_metadata(reproducible = True)

llvm_dist = repository_rule(
    implementation = _llvm_dist_impl,
    attrs = {
        "llvm_version": attr.string(mandatory = True),
        "urls": attr.string_list(mandatory = True),
        "sha256": attr.string(mandatory = True),
        "strip_prefix": attr.string(doc = "The release archive's top directory (unsliced archives only)."),
        "sliced": attr.bool(
            doc = "urls is a members-only .tar.zst made by repack_llvm.sh, not the release .tar.xz.",
        ),
        "members": attr.string_list(
            mandatory = True,
            doc = "Archive paths (below strip_prefix) to extract.",
        ),
        "stub_tools": attr.string_list(
            doc = "bin/ tools toolchains_llvm links to but no action here runs.",
        ),
    },
)
