"""One Bazel test per historical-upgrade corpus release (see BUILD.bazel)."""

load("@rules_shell//shell:sh_test.bzl", "sh_test")
load("//tools/bazel:bd_releases.bzl", "LINUX_AMD64")
load("//tools/bazel:dolt.bzl", "hermetic_bin")

_DOLT_RUNTIME_LABEL = "@dolt_test_runtime_linux_amd64//:dolt"

def historical_upgrade_tests(corpus, dolt_runtime, helpers, source_tag, source_tag_bin):
    """Declares historical_upgrade_<release>_test and its release directory per corpus release.

    Args:
      corpus: every release the corpus upgrades from.
      dolt_runtime: the releases whose lanes need the external Dolt runtime.
      helpers: releases every lane may run besides its own (bridges).
      source_tag: the release built from source (no @bd_releases entry).
      source_tag_bin: the label of that source build.
    """
    for v in corpus:
        suffix = v.replace(".", "_")
        releases = {r: None for r in [v] + helpers if r != source_tag}
        hermetic_bin(
            name = "releases_" + suffix,
            target_compatible_with = LINUX_AMD64,
            tools = {"@bd_releases//:" + r: r for r in sorted(releases.keys())},
        )
        data = [
            ":releases_" + suffix,
            "//cmd/bd:bd_for_tests",
            "//scripts:migration_harness",
        ]
        env = {
            "CANDIDATE_BIN": "$(rootpath //cmd/bd:bd_for_tests)",
            "HISTORICAL_RELEASE_DIR": "{}/releases_{}".format(native.package_name(), suffix),
        }
        if v in dolt_runtime:
            data.append(_DOLT_RUNTIME_LABEL)
            env["DOLT_BIN"] = "$(rootpath {})".format(_DOLT_RUNTIME_LABEL)
        if v == source_tag:
            data.append(source_tag_bin)
            env["SOURCE_TAG_SQLITE_BIN"] = "$(rootpath {})".format(source_tag_bin)
        sh_test(
            name = "historical_upgrade_{}_test".format(suffix),
            size = "large",
            srcs = ["historical_upgrade_test.sh"],
            args = [v],
            data = data,
            env = env,
            target_compatible_with = LINUX_AMD64,
        )
