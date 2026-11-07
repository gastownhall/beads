"""One upgrade smoke test per pinned previous release (see BUILD.bazel)."""

load("@bd_releases//:versions.bzl", "VERSIONS")
load("@rules_shell//shell:sh_test.bzl", "sh_test")
load("//tools/bazel:bd_releases.bzl", "LINUX_AMD64")

def upgrade_smoke_tests(window, tags):
    """Declares upgrade_smoke_<release>_test for the newest `window` catalog releases.

    Args:
      window: how many of the newest pinned releases to test.
      tags: the tests' tags.
    """
    for v in VERSIONS[-window:]:
        sh_test(
            name = "upgrade_smoke_{}_test".format(v.replace(".", "_")),
            size = "large",
            srcs = ["upgrade_smoke_test.sh"],
            args = [v],
            data = [
                "//:.buildflags",
                "//cmd/bd:bd_for_tests",
                "//scripts:upgrade-smoke-test.sh",
                "@bd_releases//:" + v,
            ],
            env = {
                "CANDIDATE_BIN": "$(rootpath //cmd/bd:bd_for_tests)",
                "PREV_BIN": "$(rootpath @bd_releases//:{})".format(v),
            },
            tags = tags,
            target_compatible_with = LINUX_AMD64,
        )
