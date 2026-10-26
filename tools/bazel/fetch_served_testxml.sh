#!/usr/bin/env bash
# Downloads the CI timings //backend/http:http_served_test's pinned shards are
# sized from, for tools/bazel/pin_shards.py. bazel.yml's embedded lane uploads
# every backend/http served target's test.xml as the
# bazel-embedded-served-testxml artifact on every run; this takes the newest
# <runs> runs that still have one (artifacts expire after 7 days) among
# successful same-repository pr.yml pull_request runs and bazel.yml pushes to
# main, unpacks each into <dir>/<run id>/ (reusing one already there), and
# prints each run's http_served_test directory: pin_shards.py's TESTLOGS. So
# one command regenerates the manifest:
#
#   python3 tools/bazel/pin_shards.py --shards 10 --min-seconds 3 \
#       --record-measured --tests-in backend/http \
#       --write backend/http/served_pinned_shards.txt \
#       $(tools/bazel/fetch_served_testxml.sh 30 "$(mktemp -d)")
#
# The runs measured other trees than the checkout (older commits, other
# pull requests' branches), so --tests-in keeps only the tests
# backend/http/*_test.go defines.
#
# It prints nothing unless every download succeeded, so a failure leaves
# pin_shards.py without TESTLOGS rather than with some. Needs gh,
# authenticated, and the network, so no Bazel test runs it. GH_REPO overrides
# the repository (default gastownhall/beads).
#
# Usage: fetch_served_testxml.sh <runs> <dir>
set -euo pipefail
if [[ $# -ne 2 || ! "$1" =~ ^[1-9][0-9]*$ || -z "$2" ]]; then
	echo "usage: fetch_served_testxml.sh <runs> <dir>" >&2
	exit 2
fi
want="$1"
dir="$2"
repo="${GH_REPO:-gastownhall/beads}"
artifact=bazel-embedded-served-testxml
mkdir -p "$dir"

# Every run with an unexpired artifact, newest first.
candidates="$(gh api --paginate -X GET "repos/$repo/actions/artifacts" -f name="$artifact" -f per_page=100 \
	--jq '.artifacts[] | select(.expired | not) | "\(.created_at) \(.workflow_run.id)"' |
	sort -r | awk '!seen[$2]++ { print $2 }')"

got=0
out=""
for run in $candidates; do
	kind="$(gh api "repos/$repo/actions/runs/$run" \
		--jq '"\(.conclusion) \(.event) \(.path) \(.head_repository.id == .repository.id)"')"
	case "$kind" in
	"success pull_request .github/workflows/pr.yml true" | "success push .github/workflows/bazel.yml true") ;;
	*) continue ;;
	esac
	if [[ ! -d "$dir/$run" ]]; then
		rm -rf "$dir/.$run.partial"
		gh run download "$run" -R "$repo" -n "$artifact" -D "$dir/.$run.partial" >&2
		mv -f "$dir/.$run.partial" "$dir/$run"
	fi
	if [[ ! -d "$dir/$run/http_served_test" ]]; then
		echo "fetch_served_testxml: run $run's $artifact has no http_served_test; skipping it" >&2
		continue
	fi
	out="$out$dir/$run/http_served_test"$'\n'
	got=$((got + 1))
	if ((got == want)); then
		break
	fi
done
if ((got == 0)); then
	echo "fetch_served_testxml: no successful run of $repo still has $artifact" >&2
	exit 1
fi
if ((got < want)); then
	echo "fetch_served_testxml: only $got successful runs still have $artifact (asked for $want)" >&2
fi
printf '%s' "$out"
