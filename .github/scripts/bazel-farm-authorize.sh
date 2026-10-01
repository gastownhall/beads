#!/usr/bin/env bash
# bazel-farm.yml's authorize job: may this pull_request_target run hand a
# fork PR's head to bazel.yml with the RBE secrets? Runs from a checkout of
# the base branch (the run's own commit), never the PR; reads event facts
# from the environment only (the workflow never interpolates them into a
# script). Writes allowed=true|false to $GITHUB_OUTPUT and the reason to the
# log and step summary. Exits non-zero only on a broken invocation.
#
# Allowed only when all hold:
#   EVENT_NAME is pull_request_target and ACTION is opened or synchronize
#     (reopened and ready_for_review would re-test a head no listed user
#     necessarily pushed: a fork collaborator's push while the PR was closed
#     or a draft);
#   BASE_REPO is REPOSITORY and HEAD_REPO is another repository (a fork);
#   HEAD_OWNER_ID (the head repository owner's numeric id) is PR_AUTHOR_ID:
#     the PR comes from the author's own fork, not from a third party's fork
#     whose owner could push between the author's review and "Create";
#   BASE_REF is DEFAULT_BRANCH;
#   HEAD_SHA is a full 40-hex commit id (what bazel.yml checks out);
#   PR_AUTHOR_ID and SENDER_ID (numeric user ids) are both on ALLOWLIST.
#     SENDER is who triggered this event: the pusher on synchronize, so a
#     collaborator on a listed author's fork cannot push code that runs with
#     secrets. Ids, not logins: a renamed account's old login can be
#     registered by anyone. Logins (PR_AUTHOR, SENDER) are for the log only.
#
# ALLOWLIST: one numeric user id per line; # starts a comment (the login,
# for humans). Any other entry is a broken allowlist and fails the job.
#
# Inputs (environment): ALLOWLIST (file path), EVENT_NAME, ACTION,
# REPOSITORY, BASE_REPO, HEAD_REPO, HEAD_OWNER_ID, BASE_REF, DEFAULT_BRANCH,
# HEAD_SHA, PR_AUTHOR, PR_AUTHOR_ID, SENDER, SENDER_ID, GITHUB_OUTPUT;
# GITHUB_STEP_SUMMARY optional.

set -euo pipefail
# Byte semantics for the [A-Za-z0-9] / [0-9] classes and lower(): in some
# UTF-8 locales they match or fold non-ASCII (e.g. the Kelvin sign to k).
export LC_ALL=C

# ASCII lowercase. Not ${v,,}: that is bash >= 4, and macOS ships bash 3.2.
# shellcheck disable=SC2018,SC2019 # ASCII only, on purpose (logins, repo names)
lower() {
    printf '%s' "${1:-}" | tr 'A-Z' 'a-z'
}

: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is not set}"
allowlist="${ALLOWLIST:?ALLOWLIST is not set}"
if [[ ! -f "$allowlist" ]]; then
    echo "::error::allowlist $allowlist is missing" >&2
    exit 1
fi

decide() {
    allowed=false
    reason="$1"
}

# A login: GitHub's charset, lowercased. Anything else never matches.
login() {
    local v="${1:-}"
    if [[ "$v" =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,38})$ ]]; then
        lower "$v"
    fi
}

# A numeric GitHub user id; anything else is not one.
is_id() {
    [[ "${1:-}" =~ ^[1-9][0-9]{0,19}$ ]]
}

ids=()
while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%%#*}"
    line="${line//[[:space:]]/}"
    [[ -n "$line" ]] || continue
    if ! is_id "$line"; then
        echo "::error::allowlist $allowlist has an entry that is not a numeric user id" >&2
        exit 1
    fi
    ids+=("$line")
done < "$allowlist"

listed() {
    local want="${1:-}" id
    is_id "$want" || return 1
    for id in "${ids[@]}"; do
        if [[ "$id" == "$want" ]]; then
            return 0
        fi
    done
    return 1
}

allowed=true
reason="author and sender are on the allowlist"
author="${PR_AUTHOR:-}"
sender="${SENDER:-}"
if [[ "${EVENT_NAME:-}" != pull_request_target ]]; then
    decide "event is not pull_request_target"
elif ! [[ "${ACTION:-}" =~ ^(opened|synchronize)$ ]]; then
    decide "action is not opened or synchronize"
elif [[ -z "${REPOSITORY:-}" || "${BASE_REPO:-}" != "$REPOSITORY" ]]; then
    decide "PR base repository is not this repository"
elif [[ -z "${HEAD_REPO:-}" || "$(lower "$HEAD_REPO")" == "$(lower "${REPOSITORY:-}")" ]]; then
    decide "PR head is not a fork (same-repo PRs use pr.yml's remote run)"
elif ! is_id "${HEAD_OWNER_ID:-}" || [[ "${HEAD_OWNER_ID}" != "${PR_AUTHOR_ID:-}" ]]; then
    decide "PR head is not the author's own fork"
elif [[ -z "${DEFAULT_BRANCH:-}" || "${BASE_REF:-}" != "$DEFAULT_BRANCH" ]]; then
    decide "PR does not target the default branch"
elif ! [[ "${HEAD_SHA:-}" =~ ^[0-9a-f]{40}$ ]]; then
    decide "head SHA is not a full commit id"
elif ! listed "${PR_AUTHOR_ID:-}"; then
    decide "PR author is not on the allowlist"
elif ! listed "${SENDER_ID:-}"; then
    decide "the user who triggered this run is not on the allowlist"
fi

# Logins are printed only after the charset check (log-injection safe).
shown_author="$(login "$author")"
shown_sender="$(login "$sender")"
echo "allowed=$allowed" >> "$GITHUB_OUTPUT"
msg="Bazel farm: allowed=$allowed ($reason; author ${shown_author:-<invalid>}, sender ${shown_sender:-<invalid>})"
echo "$msg"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    echo "$msg" >> "$GITHUB_STEP_SUMMARY"
fi
