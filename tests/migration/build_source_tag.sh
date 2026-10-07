#!/usr/bin/env bash
# Builds a historical bd from its module source, offline, the way
# scripts/migration-test/lib/binary.sh's build_verified_v091_source_binary
# does with network access: `go mod download` the module, then `go build
# -trimpath ./cmd/bd` in its directory with CGO and a writable copy of its
# go.mod, using the Go release it was qualified on.
#
# Usage: build_source_tag.sh <out> <go binary> <a file under the proxy tree> <module> <version> <go version>
# Paths are execroot-relative ($(execpath ...)). GOPROXY is the pinned
# file:// tree (tools/bazel/go_module_proxy.bzl), so every module is a
# sha256-verified input, and go checks each dependency against the module's
# go.sum; GOSUMDB is off because there is no network to reach it.
set -euo pipefail
out="$1" go_bin="$2" proxy_file="$3" module="$4" version="$5" go_version="$6"
root="$PWD"
tmp="$(mktemp -d)"
trap 'chmod -R u+w "$tmp" 2>/dev/null || true; rm -rf "$tmp"' EXIT
export GOROOT="$root/${go_bin%/bin/go}"
export PATH="$GOROOT/bin:$PATH"
export HOME="$tmp/home" GOPATH="$tmp/gopath" GOMODCACHE="$tmp/mod" GOCACHE="$tmp/cache"
export GOPROXY="file://$root/${proxy_file%%/proxy/*}/proxy" GOSUMDB=off GONOSUMDB= GOPRIVATE= GONOPROXY=
export GOFLAGS=-modcacherw GOWORK=off GOTOOLCHAIN=local GO111MODULE=on
export CGO_ENABLED=1 GOOS=linux GOARCH=amd64
mkdir -p "$HOME" "$tmp/scratch"
got="$(cd "$tmp/scratch" && go env GOVERSION)"
if [[ "$got" != "$go_version" ]]; then
	echo "build_source_tag: $module@$version needs $go_version, the pinned SDK is $got" >&2
	exit 1
fi
(cd "$tmp/scratch" && go mod download "$module@$version")
src="$GOMODCACHE/$module@$version"
cp -f "$src/go.mod" "$tmp/source.mod"
cp -f "$src/go.sum" "$tmp/source.sum"
chmod u+w "$tmp/source.mod" "$tmp/source.sum"
(cd "$src" && go build -trimpath -modfile="$tmp/source.mod" -o "$root/$out" ./cmd/bd)
