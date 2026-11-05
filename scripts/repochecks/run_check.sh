#!/usr/bin/env bash
# run_check.sh: run a repository guard over a declared slice of the checkout.
#
#   run_check.sh [--tree MANIFEST]... [--env NAME=RLOCATION]... \
#       [--path RLOCATION]... -- PROGRAM [ARG...]
#
# The guards under scripts/ (check-build-tags.sh, fmt-check.sh, ...) walk the
# repository root above their own directory, often with `git ls-files` or
# `git grep`. Under `bazel test` they get instead a throwaway checkout built
# from exactly their declared inputs:
#
#   --tree MANIFEST    a repo_subset manifest (tools/bazel/repo_subset.bzl),
#                      as an rlocation path: every file it lists is copied
#                      (dereferenced) to its checkout path, then the tree is
#                      `git add`ed, so git ls-files/grep see those files.
#   --env NAME=RLOC    export NAME as the absolute path of a runfile (e.g.
#                      GOFMT=<the registered SDK's gofmt>).
#   --path RLOC        put the runfile's directory first on PATH.
#
# PROGRAM is a checkout path (scripts/check-build-tags.sh) when the tree holds
# it, so the guard resolves its repository root to the tree; otherwise an
# rlocation path (a Bazel-built tool). It runs from the tree's root.
set -euo pipefail

runfiles="${TEST_SRCDIR:?run_check.sh runs under bazel test}"
tree="${TEST_TMPDIR:?}/checkout"
manifests=()
path_dirs=()
envs=()
while [[ $# -gt 0 ]]; do
	case "$1" in
	--tree) manifests+=("$2"); shift 2 ;;
	--env) envs+=("$2"); shift 2 ;;
	--path) path_dirs+=("$2"); shift 2 ;;
	--) shift; break ;;
	*) echo "run_check.sh: unknown argument $1" >&2; exit 2 ;;
	esac
done
[[ $# -ge 1 ]] || { echo "run_check.sh: no PROGRAM" >&2; exit 2; }
[[ ${#manifests[@]} -gt 0 ]] || { echo "run_check.sh: no --tree" >&2; exit 2; }

rm -rf "$tree"
mkdir -p "$tree"
files=0
for m in "${manifests[@]}"; do
	while read -r rel rloc; do
		[[ -n "$rel" ]] || continue
		mkdir -p "$tree/$(dirname "$rel")"
		cp -L "$runfiles/$rloc" "$tree/$rel"
		files=$((files + 1))
	done <"$runfiles/$m"
done
echo "run_check.sh: checkout of $files declared files"

git -C "$tree" init -q
git -C "$tree" add -f -A .

for e in "${envs[@]+"${envs[@]}"}"; do
	export "${e%%=*}=$runfiles/${e#*=}"
done
for p in "${path_dirs[@]+"${path_dirs[@]}"}"; do
	PATH="$(dirname "$runfiles/$p"):$PATH"
done
export PATH

program="$1"
shift
if [[ -e "$tree/$program" ]]; then
	program="$tree/$program"
else
	program="$runfiles/$program"
fi
cd "$tree"
exec "$program" "$@"
