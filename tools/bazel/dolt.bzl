"""Hermetic Dolt CLI for Bazel tests.

The release pinned here must equal scripts/ci/install-dolt.sh (the CLI that
`go test` jobs put on PATH) and the dolt-sql-server image in
internal/testutil/testdoltcommon.go; scripts/ci_workflow_test.go enforces it.
Raise all three together, and only after the measurement described in
install-dolt.sh.

MODULE.bazel declares one repository per platform (dolt_<os>_<arch>); only the
one selected by //tools/bazel:dolt for the execution platform is fetched.
"""

DOLT_VERSION = "2.2.0"

# sha256 of the upstream release tarballs dolt-<os>-<arch>.tar.gz, keyed by
# version so that bumping DOLT_VERSION without adding its digests fails instead
# of reusing the previous release from the repository cache (which is keyed by
# sha256 alone). They match the asset digests GitHub reports for the release
# (`gh api repos/dolthub/dolt/releases/tags/v<version>`).
DOLT_SHA256 = {
    "2.2.0": {
        "darwin-amd64": "d40b57933e2a2c025a5a3c269eb87594b1aa71ead16c9902213d521529a19b02",
        "darwin-arm64": "c6737dc2c5806e2eeef4839ad76c28167c861f878af3071df1242a6589d81267",
        "linux-amd64": "1f7ad8c2622995789420a3fb0f2d16b4aa7430000a825dd91d5938f36480cbf6",
        "linux-arm64": "a49a566d7c1ee9fdff553644855737f9d87047557c1ce1951aa7e7053277f4af",
    },
}

_BUILD = """\
package(default_visibility = ["//visibility:public"])

exports_files(["dolt"])
"""

def _dolt_release_impl(rctx):
    platform = rctx.attr.platform
    digests = DOLT_SHA256.get(DOLT_VERSION)
    if digests == None:
        fail("DOLT_SHA256 has no digests for DOLT_VERSION {}".format(DOLT_VERSION))
    if platform not in digests:
        fail("no pinned dolt {} release for platform {}".format(DOLT_VERSION, platform))
    url = "https://github.com/dolthub/dolt/releases/download/v{v}/dolt-{p}.tar.gz".format(
        v = DOLT_VERSION,
        p = platform,
    )
    rctx.download_and_extract(
        url = url,
        sha256 = digests[platform],
        # Ties the repository-cache entry to this URL (and so this version).
        canonical_id = url,
        stripPrefix = "dolt-{}/bin".format(platform),
    )
    rctx.file("BUILD.bazel", _BUILD)

dolt_release = repository_rule(
    implementation = _dolt_release_impl,
    attrs = {
        "platform": attr.string(
            mandatory = True,
            doc = "Release platform suffix, for example linux-amd64.",
        ),
    },
    doc = "Downloads the pinned Dolt CLI release for one platform and exports its `dolt` binary.",
)

def _hermetic_bin_impl(ctx):
    symlinks = {}
    for target, name in ctx.attr.tools.items():
        files = target[DefaultInfo].files.to_list()
        if len(files) != 1:
            fail("{} must provide exactly one file, got {}".format(target.label, len(files)))
        symlinks["{}/{}/{}".format(ctx.label.package, ctx.label.name, name)] = files[0]
    return [DefaultInfo(runfiles = ctx.runfiles(symlinks = symlinks))]

hermetic_bin = rule(
    implementation = _hermetic_bin_impl,
    attrs = {
        "tools": attr.label_keyed_string_dict(
            allow_files = True,
            mandatory = True,
            doc = "Executable file -> command name it is exposed under.",
        ),
    },
    doc = """Exposes tools as <package>/<name>/<command> in the runfiles of the
main repository: one stable directory a test wrapper can put first on PATH,
whatever canonical repository name the tool came from.""",
)
