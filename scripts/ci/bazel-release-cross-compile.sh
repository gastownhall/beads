#!/usr/bin/env bash
# Release-target cross-compilation gate (bazel.yml's pure-Go lane): build
# every non-test Go target of the module for every GOOS/GOARCH row of
# scripts/ci/release-targets.txt, the Bazel form of
# `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -tags gms_pure_go ./...`.
#
# One `bazel build` of //tools/bazel:release_cross with
# --//tools/bazel:release_platforms set to every manifest row: its split
# transition builds every listed target once per platform with cgo off (and
# the gms_pure_go tag every build carries), all platforms in parallel on the
# remote workers. --keep_going reports every broken target of every
# platform in one run, and a failed run names each failing platform. The target list is every go_library and go_binary
# (the packages `go build ./...` compiles; go_test targets are `go test`'s),
# kept by tools/bazel/go_srcs.py (`make bazel-sync`) minus the ones tagged
# cgo-only (incompatible with //tools/bazel:pure), which `go build ./...` skips
# under CGO_ENABLED=0. Before building, the list is checked against `bazel
# query`, so a target the generator missed fails here instead of going
# uncompiled. The list also holds //tools/bazel:pure_bd_has_no_cgo_only_deps:
# Bazel compiles gozstd's pure stubs (third_party/patches/
# gozstd_nocgo.patch) where `go build` rejects a CGO_ENABLED=0 import of it,
# so that check fails a platform whose pure bd links gozstd.
#
# Coverage boundary, as in the `go build` job this replaces: every target
# builds with cgo off, but .goreleaser.yml builds linux/amd64, linux/arm64
# and windows/amd64 with CGO_ENABLED=1, and release.yml builds the darwin
# pair natively with CGO_ENABLED=1, so the cgo half of the tree (beads_cgo.go
# -> internal/storage/embeddeddolt) is compiled for none of them here.
#
# Usage: bazel-release-cross-compile.sh [extra bazel build flags...]
set -euo pipefail

manifest="$(dirname "${BASH_SOURCE[0]}")/release-targets.txt"

targets=()
while read -r goos goarch extra; do
    if [[ -z "$goarch" || -n "$extra" ]]; then
        echo "::error::malformed row in $manifest: '$goos $goarch $extra', want 'GOOS GOARCH'"
        exit 1
    fi
    targets+=("$goos/$goarch")
done < <(grep -v '^[[:space:]]*#' "$manifest" | grep -v '^[[:space:]]*$' || true)

if [[ "${#targets[@]}" -eq 0 ]]; then
    echo "::error::no release targets in $manifest"
    exit 1
fi

platforms=()
for target in "${targets[@]}"; do
    platforms+=("${target%/*}_${target#*/}")
done

# Every Bazel package holding a Go target (less testonly targets and the
# packages of cgo-only ones) must be reached by //tools/bazel:release_cross;
# a listed label that no longer exists fails the build itself.
universe='kind("go_library|go_binary", //...)'
wanted="$universe except siblings(attr(tags, \"\\bcgo-only\\b\", $universe)) except attr(testonly, 1, $universe)"
# One query at a time: they share the Bazel server.
want_pkgs="$(bazel query "$wanted" --output=package | LC_ALL=C sort -u)"
built_pkgs="$(bazel query 'deps(//tools/bazel:release_cross)' --output=package | LC_ALL=C sort -u)"
missing="$(LC_ALL=C comm -23 <(echo "$want_pkgs") <(echo "$built_pkgs"))"
if [[ -n "$missing" ]]; then
    echo "::error::Go packages //tools/bazel:release_cross does not build (run \`make bazel-sync\`; a package whose Go targets are all invisible to //tools/bazel needs one visible to it):"
    echo "$missing"
    exit 1
fi

platform_list="$(IFS=,; echo "${platforms[*]}")"
echo "::group::bazel build //tools/bazel:release_cross for ${targets[*]}"
status=0
bazel build --keep_going "--//tools/bazel:release_platforms=$platform_list" \
    "$@" -- //tools/bazel:release_cross || status=$?
echo "::endgroup::"
if [[ "$status" -ne 0 ]]; then
    # Bazel's errors above name the target, not the platform (each split
    # configuration's output directory is a hash). Rebuild one platform at a
    # time to name them: the same configurations, so everything that built
    # is a cache hit and only the failures run again.
    failed=()
    for i in "${!platforms[@]}"; do
        if ! bazel build --keep_going "--//tools/bazel:release_platforms=${platforms[$i]}" \
            "$@" -- //tools/bazel:release_cross >/dev/null 2>&1; then
            echo "::error::release cross-compilation failed for ${targets[$i]}"
            failed+=("${targets[$i]}")
        fi
    done
    echo "::error::release cross-compilation failed for: ${failed[*]:-(bazel exit $status; no single platform reproduced it, see the log above)}"
    exit 1
fi

echo "release cross-compilation passed: ${targets[*]}"
