"""deb_sysroot: a C/C++ sysroot assembled from pinned Debian/Ubuntu packages.

Every package is fetched by sha256, so the sysroot is byte-identical on every
host. It gives the hermetic LLVM toolchain (MODULE.bazel) the glibc,
libstdc++ and ICU headers and link libraries that cgo targets need, instead
of whatever the host has under /usr.
"""

# Trims what no compile or link needs, then makes the sysroot self-contained:
# package symlinks may be absolute (outside a chroot they would resolve
# against the HOST), and glibc's linker scripts name the pre-usr-merge /lib
# and /lib64, which the sysroot only has under /usr.
_POSTPROCESS = """
set -euo pipefail
shopt -s globstar nullglob
rm -rf -- $REMOVE
find . -type l -lname '/*' -print0 | while IFS= read -r -d '' l; do
  ln -sfn "$(realpath -m --relative-to="$(dirname "$l")" "$PWD$(readlink "$l")")" "$l"
done
find . -xtype l -delete
grep -rlI --include='*.so' 'GROUP' usr/lib | while IFS= read -r s; do
  sed -i -e 's@ /lib/@ /usr/lib/@g' -e 's@ /lib64/@ /usr/lib64/@g' "$s"
done
"""

def _deb_sysroot_impl(rctx):
    for path, sha256 in rctx.attr.packages.items():
        name = path.rsplit("/", 1)[-1]
        deb = "_debs/" + name
        rctx.download(
            url = [mirror + path for mirror in rctx.attr.mirrors],
            output = deb,
            sha256 = sha256,
        )
        unpacked = "_debs/" + name + ".d"
        rctx.extract(deb, output = unpacked)
        data = [f for f in rctx.path(unpacked).readdir() if f.basename.startswith("data.tar")]
        if len(data) != 1:
            fail("%s: expected one data.tar.* member, found %s" % (name, data))
        rctx.extract(data[0], output = ".")
    rctx.delete("_debs")

    res = rctx.execute(
        ["bash", "-c", _POSTPROCESS],
        environment = {"REMOVE": " ".join(rctx.attr.remove)},
    )
    if res.return_code != 0:
        fail("deb_sysroot: post-processing failed:\n" + res.stderr)

    rctx.file("BUILD.bazel", """\
filegroup(
    name = "sysroot",
    srcs = glob(
        ["**"],
        exclude = [
            "BUILD.bazel",
            "REPO.bazel",
        ],
    ),
    visibility = ["//visibility:public"],
)
""")
    return rctx.repo_metadata(reproducible = True)

deb_sysroot = repository_rule(
    implementation = _deb_sysroot_impl,
    attrs = {
        "packages": attr.string_dict(
            mandatory = True,
            doc = "Pool path (relative to each mirror) to the .deb's sha256.",
        ),
        "mirrors": attr.string_list(
            mandatory = True,
            doc = "Archive roots tried in order; the first is an immutable snapshot.",
        ),
        "remove": attr.string_list(
            doc = "Bash globs (globstar) dropped after unpacking: docs, unused runtimes and archives.",
        ),
    },
)
