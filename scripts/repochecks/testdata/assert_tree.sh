#!/usr/bin/env bash
# Run by //scripts/repochecks:run_check_test through run_check.sh: the
# checkout holds exactly the slice's files, at their repository paths, under
# git, from the checkout root, with --env exported as an absolute path.
set -euo pipefail
want="scripts/repochecks/testdata/a.txt
scripts/repochecks/testdata/assert_tree.sh
scripts/repochecks/testdata/sub/b.sh"
got="$(git ls-files)"
if [[ "$got" != "$want" ]]; then
	printf 'git ls-files:\n%s\nwant:\n%s\n' "$got" "$want" >&2
	exit 1
fi
[[ "$(pwd -P)" == "$(git rev-parse --show-toplevel)" ]] || { echo "not run from the checkout root" >&2; exit 1; }
[[ ! -L scripts/repochecks/testdata/a.txt ]] || { echo "files must be copies, not runfiles symlinks" >&2; exit 1; }
[[ "$PROBE" == /* && "$(cat "$PROBE")" == a ]] || { echo "PROBE=$PROBE is not the absolute runfile" >&2; exit 1; }
echo "run_check.sh: tree, cwd and --env as expected"
