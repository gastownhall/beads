#!/usr/bin/env bash
# Deterministic members-only repack of the pinned LLVM release for beads'
# hermetic C toolchain (MODULE.bazel llvm_dist "llvm_dist_linux_x86_64").
#
#   repack_llvm.sh MEMBERS_FILE OUT.tar.zst
#
# MEMBERS_FILE is one archive path per line (below the strip prefix); this
# directory's llvm_members.txt matches MODULE.bazel's llvm_dist_linux_x86_64
# "members" exactly, so:
#   tools/cc_toolchain/repack_llvm.sh tools/cc_toolchain/llvm_members.txt \
#     LLVM-22.1.8-Linux-X64-slice.tar.zst
# rebuilds and verifies the pinned slice. Changing MODULE.bazel's members
# list requires updating llvm_members.txt the same way, and vice versa.
#
# Downloads the upstream release (sha256-pinned, as MODULE.bazel pins it),
# extracts exactly the listed members (paths below the strip prefix), and
# writes a reproducible .tar.zst: sorted names, mtime 0, uid/gid 0, no xattrs.
# The extracted files are byte-identical to what llvm_dist.bzl extracts today,
# so every action key that reads them is unchanged; only the archive (URL,
# sha256, format) changes. Re-running the script yields the same sha256.
set -euo pipefail
members=${1:?members file}
out=${2:?output .tar.zst}
ver=22.1.8
url="https://github.com/llvm/llvm-project/releases/download/llvmorg-${ver}/LLVM-${ver}-Linux-X64.tar.xz"
sha=df0e1ecf16caf3489a272a5eea4eec9b0d82878f6477fa309504f918a0006384
prefix="LLVM-${ver}-Linux-X64"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "$work/llvm.tar.xz" "$url"
echo "$sha  $work/llvm.tar.xz" | sha256sum -c - >/dev/null
mkdir "$work/x"
# xz is one stream: this is the slow, single-threaded step CI pays today.
# shellcheck disable=SC2046 # intentional: one archive path per tar argument
tar -xJf "$work/llvm.tar.xz" -C "$work/x" --strip-components=1 \
	$(sed "s#^#${prefix}/#" "$members")
tar -C "$work/x" --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
	--no-xattrs --no-acls --no-selinux -cf - . | zstd -19 -T0 -q -o "$out" -f
sha256sum "$out"
