#!/usr/bin/env bash
# The push-time test suite, run by .githooks/pre-push when a pushed branch
# changes Go or Bazel inputs: bazel.yml's test lane, `bazel test //...
# --config=ci`, with the action keys CI uses, so a push reuses what CI and
# earlier pushes already computed.
#
# BD_PREPUSH_SUITE picks where it runs (default auto):
#   auto   remote-exec when bazel's effective options name a remote executor
#          (a maintainer's .bazelrc.local, an agent host's ~/.bazelrc);
#          fork-cache when bazel is installed without one; go when bazel is
#          not installed.
#   rbe    --config=remote-exec; fails when no rc file names an executor (the
#          suite would otherwise compile and run on this machine).
#   cache  --config=fork-cache: the anonymous read-only cache; misses run on
#          this machine and nothing is uploaded.
#   go     make test-go (plain go test through scripts/test.sh), under a
#          banner saying it is not what CI enforces.
# BAZEL and MAKE override the binaries. Bypass the hook: git push --no-verify.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

bazel_bin="${BAZEL:-bazel}"
make_bin="${MAKE:-make}"
mode="${BD_PREPUSH_SUITE:-auto}"
executor=""
# Why the push runs plain go test instead of bazel, and how to get bazel back.
go_why="BD_PREPUSH_SUITE=go"
go_fix="unset BD_PREPUSH_SUITE to run the bazel suite"

# The go suite is not what CI enforces: bazel.yml gates on bazel test, whose
# nogo lint/vet, formatting and repository-guard targets have no go test
# counterpart. Say so loudly, with the reason, so a green push is not read as
# CI parity.
announce_go_suite() {
  {
    echo "pre-push: ========================================================================"
    echo "pre-push: running make test-go (plain go test): NOT the bazel suite CI gates on."
    echo "pre-push: why: $go_why"
    echo "pre-push: CI runs bazel test //... --config=ci (.github/workflows/bazel.yml); a push"
    echo "pre-push: that passes here can still fail there (nogo lint/vet, formatting, guards)."
    echo "pre-push: fix: $go_fix"
    echo "pre-push: ========================================================================"
  } >&2
}

# The remote executor some rc file names for this workspace, empty for none:
# the last non-empty --remote_executor among the rc options Bazel reads
# (system, workspace with .bazelrc.local and user.bazelrc, home) and the
# remote-exec config's definitions, from `bazel info --announce_rc`. Bazel's
# own reading of every rc, not a grep of one file, so an agent host's
# ~/.bazelrc executor counts. Fails, printing Bazel's error, when Bazel cannot
# read its options.
effective_remote_executor() {
  local announced
  if ! announced="$("$bazel_bin" info --announce_rc --config=remote-exec release 2>&1 >/dev/null)"; then
    printf '%s\n' "$announced" >&2
    return 1
  fi
  printf '%s\n' "$announced" | awk '
    /^INFO: (Reading rc options|Options provided by the client)/ { section = 1; next }
    /^[^[:space:]]/ { section = 0 }
    !section && !/^INFO: Found applicable config definition [^ ]*:remote-exec / { next }
    {
      for (i = 1; i <= NF; i++) {
        value = ""
        if ($i ~ /^--remote_executor=/) {
          value = substr($i, length("--remote_executor=") + 1)
        } else if ($i == "--remote_executor" && i < NF) {
          value = $(i + 1)
        }
        if (value != "") {
          executor = value
        }
      }
    }
    END { print executor }'
}

# Sets $executor, or fails the push: an unreadable option set is no evidence
# for either mode.
probe_executor() {
  if ! executor="$(effective_remote_executor)"; then
    echo "pre-push: bazel info could not read this workspace's options (above); fix the rc, or set BD_PREPUSH_SUITE=rbe|cache|go" >&2
    exit 2
  fi
}

have_bazel() {
  command -v "$bazel_bin" >/dev/null 2>&1
}

case "$mode" in
auto)
  if ! have_bazel; then
    go_why="bazel is not installed"
    go_fix="install bazelisk as bazel (CONTRIBUTING.md \"Building and testing\")"
    mode=go
  else
    probe_executor
    if [ -n "$executor" ]; then
      mode=rbe
    else
      mode=cache
    fi
  fi
  ;;
go | cache) ;;
rbe)
  if have_bazel; then
    probe_executor
    if [ -z "$executor" ]; then
      echo "pre-push: BD_PREPUSH_SUITE=rbe but no rc file names a --remote_executor; configure one (engdocs/TESTING.md \"Building and testing\") or use BD_PREPUSH_SUITE=cache|go" >&2
      exit 2
    fi
  fi
  ;;
*)
  echo "pre-push: BD_PREPUSH_SUITE=$mode is not one of auto, rbe, cache, go" >&2
  exit 2
  ;;
esac

case "$mode" in
go)
  announce_go_suite
  exec "$make_bin" test-go
  ;;
rbe) config=remote-exec ;;
cache) config=fork-cache ;;
esac

if ! have_bazel; then
  echo "pre-push: BD_PREPUSH_SUITE=$mode needs bazel on PATH; install bazelisk or use BD_PREPUSH_SUITE=go" >&2
  exit 2
fi

echo "pre-push: bazel test //... --config=ci --config=$config${executor:+ on $executor} (BD_PREPUSH_SUITE=go runs plain go test instead)" >&2
exec "$bazel_bin" test //... --config=ci "--config=$config"
