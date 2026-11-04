#!/usr/bin/env bash
# main-ci-alert.sh: surface a red main (.github/workflows/main-ci-alert.yml;
# engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "Merge Queue").
#
# A merge group runs the fast set only: the Windows, macOS and Nix jobs and
# the Dolt-tier Bazel lanes first run on main's push, after the merge
# (pr.yml's and pr-risk.yml's push runs, bazel.yml's own push run). The queue
# lands a batch of entries as one push of its last entry, so one push run can
# cover several PRs. This runs once per completed push run on main of a
# tracked workflow:
#
#   failure, timed_out, startup_failure
#       open the ci/main-red issue with the workflow, the run, the head
#       commit, the candidate PRs and the failed jobs; while one is open,
#       comment on it instead (one issue at a time). The candidates are every
#       PR a squash subject ("... (#N)") names between this workflow's
#       previous decided push run's head and this run's head (the head
#       commit's alone when that range cannot be read). Comment on each
#       candidate too, but only when this turns the workflow red (its
#       previous decided push run was green, or there is none) and is the
#       run's first attempt: a later red of the same workflow, or a re-run
#       that went red again, updates the issue alone. A green push run of
#       the workflow newer than this one (each push keys its own concurrency
#       group, so they finish out of order) makes this red stale: it
#       notifies nobody and reports on an open issue only. A runs or jobs
#       API error leaves those parts out (head-commit candidates, notified;
#       no job list) with a warning, rather than losing the issue.
#   success
#       with the issue open: comment when this workflow's previous decided
#       push run was red, listing what is still red, and close the issue
#       once no tracked workflow's latest decided push run on main is red
#       or unknown.
#   anything else (cancelled, skipped, ...)
#       nothing: it says nothing about the tree.
#
# A decided run is one that succeeded or went red: a cancelled run (main.yml
# cancels its older runs) is passed over, page after page. This run counts as
# its workflow's latest unless the runs API lists a newer one. The runs API
# lists a workflow's newest 1000 push runs: a workflow none of them decided is
# unknown.
#
# Env: GITHUB_REPOSITORY; GH_TOKEN (issues: write, pull-requests: write,
# actions: read, contents: read); the triggering run's WORKFLOW_NAME, RUN_ID,
# RUN_URL, RUN_CREATED_AT, RUN_ATTEMPT, CONCLUSION, HEAD_SHA and
# HEAD_COMMIT_MESSAGE.

set -euo pipefail
export LC_ALL=C

repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is unset}
label="ci/main-red"
title="CI is red on main"

# The tracked workflows, by the name workflow_run matches, and their files
# (main-ci-alert.yml's workflow_run list names the same four).
names=("PR" "PR Risk" "Bazel" "Main")
files=(pr.yml pr-risk.yml bazel.yml main.yml)
tracked=$(printf '%s, ' "${names[@]}")
tracked=${tracked%, }

workflow=${WORKFLOW_NAME:?WORKFLOW_NAME is unset}
file=
for i in "${!names[@]}"; do
	if [[ "${names[$i]}" == "$workflow" ]]; then
		file=${files[$i]}
	fi
done
[[ -n "$file" ]] || {
	echo "::error::main-ci-alert.sh: '$workflow' is not a tracked workflow ($tracked)"
	exit 1
}
[[ "${RUN_ID:-}" =~ ^[0-9]+$ ]] || {
	echo "::error::main-ci-alert.sh: RUN_ID '${RUN_ID:-}' is not a run id"
	exit 1
}
[[ "${RUN_CREATED_AT:-}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || {
	echo "::error::main-ci-alert.sh: RUN_CREATED_AT '${RUN_CREATED_AT:-}' is not a UTC timestamp"
	exit 1
}
[[ "${RUN_ATTEMPT:-}" =~ ^[1-9][0-9]*$ ]] || {
	echo "::error::main-ci-alert.sh: RUN_ATTEMPT '${RUN_ATTEMPT:-}' is not a run attempt"
	exit 1
}
run_url=${RUN_URL:?RUN_URL is unset}
sha=${HEAD_SHA:?HEAD_SHA is unset}
conclusion=${CONCLUSION:?CONCLUSION is unset}

is_red() {
	case "$1" in
		failure | timed_out | startup_failure) return 0 ;;
	esac
	return 1
}

# previous_run RUNS: the newest run of decided_runs' RUNS created before this
# one, as "conclusion head_sha html_url"; nothing when there is none.
previous_run() {
	local created run_conclusion run_sha run_url
	while read -r created run_conclusion run_sha run_url; do
		if [[ -n "$created" && "$created" < "$RUN_CREATED_AT" ]]; then
			printf '%s %s %s\n' "$run_conclusion" "$run_sha" "$run_url"
			return
		fi
	done <<<"$1"
}

# newer_green RUNS: the html_url of the newest green run of decided_runs'
# RUNS created after this one; nothing when there is none.
newer_green() {
	local created run_conclusion run_sha run_url
	while read -r created run_conclusion run_sha run_url; do
		if [[ "$created" > "$RUN_CREATED_AT" && "$run_conclusion" == success ]]; then
			printf '%s\n' "$run_url"
			return
		fi
	done <<<"$1"
}

# decided_runs FILE: set runs to the workflow's decided push runs on main,
# newest first, one "created_at conclusion head_sha html_url" line each, and
# runs_listed to how far back they reach: "before" to the newest one created
# before this run, "all" when the workflow has no such run, "cut" when the
# runs API's window ended first (a filtered list stops at the newest 1000
# runs: 10 pages of 100). Cancelled runs can fill whole pages, so it reads
# page after page until one of those. Returns 1 on an API error; it checks
# each step itself, since set -e does not apply in a caller's || or if.
decided_runs() {
	local page listed count
	runs=
	runs_listed="cut"
	for ((page = 1; page <= 10; page++)); do
		# The page's run count, then its decided runs.
		listed=$(gh api "repos/$repo/actions/workflows/$1/runs?branch=main&event=push&status=completed&per_page=100&page=$page&exclude_pull_requests=true" |
			jq -r '(.workflow_runs | length), (.workflow_runs[]
				| select(.conclusion == "success" or .conclusion == "failure" or .conclusion == "timed_out" or .conclusion == "startup_failure")
				| "\(.created_at) \(.conclusion) \(.head_sha) \(.html_url)")') || return 1
		count=${listed%%$'\n'*}
		[[ "$count" =~ ^[0-9]+$ ]] || return 1
		if [[ "$listed" == *$'\n'* ]]; then
			runs+=${runs:+$'\n'}${listed#*$'\n'}
		fi
		if [[ -n "$(previous_run "$runs")" ]]; then
			runs_listed="before"
			return 0
		fi
		if ((count < 100)); then
			runs_listed="all"
			return 0
		fi
	done
}

open_issue() {
	gh issue list --repo "$repo" --label "$label" --state open --json number --jq '.[0].number // empty'
}

case "$conclusion" in
	failure | timed_out | startup_failure | success) ;;
	*)
		echo "$workflow concluded $conclusion on main at $sha: nothing to report"
		exit 0
		;;
esac

body=$(mktemp)
trap 'rm -f "$body"' EXIT
issue=$(open_issue)

if [[ "$conclusion" != success ]]; then
	# The head commit's subject, inert in Markdown: an inline code span, so
	# it can neither mention anyone nor format; backticks would end the span.
	subject=${HEAD_COMMIT_MESSAGE-}
	subject=${subject%%$'\n'*}
	subject=${subject//\`/\'}
	head_pr=
	if [[ "$subject" =~ \(#([0-9]+)\)$ ]]; then
		head_pr=${BASH_REMATCH[1]}
	fi
	# A runs API error must not lose the issue: without the previous run,
	# the candidates are the head commit's and they are notified.
	if ! decided_runs "$file"; then
		echo "::warning::main-ci-alert.sh: could not list $workflow's push runs on main; the candidates are the head commit's"
		runs=
	elif [[ "$runs_listed" == cut ]]; then
		echo "::warning::main-ci-alert.sh: none of the runs API's newest 1000 push runs of $workflow on main is a decided run before this one; the candidates are the head commit's"
	fi
	read -r prev_conclusion prev_sha prev_url <<<"$(previous_run "$runs")" || true
	# A green push run of this workflow newer than this one (push runs
	# overlap, each push keying its own concurrency group, and finish out of
	# order) means main went green after this run's head: this red is stale,
	# so it notifies nobody and reports on an open issue only. A newer red
	# run does not stand in for this one: when it decided after this run, its
	# alert took this run as its previous one and notified nobody.
	newer=$(newer_green "$runs")
	if [[ -n "$newer" && -z "$issue" ]]; then
		echo "$workflow is $conclusion on main at $sha, but a newer push run of it is green ($newer): nothing to report"
		exit 0
	fi
	# The candidates: every PR the push range names, oldest first, from this
	# workflow's previous decided push run's head to this run's (a batch
	# lands as one push; earlier runs that were cancelled or are still
	# running widen the range). The head commit's alone when there is no
	# such run, or the range cannot be read, is not a fast-forward, or is
	# longer than one page of the compare API (100 commits).
	prs=()
	range="the head commit's alone: no earlier decided push run of $workflow to compare with, or its range could not be listed"
	if [[ "${prev_sha:-}" =~ ^[0-9a-f]{40}$ ]] &&
		compare=$(gh api "repos/$repo/compare/$prev_sha...$sha?per_page=100") &&
		[[ "$(jq -r 'if .status == "ahead" and (.total_commits | type) == "number" and .total_commits <= 100 then "ok" else "no" end' <<<"$compare" 2>/dev/null)" == ok ]]; then
		while IFS= read -r pr; do
			prs+=("$pr")
		done < <(jq -r '[.commits[].commit.message | split("\n")[0] | capture("\\(#(?<n>[0-9]+)\\)$").n]
			| reduce .[] as $n ([]; if index([$n]) then . else . + [$n] end) | .[]' <<<"$compare")
		range="every PR in https://github.com/$repo/compare/${prev_sha:0:12}...${sha:0:12}, since $workflow's previous decided push run on main: $prev_url"
	elif [[ -n "$head_pr" ]]; then
		prs=("$head_pr")
	fi
	candidates=
	if ((${#prs[@]} > 0)); then
		candidates=$(printf '#%s, ' "${prs[@]}")
		candidates=${candidates%, }
	fi
	# Notify the candidates only when this turns the workflow red (and no
	# newer push run is green), on the run's first attempt (a re-run that
	# went red again reached them, or was kept from them, the first time),
	# and at most the newest ten of them (a range that long means earlier
	# runs never decided; the issue lists them all).
	notify=true
	quiet=
	if [[ -n "$newer" ]]; then
		notify=false
		quiet="A newer push run of $workflow is green already ($newer), so the candidates were not notified."
	elif [[ -n "${prev_conclusion:-}" && "$prev_conclusion" != success ]]; then
		notify=false
		quiet="$workflow was already red on main before this push ($prev_url), so the candidates were not notified."
	elif ((RUN_ATTEMPT > 1)); then
		notify=false
		quiet="This is attempt $RUN_ATTEMPT of this run, so the candidates were not notified again."
	fi
	notified=(${prs[@]+"${prs[@]}"})
	if ((${#notified[@]} > 10)); then
		notified=("${notified[@]: -10}")
	fi
	jobs_listed=true
	jobs=$(gh api --paginate "repos/$repo/actions/runs/$RUN_ID/jobs?per_page=100" |
		jq -r '.jobs[] | select(.conclusion == "failure" or .conclusion == "timed_out" or .conclusion == "cancelled") | "- [\(.name)](\(.html_url)): \(.conclusion)"') || {
		echo "::warning::main-ci-alert.sh: could not list run $RUN_ID's jobs; the issue links the run instead"
		jobs_listed=false
		jobs=
	}
	{
		printf '**%s** is %s on main at %s: %s\n\n' "$workflow" "$conclusion" "$sha" "$run_url"
		# shellcheck disable=SC2016 # Markdown backticks, not a command
		printf 'Head commit: `%s`\n' "$subject"
		if [[ -n "$candidates" ]]; then
			printf 'Candidates: %s (%s)\n' "$candidates" "$range"
		else
			printf 'Candidates: none, no PR number in the pushed commits'"'"' subjects (%s; a direct push?)\n' "$range"
		fi
		if [[ "$notify" != true ]]; then
			printf '%s\n' "$quiet"
		elif ((${#notified[@]} < ${#prs[@]})); then
			printf 'Notified the newest %s of them.\n' "${#notified[@]}"
		fi
		printf '\n'
		if [[ "$jobs_listed" != true ]]; then
			printf 'Failed jobs: the run'"'"'s jobs could not be listed; see the run.\n'
		elif [[ -n "$jobs" ]]; then
			printf 'Failed jobs:\n%s\n' "$jobs"
		else
			printf 'Failed jobs: none listed (no job ran: a startup failure, or the run was cut short).\n'
		fi
		if [[ -z "$issue" ]]; then
			printf '\nThe merge queue tested these PRs on the fast set only; the push runs are the first run of the rest (Windows, macOS, Nix, the Dolt-tier Bazel lanes) on the pushed tree. Fix forward or revert. Later reds comment here, and the issue closes itself once no tracked workflow'"'"'s latest push run on main (%s) is red (engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "Merge Queue").\n' "$tracked"
		fi
	} >"$body"
	if [[ -n "$issue" ]]; then
		gh issue comment "$issue" --repo "$repo" --body-file "$body"
	else
		# The label may not exist yet; an existing one is left as it is.
		gh label create "$label" --repo "$repo" --color b60205 \
			--description "A push run on main is red (main-ci-alert.yml)" >/dev/null 2>&1 || true
		url=$(gh issue create --repo "$repo" --title "$title" --label "$label" --body-file "$body")
		issue=${url##*/}
	fi
	echo "$workflow is $conclusion on main at $sha: reported on #$issue (candidates: ${candidates:-none}; notified: $notify)"
	if [[ "$notify" == true && -n "$candidates" ]]; then
		printf 'A push to main that includes this PR is %s in **%s** at %s: %s\n\nCandidates: %s. Tracked in #%s.\n' \
			"$conclusion" "$workflow" "$sha" "$run_url" "$candidates" "$issue" >"$body"
		for pr in "${notified[@]}"; do
			gh pr comment "$pr" --repo "$repo" --body-file "$body" ||
				echo "::warning::main-ci-alert.sh: could not comment on #$pr; #$issue has the report"
		done
	fi
	exit 0
fi

# success
if [[ -z "$issue" ]]; then
	echo "$workflow is green on main at $sha; no ci/main-red issue is open"
	exit 0
fi
# This workflow's previous decided run, and every tracked workflow's latest
# (this run when the API lists nothing newer of its workflow). A runs API
# error ends the script and leaves the issue open. A workflow with no
# decided run in the runs API's window is unknown, not green: it keeps the
# issue open too.
previous=
red=()
unknown=()
for i in "${!names[@]}"; do
	decided_runs "${files[$i]}"
	if [[ "${names[$i]}" == "$workflow" ]]; then
		read -r previous _ <<<"$(previous_run "$runs")" || true
	fi
	read -r created latest_conclusion _ latest_url <<<"${runs%%$'\n'*}" || true
	if [[ "${names[$i]}" == "$workflow" && ! "${created:-}" > "$RUN_CREATED_AT" ]]; then
		continue
	fi
	if is_red "${latest_conclusion:-}"; then
		red+=("${names[$i]} ($latest_url)")
	elif [[ -z "$runs" && "$runs_listed" == cut ]]; then
		unknown+=("${names[$i]}")
	fi
done
if is_red "${previous:-}"; then
	{
		printf '**%s** is green again on main at %s: %s\n' "$workflow" "$sha" "$run_url"
		if ((${#red[@]} > 0)); then
			still=$(printf '%s, ' "${red[@]}")
			printf '\nStill red: %s\n' "${still%, }"
		fi
		if ((${#unknown[@]} > 0)); then
			still=$(printf '%s, ' "${unknown[@]}")
			printf '\nNot known: %s (no decided push run on main among the newest 1000 the runs API lists)\n' "${still%, }"
		fi
	} >"$body"
	gh issue comment "$issue" --repo "$repo" --body-file "$body"
fi
if ((${#red[@]} == 0 && ${#unknown[@]} == 0)); then
	gh issue close "$issue" --repo "$repo" --comment "No tracked workflow's latest push run on main ($tracked) is red: closing. $workflow is green at $sha: $run_url"
	echo "closed #$issue"
else
	echo "#$issue stays open: red ${red[*]:-none}; not known ${unknown[*]:-none}"
fi
