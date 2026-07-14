#!/usr/bin/env bash
# docs-autofix-push.sh - apply a CI-generated CLI docs regeneration patch to a
# PR branch, or fall back to an instructive PR comment when pushing is not
# possible.
#
# Runs on the PRIVILEGED side of the docs-autofix workflow_run pipeline: the
# checkout is always the base repository's default branch (trusted code), and
# the patch produced by the unprivileged PR build is treated as UNTRUSTED DATA.
# Confinement is layered: the path allowlist below pins WHICH files a patch may
# name (anchored regexes, single path segment, no traversal, no symlink modes),
# and `git apply --index` supplies the underlying escape guards (rejects `..`
# paths, absolute paths, and writes through in-patch symlinks). A hostile patch
# can therefore at most rewrite generated doc files on its own PR branch.
#
# Inputs (environment):
#   BASE_REPO    base "owner/name" (e.g. gastownhall/beads)
#   HEAD_REPO    PR head "owner/name" (same as BASE_REPO for branch PRs;
#                may be empty if the head fork was deleted)
#   HEAD_BRANCH  PR head branch name
#   HEAD_SHA     head commit the failing run was built from
#   PATCH_FILE   path to the downloaded cli-docs-freshness.patch
#   RUN_ID       workflow run id that produced the patch (for comment text)
#   RUN_URL      html url of that run (for commit/comment provenance)
#   GH_TOKEN     token for gh api calls (PR lookup, comments) - needs the
#                workflow's pull-requests:write; never the PAT
#   PUSH_TOKEN   token for the git push only (optional; defaults to GH_TOKEN),
#                so a dedicated DOCS_AUTOFIX_TOKEN needs contents:write only
#   AUTOFIX_TOKEN_KIND  "pat" when a dedicated push token is in use, "default"
#                       for the workflow's GITHUB_TOKEN (retrigger caveat)
#
# Exit 0 on every non-actionable outcome (PR closed, head moved, no patch);
# exit 1 only on genuine errors so the workflow surfaces them.

set -euo pipefail

if [ -z "${HEAD_REPO:-}" ] || [ -z "${HEAD_BRANCH:-}" ]; then
    echo "Head repository/branch unavailable (deleted fork?); nothing to do."
    exit 0
fi
: "${BASE_REPO:?}" "${HEAD_SHA:?}"
: "${PATCH_FILE:?}" "${RUN_ID:?}" "${RUN_URL:?}" "${GH_TOKEN:?}"
AUTOFIX_TOKEN_KIND="${AUTOFIX_TOKEN_KIND:-default}"
PUSH_TOKEN="${PUSH_TOKEN:-$GH_TOKEN}"
PATCH_FILE="$(readlink -f "$PATCH_FILE")"

COMMENT_MARKER="<!-- cli-docs-autofix -->"
AUTOFIX_SUBJECT="docs: auto-regenerate CLI reference"

# Files the doc generators may write - keep in sync with GEN_PATHSPECS in
# scripts/check-cli-docs-drift.sh. Anchored, single path segment where a
# wildcard appears, conservative filename charset: no traversal, no nesting,
# no metacharacters can slip through.
path_allowed() {
    case "$1" in *..*) return 1 ;; esac
    [[ "$1" == "docs/CLI_REFERENCE.md" ]] && return 0
    [[ "$1" == "docs/docs.json" ]] && return 0
    [[ "$1" =~ ^docs/cli-reference/[A-Za-z0-9_.-]+\.md$ ]] && return 0
    return 1
}

if [ ! -s "$PATCH_FILE" ]; then
    echo "No patch content; nothing to do."
    exit 0
fi

# --- Validate the untrusted patch --------------------------------------------

# Generated docs are regular files; refuse any symlink (120000) mode before
# git apply can materialize one at an allowlisted path.
if grep -qE '^(new|old) file mode 120000$|^new mode 120000$' "$PATCH_FILE"; then
    echo "REFUSED: patch introduces a symlink mode."
    exit 1
fi

# --numstat prints "added<TAB>deleted<TAB>path"; renames appear as
# "old => new" forms, which the allowlist match rejects.
BAD_PATHS=""
while IFS=$'\t' read -r _ _ path; do
    [ -n "$path" ] || continue
    if ! path_allowed "$path"; then
        BAD_PATHS="${BAD_PATHS}${path}\n"
    fi
done < <(git apply --numstat "$PATCH_FILE")

if [ -n "$BAD_PATHS" ]; then
    printf 'REFUSED: patch touches paths outside the generated-docs allowlist:\n%b' "$BAD_PATHS"
    exit 1
fi

# --- Resolve the PR and confirm the patch is still current -------------------

# List-and-filter client side: branch names with URL metacharacters would
# corrupt a ?head= query string, and jq --arg needs no encoding.
PULLS_JSON="$(gh api --paginate "repos/$BASE_REPO/pulls?state=open&per_page=100")"
PR_MATCH="$(printf '%s' "$PULLS_JSON" | jq -r -s --arg repo "$HEAD_REPO" --arg branch "$HEAD_BRANCH" \
    'add | [ .[] | select(.head.ref == $branch and (.head.repo.full_name // "") == $repo) ]
     | .[0] | if . == null then "" else "\(.number) \(.head.sha)" end')"
PR_NUMBER="${PR_MATCH%% *}"
PR_HEAD_NOW="${PR_MATCH##* }"

if [ -z "$PR_NUMBER" ]; then
    echo "No open PR for $HEAD_REPO:$HEAD_BRANCH; nothing to do."
    exit 0
fi
if [ "$PR_HEAD_NOW" != "$HEAD_SHA" ]; then
    echo "PR #$PR_NUMBER head moved ($HEAD_SHA -> $PR_HEAD_NOW); a newer run owns the fix."
    exit 0
fi

# Circuit breaker: if the failing head is already one of our autofix commits,
# regeneration is not converging (or something keeps dirtying the docs) -
# stacking more bot commits would loop. Fail safe to the recipe comment.
HEAD_MSG="$(gh api "repos/$BASE_REPO/commits/$HEAD_SHA" --jq '.commit.message' 2>/dev/null || true)"
case "$HEAD_MSG" in
    "$AUTOFIX_SUBJECT"*)
        echo "Head $HEAD_SHA is already an autofix commit; refusing to stack another."
        NONCONVERGENT=1
        ;;
    *) NONCONVERGENT=0 ;;
esac

post_or_update_comment() {
    local body_file="$1"
    # Capture fully before taking the first id: head -1 on a live --paginate
    # stream SIGPIPEs gh under pipefail.
    local ids existing
    ids="$(gh api --paginate "repos/$BASE_REPO/issues/$PR_NUMBER/comments" \
        --jq ".[] | select(.body | startswith(\"$COMMENT_MARKER\")) | .id")"
    existing="$(printf '%s\n' "$ids" | head -1)"
    if [ -n "$existing" ]; then
        gh api --method PATCH "repos/$BASE_REPO/issues/comments/$existing" \
            -F body=@"$body_file" >/dev/null
        echo "Updated autofix comment $existing on PR #$PR_NUMBER."
    else
        gh api --method POST "repos/$BASE_REPO/issues/$PR_NUMBER/comments" \
            -F body=@"$body_file" >/dev/null
        echo "Posted autofix comment on PR #$PR_NUMBER."
    fi
}

comment_fallback() {
    local reason="$1"
    local body
    body="$(mktemp)"
    cat > "$body" <<EOF
$COMMENT_MARKER
**Generated CLI docs are stale on this PR** (${reason}).

CI already produced the exact fix with its canonical pinned build. Apply it locally:

\`\`\`bash
gh run download $RUN_ID -R $BASE_REPO -n cli-docs-freshness-patch
git apply --index cli-docs-freshness.patch
git commit -m "docs: regenerate CLI reference"
git push
\`\`\`

Or regenerate from scratch: \`CGO_ENABLED=0 go build -tags gms_pure_go -o ./bd-docs ./cmd/bd/ && ./scripts/generate-cli-docs.sh ./bd-docs && rm ./bd-docs\`

_Automated by the [docs-autofix workflow]($RUN_URL); this comment is updated in place on each failing run._
EOF
    post_or_update_comment "$body"
    rm -f "$body"
}

if [ "$NONCONVERGENT" = "1" ]; then
    comment_fallback "an earlier auto-fix did not converge - please regenerate manually"
    exit 0
fi

# --- Fork PRs: no token we hold can push there, leave the recipe --------------

if [ "$HEAD_REPO" != "$BASE_REPO" ]; then
    comment_fallback "fork PR - CI cannot push the fix to your branch"
    exit 0
fi

# --- Same-repo PRs: push the regen commit -------------------------------------

# Keep the token out of on-disk .git/config: pass the auth header per command.
# Uses PUSH_TOKEN (the optional contents:write PAT), not the API token.
AUTH_CONFIG="http.https://github.com/.extraheader=AUTHORIZATION: basic $(printf 'x-access-token:%s' "$PUSH_TOKEN" | base64 -w0)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

git -c "$AUTH_CONFIG" clone --quiet --no-checkout --filter=blob:none \
    "https://github.com/${BASE_REPO}.git" "$WORK/repo"
cd "$WORK/repo"
git -c "$AUTH_CONFIG" fetch --quiet origin "$HEAD_BRANCH"
git checkout --quiet "$HEAD_SHA" 2>/dev/null || {
    echo "Head $HEAD_SHA no longer reachable on $BASE_REPO/$HEAD_BRANCH; skipping."
    exit 0
}

if ! git apply --index "$PATCH_FILE" 2>/dev/null; then
    cd - >/dev/null
    comment_fallback "the regeneration patch no longer applies cleanly"
    exit 0
fi

git -c user.name="github-actions[bot]" \
    -c user.email="41898282+github-actions[bot]@users.noreply.github.com" \
    commit --quiet -m "$AUTOFIX_SUBJECT

Applied from the cli-docs-freshness-patch artifact of $RUN_URL
(generated with CI's canonical pinned build). See
scripts/check-cli-docs-drift.sh for how drift is attributed."

if ! git -c "$AUTH_CONFIG" push --quiet origin "HEAD:refs/heads/$HEAD_BRANCH"; then
    cd - >/dev/null
    comment_fallback "pushing the fix to $HEAD_BRANCH failed (branch protection or a concurrent push)"
    exit 0
fi
NEW_SHA="$(git rev-parse HEAD)"
cd - >/dev/null

echo "Pushed regen commit $NEW_SHA to $BASE_REPO/$HEAD_BRANCH."

BODY="$(mktemp)"
cat > "$BODY" <<EOF
$COMMENT_MARKER
**Pushed \`${NEW_SHA:0:12}\` regenerating the stale CLI docs**, from the canonical-build patch of the [failing run]($RUN_URL).
EOF
if [ "$AUTOFIX_TOKEN_KIND" = "default" ]; then
    cat >> "$BODY" <<'EOF'

Note: this commit was pushed with the default workflow token, which does **not** retrigger PR checks - re-run them (or push any commit) to refresh the gate. Configuring a `DOCS_AUTOFIX_TOKEN` repo secret removes this step.
EOF
fi
post_or_update_comment "$BODY"
rm -f "$BODY"
