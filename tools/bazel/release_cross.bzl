"""Build Go targets for every release platform in one Bazel invocation.

scripts/ci/bazel-release-cross-compile.sh passes the platforms of
scripts/ci/release-targets.txt as --//tools/bazel:release_platforms; the
split transition below then configures every target once per platform, with
pure Go (cgo off), so all platforms build in parallel on the remote workers
instead of one `bazel build --platforms=...` after another.
"""

def _release_platforms_transition_impl(settings, _attr):
    platforms = settings["//tools/bazel:release_platforms"]
    if not platforms:
        fail("--//tools/bazel:release_platforms is empty; scripts/ci/bazel-release-cross-compile.sh sets it from scripts/ci/release-targets.txt")
    return {
        platform: {
            "//command_line_option:platforms": ["@rules_go//go/toolchain:" + platform],
            "@rules_go//go/config:pure": True,
        }
        for platform in platforms
    }

_release_platforms_transition = transition(
    implementation = _release_platforms_transition_impl,
    inputs = ["//tools/bazel:release_platforms"],
    outputs = [
        "//command_line_option:platforms",
        "@rules_go//go/config:pure",
    ],
)

def _release_cross_build_impl(ctx):
    return [DefaultInfo(files = depset(transitive = [
        dep[DefaultInfo].files
        for deps in ctx.split_attr.targets.values()
        for dep in deps
    ]))]

release_cross_build = rule(
    implementation = _release_cross_build_impl,
    doc = "Builds `targets` for every rules_go platform (<os>_<arch>) in --//tools/bazel:release_platforms, with cgo off.",
    attrs = {
        "targets": attr.label_list(
            cfg = _release_platforms_transition,
            mandatory = True,
        ),
    },
)

def _string_list_flag_impl(_ctx):
    return []

string_list_flag = rule(
    implementation = _string_list_flag_impl,
    build_setting = config.string_list(flag = True),
)
