#!/usr/bin/env bash
# Agent PR preflight for beads.
#
# This script is read-only. It turns the contributor-protection policy into a
# concrete checklist before an agent reviews, closes, merges, or supersedes a PR.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  scripts/pr-preflight.sh <pr-number-or-url> [--repo owner/name]
  scripts/pr-preflight.sh --search "<topic keywords>" [--repo owner/name]

Before changing or merging an existing PR:
  scripts/pr-preflight.sh 123 --repo gastownhall/beads

Before implementing a feature/fix or opening a replacement PR:
  scripts/pr-preflight.sh --search "dependency cycle detection" --repo gastownhall/beads

What this checks:
  - whether an existing PR is external/cross-repository contributor work
  - draft, review, mergeability, and check status
  - risky diff signals: .beads data, missing tests for code changes, large diffs
  - contributor-protection next steps and attribution reminders

The script does not replace code review or local validation.
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 2
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

repo_from_url() {
  sed -E \
    -e 's#^git@github.com:##' \
    -e 's#^https://github.com/##' \
    -e 's#^ssh://git@github.com/##' \
    -e 's#\.git$##' \
    <<<"$1"
}

default_repo() {
  local url
  if url=$(git remote get-url upstream 2>/dev/null); then
    repo_from_url "$url"
    return
  fi
  if url=$(git remote get-url origin 2>/dev/null); then
    repo_from_url "$url"
    return
  fi
  gh repo view --json nameWithOwner --jq .nameWithOwner
}

repo=""
pr=""
search=""

while (($#)); do
  case "$1" in
    --repo)
      [[ $# -ge 2 ]] || die "--repo requires owner/name"
      repo="$2"
      shift 2
      ;;
    --search)
      [[ $# -ge 2 ]] || die "--search requires keywords"
      search="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*)
      die "unknown option: $1"
      ;;
    *)
      [[ -z "$pr" ]] || die "only one PR may be provided"
      pr="$1"
      shift
      ;;
  esac
done

need gh
need git
need jq

repo="${repo:-$(default_repo)}"
[[ -n "$repo" ]] || die "could not determine GitHub repo; pass --repo owner/name"

if [[ -n "$search" ]]; then
  printf 'PR topic preflight for %s\n' "$repo"
  printf 'Search: %s\n\n' "$search"

  results=$(gh pr list \
    --repo "$repo" \
    --state open \
    --search "$search" \
    --json number,title,author,headRefName,isCrossRepository,url,updatedAt)

  count=$(jq 'length' <<<"$results")
  if [[ "$count" -eq 0 ]]; then
    printf '[pass] No open PRs matched this search.\n'
    printf '[next] Use broader keywords if the topic is ambiguous before opening a new PR.\n'
    exit 0
  fi

  printf '[block] Found %s open PR(s). Review relevant contributor work before building separately.\n\n' "$count"
  jq -r '.[] |
    "- PR #\(.number): \(.title)\n  author: \(.author.login)  external: \(.isCrossRepository)  branch: \(.headRefName)\n  url: \(.url)"' \
    <<<"$results"
  printf '\n[next] Run scripts/pr-preflight.sh <number> --repo %s for each relevant PR.\n' "$repo"
  exit 1
fi

[[ -n "$pr" ]] || { usage; exit 2; }

json=$(gh pr view "$pr" \
  --repo "$repo" \
  --json number,title,author,url,baseRefName,headRefName,headRepositoryOwner,isCrossRepository,isDraft,maintainerCanModify,mergeStateStatus,mergeable,reviewDecision,changedFiles,additions,deletions,files,statusCheckRollup,closingIssuesReferences,latestReviews)

number=$(jq -r .number <<<"$json")
title=$(jq -r .title <<<"$json")
author=$(jq -r .author.login <<<"$json")
url=$(jq -r .url <<<"$json")
external=$(jq -r .isCrossRepository <<<"$json")
draft=$(jq -r .isDraft <<<"$json")
review=$(jq -r 'if (.reviewDecision // "") == "" then "REVIEW_REQUIRED" else .reviewDecision end' <<<"$json")
merge_state=$(jq -r '.mergeStateStatus // "UNKNOWN"' <<<"$json")
mergeable=$(jq -r '.mergeable // "UNKNOWN"' <<<"$json")
files=$(jq -r .changedFiles <<<"$json")
additions=$(jq -r .additions <<<"$json")
deletions=$(jq -r .deletions <<<"$json")
maintainer_can_modify=$(jq -r '.maintainerCanModify // false' <<<"$json")

status=0

block() {
  printf '[block] %s\n' "$*"
  status=1
}

warn() {
  printf '[warn] %s\n' "$*"
}

pass() {
  printf '[pass] %s\n' "$*"
}

printf 'PR preflight for %s#%s\n' "$repo" "$number"
printf 'Title: %s\n' "$title"
printf 'Author: %s\n' "$author"
printf 'URL: %s\n' "$url"
printf 'Base/head: %s <- %s\n' "$(jq -r .baseRefName <<<"$json")" "$(jq -r .headRefName <<<"$json")"
printf 'Diff: %s files, +%s/-%s\n\n' "$files" "$additions" "$deletions"

if [[ "$external" == "true" ]]; then
  warn "External contributor PR. Do not rewrite, close, or supersede silently."
  if [[ "$maintainer_can_modify" == "true" ]]; then
    pass "Maintainers can push fixes to this PR branch."
  else
    warn "Maintainers cannot push to this PR branch; coordinate in comments or use a credited follow-up branch."
  fi
else
  pass "PR is not cross-repository contributor work."
fi

if [[ "$draft" == "true" ]]; then
  block "PR is draft."
fi

case "$review" in
  APPROVED)
    pass "Review decision is APPROVED."
    ;;
  CHANGES_REQUESTED)
    block "Review decision is CHANGES_REQUESTED."
    ;;
  *)
    warn "Review decision is ${review}."
    ;;
esac

case "$merge_state" in
  CLEAN|HAS_HOOKS)
    pass "Merge state is ${merge_state}."
    ;;
  BLOCKED|DIRTY|UNKNOWN|UNSTABLE)
    block "Merge state is ${merge_state}."
    ;;
  *)
    warn "Merge state is ${merge_state}; verify in GitHub before merging."
    ;;
esac

if [[ "$mergeable" == "CONFLICTING" ]]; then
  block "GitHub reports merge conflicts."
fi

failed_checks=$(jq '[.statusCheckRollup[]? | select(
  ((.conclusion // "") | test("FAILURE|CANCELLED|TIMED_OUT|ACTION_REQUIRED")) or
  ((.state // "") | test("ERROR|FAILURE"))
)] | length' <<<"$json")
pending_checks=$(jq '[.statusCheckRollup[]? | select(
  if (.status? // null) != null then
    .status != "COMPLETED"
  elif (.state? // null) != null then
    (.state | test("^(EXPECTED|PENDING)$"))
  else
    true
  end
)] | length' <<<"$json")
if [[ "$failed_checks" -gt 0 ]]; then
  block "$failed_checks status check(s) failed or require action."
elif [[ "$pending_checks" -gt 0 ]]; then
  block "$pending_checks status check(s) are still pending."
else
  pass "No failed or pending status checks reported."
fi

beads_files=$(jq '[.files[]?.path | select(startswith(".beads/"))] | length' <<<"$json")
if [[ "$beads_files" -gt 0 ]]; then
  block "PR changes .beads/ data; contributor PRs must not include planning database changes."
fi

code_files=$(jq '[.files[]?.path | select(test("\\.(go|py|sh)$"))] | length' <<<"$json")
test_files=$(jq '[.files[]?.path | select(test("(^|/)(test|tests)/|_test\\.go$|\\.bats$"))] | length' <<<"$json")
if [[ "$code_files" -gt 0 && "$test_files" -eq 0 ]]; then
  warn "Code changed but no obvious test files changed."
fi

if [[ "$files" -gt 30 || "$additions" -gt 1000 ]]; then
  warn "Large PR; verify scope is one issue and one PR."
fi

issue_count=$(jq '[.closingIssuesReferences[]?] | length' <<<"$json")
if [[ "$issue_count" -eq 0 ]]; then
  warn "No closing issue reference found."
else
  pass "PR references $issue_count closing issue(s)."
fi

printf '\nChanged files:\n'
jq -r '.files[]?.path | "  - \(.)"' <<<"$json"

printf '\nRequired agent behavior:\n'
printf '  - Review contributor work first; build on it when relevant.\n'
printf '  - Preserve contributor tests unless they are wrong.\n'
printf '  - Preserve attribution with existing commits or Co-authored-by trailers.\n'
printf '  - If a rewrite is unavoidable, explain why on PR #%s and credit design/tests.\n' "$number"
printf '  - Run local validation before merge, normally: make test\n'

if [[ "$status" -ne 0 ]]; then
  printf '\nResult: BLOCKED for autonomous merge/close/replacement until addressed.\n'
else
  printf '\nResult: preflight passed. Continue with code review and local validation.\n'
fi

exit "$status"
