#!/usr/bin/env bash
# fork.sh — maintain this fork as upstream's default branch plus the fork delta.
#
#   scripts/fork/fork.sh sync                   merge upstream's default branch into
#                                               the integration branch and open the PR
#   scripts/fork/fork.sh release                tag the merged integration head as the
#                                               next stable fork release (<upstream-version>-dc.N)
#   scripts/fork/fork.sh pair [<bd-fork-tag>]   link the bd fork release this build
#                                               pairs with (repos that link beads only)
#
# Every fact is derived, never configured: the upstream repository, its default
# branch and the integration branch come from GitHub (parent, default branches),
# the base is the newest upstream release already merged into the integration
# branch, and the fork label (fd, fc, ...) comes from this fork's own release
# tags. FORK.md documents the full cycle.
set -euo pipefail

die() {
	echo "fork.sh: $*" >&2
	exit 1
}

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

# Resolve this fork from origin explicitly: with an "upstream" remote present,
# a bare `gh repo view` may pick the parent repository instead.
repo_json=$(gh repo view "$(git remote get-url origin)" --json nameWithOwner,parent,defaultBranchRef)
self=$(jq -r '.nameWithOwner' <<<"$repo_json")
upstream=$(jq -r 'if .parent then "\(.parent.owner.login)/\(.parent.name)" else "" end' <<<"$repo_json")
integration=$(jq -r '.defaultBranchRef.name' <<<"$repo_json")
[[ -n "$upstream" ]] || die "$self is not a fork on GitHub"
upstream_url="https://github.com/${upstream}.git"

release_re='^v[0-9]+\.[0-9]+\.[0-9]+$'

fetch_integration() {
	git fetch --no-tags origin "refs/heads/${integration}:refs/remotes/origin/${integration}"
}

fetch_upstream_tag() {
	git fetch --no-tags "$upstream_url" "refs/tags/$1:refs/tags/$1"
}

# Newest upstream release tag that is already an ancestor of the integration head.
current_base() {
	local tag
	while read -r tag; do
		[[ "$tag" =~ $release_re ]] || continue
		git rev-parse -q --verify "refs/tags/${tag}^{commit}" >/dev/null || fetch_upstream_tag "$tag"
		if git merge-base --is-ancestor "$tag" "origin/${integration}"; then
			echo "$tag"
			return 0
		fi
	done < <(gh release list -R "$upstream" --exclude-pre-releases --limit 100 --json tagName --jq '.[].tagName' | sort -rV)
	die "no upstream release of $upstream is an ancestor of origin/${integration}"
}

cmd_sync() {
	fetch_integration
	local upstream_branch target base short
	upstream_branch=$(gh repo view "$upstream" --json defaultBranchRef --jq .defaultBranchRef.name)
	git fetch --no-tags "$upstream_url" "refs/heads/${upstream_branch}:refs/remotes/upstream-sync/${upstream_branch}"
	target=$(git rev-parse "refs/remotes/upstream-sync/${upstream_branch}")
	# The fork delta is everything since the upstream commit last merged in.
	base=$(git merge-base "$target" "origin/${integration}")
	if [[ "$target" == "$base" ]]; then
		echo "fork.sh: already at upstream ${upstream_branch} ${target}"
		return 0
	fi
	short=$(git rev-parse --short=9 "$target")

	local main_root lane worktree
	main_root=$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")
	lane="lane/${integration}-${upstream_branch}-${short}"
	worktree="$(dirname "$main_root")/worktrees/$(basename "$main_root")/lane-${integration}-${upstream_branch}-${short}"

	git worktree add -b "$lane" "$worktree" "$target"
	git -C "$worktree" merge --no-ff "origin/${integration}"
	if grep -q '^BD_FORK_VERSION=' "$worktree/deps.env" 2>/dev/null; then
		(cd "$worktree" && "$worktree/scripts/fork/fork.sh" pair)
		git -C "$worktree" add go.mod go.sum deps.env
		git -C "$worktree" commit -m "refresh the bd fork pairing on upstream ${upstream_branch} ${short}"
	fi
	git -C "$worktree" push -u origin "$lane"
	gh pr create -R "$self" --base "$integration" --head "$lane" --draft \
		--title "Sync ${integration} with upstream ${upstream_branch} ${short}" \
		--body "Upstream ${upstream_branch} ${target} merged with the fork integration lane (\`git diff ${base} origin/${integration}\`). Created by scripts/fork/fork.sh sync; see FORK.md."
}

cmd_release() {
	fetch_integration
	local base label head n tag
	base=$(current_base)
	label=dc
	head=$(git rev-parse "origin/${integration}")
	n=0
	while read -r tag; do
		[[ "$tag" =~ ^${base}-${label}\.([0-9]+)$ ]] || continue
		if ((BASH_REMATCH[1] > n)); then
			n=${BASH_REMATCH[1]}
		fi
	done < <(git ls-remote --tags --refs origin "refs/tags/${base}-${label}.*" | sed 's#.*refs/tags/##')
	tag="${base}-${label}.$((n + 1))"
	local upstream_branch synced
	upstream_branch=$(gh repo view "$upstream" --json defaultBranchRef --jq .defaultBranchRef.name)
	git fetch --no-tags "$upstream_url" "refs/heads/${upstream_branch}:refs/remotes/upstream-sync/${upstream_branch}"
	synced=$(git rev-parse "refs/remotes/upstream-sync/${upstream_branch}")
	git merge-base --is-ancestor "$synced" "$head" || die "integration does not contain upstream ${upstream_branch} $synced"
	echo "fork.sh: ${self} ${integration} ${head} = upstream ${upstream_branch} ${synced} + fork delta:"
	git diff --stat "$synced" "$head"
	git tag -a "$tag" -m "Fork release ${tag}: upstream ${upstream_branch} ${synced} (${base} line) + fork delta" "$head"
	git push origin "refs/tags/${tag}"
	echo "fork.sh: pushed ${tag}; the release workflow publishes it"
}

cmd_pair() {
	[[ -f deps.env ]] || die "pair needs deps.env"
	local module="github.com/steveyegge/beads" required fork_repo tag
	required=$(go list -m -f '{{.Version}}' "$module") || die "go.mod does not require $module"
	# The bd fork is this fork owner's fork of the repository the module lives in.
	local module_repo owner
	module_repo=$(gh api "repos/${module#github.com/}" --jq .full_name)
	owner=${self%%/*}
	fork_repo=$(gh api "repos/${module_repo}/forks" --paginate --jq ".[] | select(.owner.login == \"${owner}\") | .full_name")
	[[ -n "$fork_repo" ]] || die "$owner has no fork of $module_repo"
	tag="${1:-}"
	if [[ -z "$tag" ]]; then
		tag=$(gh release list -R "$fork_repo" --limit 100 --json tagName --jq '.[].tagName' | grep -E "^${required}-dc\.[0-9]+$" | sort -rV | head -n 1)
	fi
	[[ "$tag" =~ ^${required}-dc\.[0-9]+$ ]] || die "bd fork tag must be ${required}-dc.N (the linked version), got: ${tag:-none}"
	go mod edit -replace "${module}=github.com/${fork_repo}@${tag}"
	go mod tidy
	local key
	for key in BD_FORK_MODULE BD_FORK_VERSION; do
		sed -i "/^${key}=/d" deps.env
	done
	printf 'BD_FORK_MODULE=github.com/%s\nBD_FORK_VERSION=%s\n' "$fork_repo" "$tag" >>deps.env
	go list -m "$module"
	make check-gomod-replace
}

case "${1:-}" in
sync) cmd_sync ;;
release) cmd_release ;;
pair) shift && cmd_pair "$@" ;;
*) die "usage: fork.sh sync | release | pair [<bd-fork-tag>]" ;;
esac
