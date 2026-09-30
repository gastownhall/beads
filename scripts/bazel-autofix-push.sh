#!/usr/bin/env bash
# bazel-autofix-push.sh - apply a CI-generated `make bazel-sync` patch
# (BUILD.bazel / MODULE.bazel / MODULE.bazel.lock) to a PR branch, or fall
# back to an instructive PR comment when pushing is not possible.
#
# Runs on the PRIVILEGED side of the bazel-autofix workflow_run pipeline: the
# checkout is always the base repository's default branch (trusted code), and
# the patch produced by the unprivileged PR build (scripts/ci/bazel-sync-patch.sh
# in bazel.yml) is treated as UNTRUSTED DATA. Confinement is layered:
#   * the path allowlist below pins WHICH files a patch may name (anchored
#     regex, no hidden or `..` segments, nothing under third_party/);
#   * any mode line, symlink, rename/copy or binary hunk is refused, so only
#     regular 100644 files are created, edited or deleted;
#   * the patch is applied to an index only (`git read-tree` + `git apply
#     --cached` in a bare clone): the PR tree is never written to disk, and
#     git's own guards reject `..`/absolute paths and in-patch symlinks;
#   * the staged result is re-checked against the same allowlist;
#   * only files in packages the PR itself changed are pushed, so drift that
#     main introduced is never pushed onto an unrelated PR.
# A hostile patch can therefore at most rewrite Bazel build files on its own
# PR branch, which its author could push there anyway.
#
# Usage:
#   bazel-autofix-push.sh             validate, then push or comment (env below)
#   bazel-autofix-push.sh --check P   validate patch P only; exit 0 if acceptable
#
# Inputs (environment):
#   BASE_REPO    base "owner/name" (e.g. gastownhall/beads)
#   HEAD_REPO    PR head "owner/name" (same as BASE_REPO for branch PRs;
#                may be empty if the head fork was deleted)
#   HEAD_BRANCH  PR head branch name
#   HEAD_SHA     head commit the failing run was built from
#   PATCH_FILE   path to the downloaded bazel-sync.patch
#   META_FILE    optional bazel-sync-meta.txt from the same artifact; untrusted,
#                used only to skip a patch whose head_sha disagrees with HEAD_SHA
#   RUN_ID       workflow run id that produced the patch (for comment text)
#   RUN_URL      html url of that run (for commit/comment provenance)
#   GH_TOKEN     token for gh api calls (PR lookup, comments) - needs the
#                workflow's pull-requests:write; never the PAT
#   PUSH_TOKEN   token for git fetch/push only (optional; defaults to
#                GH_TOKEN), so the shared DOCS_AUTOFIX_TOKEN needs
#                contents:write only
#   AUTOFIX_TOKEN_KIND  "pat" when a dedicated push token is in use, "default"
#                       for the workflow's GITHUB_TOKEN (retrigger caveat)
#
# Exit 0 on every non-actionable outcome (PR closed, head moved, no patch);
# exit 1 on a refused patch or a genuine error so the workflow surfaces them.

set -euo pipefail
if ((BASH_VERSINFO[0] < 4)); then
	echo "bazel-autofix-push.sh: bash >= 4 required (associative arrays)" >&2
	exit 2
fi
export LC_ALL=C
# Nothing from the PR tree may run: no LFS smudge, no prompts.
export GIT_LFS_SKIP_SMUDGE=1 GIT_TERMINAL_PROMPT=0

COMMENT_MARKER="<!-- bazel-sync-autofix -->"
AUTOFIX_SUBJECT="build(bazel): auto-sync BUILD files"
# Comments are only ever edited when this account wrote them: anyone can post
# a comment that starts with the marker.
COMMENT_AUTHOR="github-actions[bot]"

# Files `make bazel-sync` may write and this bot may push - keep identical to
# scripts/ci/bazel-sync-patch.sh (scripts/ci_workflow_test.go checks). Every
# segment starts with a conservative non-dot character: no traversal, no
# .github/, no metacharacters or quoted names can slip through.
BUILD_FILE_RE='^([A-Za-z0-9_+-][A-Za-z0-9_.+-]*/)*BUILD\.bazel$'

path_allowed() {
    case "$1" in
        MODULE.bazel | MODULE.bazel.lock) return 0 ;;
        third_party/*) return 1 ;;
    esac
    [[ "$1" =~ $BUILD_FILE_RE ]]
}

# Every git call: no hooks, whatever config a clone might carry.
git_() {
    git -c core.hooksPath=/dev/null -c core.fsmonitor=false "$@"
}

# validate_patch FILE: refuse anything but plain text edits to regular files
# that path_allowed accepts. Shared verbatim with docs-autofix-push.sh
# (scripts/ci_workflow_test.go checks).
validate_patch() {
    local file="$1" line path names bad="" count=0
    # No mode change, symlink, rename or copy, binary hunk. ANY line starting
    # "rename " or "copy " is refused: git apply also accepts the legacy
    # "rename old"/"rename new" headers, which name a second path.
    if grep -qE '^(old mode|new mode|similarity index|dissimilarity index|rename |copy |GIT binary patch|Binary files )' "$file"; then
        echo "REFUSED: patch contains a mode change, rename/copy or binary hunk."
        return 1
    fi
    if grep -E '^(new file mode|deleted file mode) ' "$file" | grep -qvE '^(new file mode|deleted file mode) 100644$'; then
        echo "REFUSED: patch creates or deletes a non-regular (symlink/executable/submodule) file."
        return 1
    fi
    # Every index line, whatever its ids look like, must be "index A..B" or
    # "index A..B 100644"; a malformed id cannot hide a mode from this check.
    if grep -E '^index ' "$file" | grep -qvE '^index [0-9a-f]+\.\.[0-9a-f]+( 100644)?$'; then
        echo "REFUSED: patch has an index line that is not a regular 100644 file."
        return 1
    fi
    # --summary is git's own view of creations, deletions, renames, copies,
    # mode changes and rewrites; only 100644 creations/deletions may appear.
    if ! names="$(git apply --summary "$file")"; then
        echo "REFUSED: git apply cannot parse the patch."
        return 1
    fi
    while IFS= read -r line; do
        case "$line" in
            "") continue ;;
            " create mode 100644 "*) path="${line# create mode 100644 }" ;;
            " delete mode 100644 "*) path="${line# delete mode 100644 }" ;;
            *)
                echo "REFUSED: patch summary has an unexpected entry: $line"
                return 1
                ;;
        esac
        path_allowed "$path" || bad="${bad}  ${path}\n"
    done <<<"$names"
    # --numstat -z prints "added<TAB>deleted<TAB>NAME<NUL>" with NAME raw
    # (never quoted) and, for a rename, only the NEW name - hence the header
    # and --summary checks above. The last read field keeps any tab/newline.
    names="$(mktemp)"
    if ! git apply --numstat -z "$file" >"$names"; then
        rm -f "$names"
        echo "REFUSED: git apply cannot parse the patch."
        return 1
    fi
    while IFS=$'\t' read -r -d '' _ _ path; do
        count=$((count + 1))
        path_allowed "$path" || bad="${bad}  ${path}\n"
    done <"$names"
    rm -f "$names"
    if [ "$count" -eq 0 ]; then
        echo "REFUSED: patch names no files."
        return 1
    fi
    if [ -n "$bad" ]; then
        printf 'REFUSED: patch touches paths outside the allowlist:\n%b' "$bad"
        return 1
    fi
}

# check_staged COMMIT: the index (after git apply --cached) differs from
# COMMIT only by regular-file adds/edits/deletes of allowlisted paths. Sets
# STAGED_PATHS. Shared verbatim with docs-autofix-push.sh.
check_staged() {
    local base="$1" meta path raw src_mode dst_mode status err=""
    STAGED_PATHS=()
    raw="$(mktemp)"
    # -z --raw records: ":SRCMODE DSTMODE SRCSHA DSTSHA STATUS<NUL>PATH<NUL>".
    git_ diff-index --cached --raw --no-renames -z "$base" >"$raw"
    while IFS= read -r -d '' meta && IFS= read -r -d '' path; do
        read -r src_mode dst_mode _ _ status <<<"$meta"
        case "$src_mode $dst_mode $status" in
            ":100644 100644 M" | ":000000 100644 A" | ":100644 000000 D") ;;
            *) err="REFUSED: staged change is not a regular-file edit: $meta $path" && break ;;
        esac
        if ! path_allowed "$path"; then
            err="REFUSED: staged change outside the allowlist: $meta $path" && break
        fi
        STAGED_PATHS+=("$path")
    done <"$raw"
    rm -f -- "$raw"
    if [ -n "$err" ]; then
        echo "$err"
        return 1
    fi
}

if [ "${1:-}" = "--check" ]; then
    [ -s "${2:-}" ] || { echo "usage: $0 --check <patch>" >&2; exit 2; }
    validate_patch "$2"
    echo "OK: patch touches only allowlisted Bazel build files."
    exit 0
fi

if [ -z "${HEAD_REPO:-}" ] || [ -z "${HEAD_BRANCH:-}" ]; then
    echo "Head repository/branch unavailable (deleted fork?); nothing to do."
    exit 0
fi
: "${BASE_REPO:?}" "${HEAD_SHA:?}"
: "${PATCH_FILE:?}" "${RUN_ID:?}" "${RUN_URL:?}" "${GH_TOKEN:?}"
AUTOFIX_TOKEN_KIND="${AUTOFIX_TOKEN_KIND:-default}"
PUSH_TOKEN="${PUSH_TOKEN:-$GH_TOKEN}"

if [ ! -s "$PATCH_FILE" ]; then
    echo "No patch content; nothing to do."
    exit 0
fi
PATCH_FILE="$(readlink -f "$PATCH_FILE")"
if ! [[ "$HEAD_SHA" =~ ^[0-9a-f]{40}$ ]]; then
    echo "HEAD_SHA is not a commit id: $HEAD_SHA" >&2
    exit 1
fi

# --- Validate the untrusted patch --------------------------------------------

validate_patch "$PATCH_FILE"

# The metadata can only veto or narrow: a patch built for another head is not
# ours, and its PR number only picks among PRs that already match the event.
META_PR=""
if [ -n "${META_FILE:-}" ] && [ -f "$META_FILE" ]; then
    meta_sha="$(sed -n 's/^head_sha=\([0-9a-f]\{40\}\)$/\1/p' "$META_FILE" | head -1)"
    if [ -n "$meta_sha" ] && [ "$meta_sha" != "$HEAD_SHA" ]; then
        echo "Patch was built for head $meta_sha, not $HEAD_SHA; skipping."
        exit 0
    fi
    META_PR="$(sed -n 's/^pr=\([1-9][0-9]\{0,9\}\)$/\1/p' "$META_FILE" | head -1)"
fi

# --- Resolve the PR and confirm the patch is still current -------------------

# List-and-filter client side: branch names with URL metacharacters would
# corrupt a ?head= query string, and jq --arg needs no encoding. The PR must
# target this repository. One head branch can back several open PRs (into
# different bases): the metadata's PR number picks among them, and without
# it an ambiguous match is skipped rather than guessed.
PULLS_JSON="$(gh api --paginate "repos/$BASE_REPO/pulls?state=open&per_page=100")"
PR_MATCH="$(printf '%s' "$PULLS_JSON" | jq -r -s --arg repo "$HEAD_REPO" --arg branch "$HEAD_BRANCH" --arg base "$BASE_REPO" --arg pr "$META_PR" \
    'add | [ .[] | select(.head.ref == $branch and (.head.repo.full_name // "") == $repo
                          and (.base.repo.full_name // "") == $base)
                 | select($pr == "" or (.number | tostring) == $pr) ]
     | if length == 1 then .[0] | "\(.number)\t\(.head.sha)\t\(.base.ref)" else "\(length)" end')"
case "$PR_MATCH" in
    0)
        echo "No open PR for $HEAD_REPO:$HEAD_BRANCH into $BASE_REPO${META_PR:+ numbered #$META_PR}; nothing to do."
        exit 0
        ;;
    *$'\t'*) IFS=$'\t' read -r PR_NUMBER PR_HEAD_NOW PR_BASE_REF <<<"$PR_MATCH" ;;
    *)
        echo "$PR_MATCH open PRs use $HEAD_REPO:$HEAD_BRANCH and the patch names none of them; not guessing."
        exit 0
        ;;
esac
if [ "$PR_HEAD_NOW" != "$HEAD_SHA" ]; then
    echo "PR #$PR_NUMBER head moved ($HEAD_SHA -> $PR_HEAD_NOW); a newer run owns the fix."
    exit 0
fi

# post_or_update_comment BODY_FILE: edit our own marker comment, else post.
# Shared verbatim with docs-autofix-push.sh.
post_or_update_comment() {
    local body_file="$1"
    # Capture fully before taking the first id: head -1 on a live --paginate
    # stream SIGPIPEs gh under pipefail.
    local ids existing
    ids="$(gh api --paginate "repos/$BASE_REPO/issues/$PR_NUMBER/comments" \
        --jq ".[] | select(.user.login == \"$COMMENT_AUTHOR\" and (.body | startswith(\"$COMMENT_MARKER\"))) | .id")"
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

# head_branch_protected: true unless GitHub itself says HEAD_BRANCH has no
# branch protection and no ruleset; an API error counts as protected. The name
# list is a floor, not the check. Shared verbatim with docs-autofix-push.sh.
head_branch_protected() {
    local enc protected rules
    case "$HEAD_BRANCH" in
        main | release/* | gh-readonly-queue/*) return 0 ;;
    esac
    enc="$(jq -rn --arg b "$HEAD_BRANCH" '$b | @uri')"
    protected="$(gh api "repos/$BASE_REPO/branches/$enc" --jq '.protected' 2>/dev/null)" || return 0
    [ "$protected" = "false" ] || return 0
    rules="$(gh api "repos/$BASE_REPO/rules/branches/$enc" --jq 'length' 2>/dev/null)" || return 0
    [ "$rules" = "0" ] || return 0
    return 1
}

comment_fallback() {
    local reason="$1"
    local body
    body="$(mktemp)"
    cat > "$body" <<EOF
$COMMENT_MARKER
**Bazel BUILD files are out of sync on this PR** (${reason}).

Go files, imports or go.mod changed without \`make bazel-sync\`. CI already produced the fix; apply it locally:

\`\`\`bash
gh run download $RUN_ID -R $BASE_REPO -n bazel-sync-patch
git apply --index bazel-sync.patch
git commit -m "build(bazel): sync BUILD files"
git push
\`\`\`

Or regenerate with Bazel installed: \`make bazel-sync\`, then commit the result. Drift outside BUILD.bazel / MODULE.bazel / MODULE.bazel.lock (e.g. third_party/patches) is never in the patch; the failing run's log lists it.

_Automated by the [bazel-autofix workflow]($RUN_URL); this comment is updated in place on each failing run._
EOF
    post_or_update_comment "$body"
    rm -f "$body"
}

# --- Fork PRs: no token we hold can push there, leave the recipe --------------

if [ "$HEAD_REPO" != "$BASE_REPO" ]; then
    comment_fallback "fork PR - CI cannot push the fix to your branch"
    exit 0
fi

# Never push to a protected branch or one under a ruleset, even if a PR uses
# it as head: the push token may be able to bypass what the author cannot.
if head_branch_protected; then
    comment_fallback "the head branch $HEAD_BRANCH is protected from bot pushes"
    exit 0
fi

# --- Same-repo PRs: push the sync commit --------------------------------------

# Keep the token out of on-disk .git/config: pass the auth header per command.
# Uses PUSH_TOKEN (the optional contents:write PAT), not the API token.
AUTH_CONFIG="http.https://github.com/.extraheader=AUTHORIZATION: basic $(printf 'x-access-token:%s' "$PUSH_TOKEN" | base64 -w0)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
# Bare clone and a private index: the PR tree is never checked out, so no
# symlink, .gitattributes or hook from it touches the disk.
export GIT_INDEX_FILE="$WORK/index"

# Only the two branches this run needs, blobs on demand: other branches'
# history never reaches the runner.
git_ init --quiet --bare "$WORK/repo.git"
cd "$WORK/repo.git"
git_ remote add origin "https://github.com/${BASE_REPO}.git"
git_ config remote.origin.promisor true
git_ config remote.origin.partialclonefilter blob:none
git_ -c "$AUTH_CONFIG" fetch --quiet --no-tags --filter=blob:none origin \
    "+refs/heads/$HEAD_BRANCH:refs/autofix/head" "+refs/heads/$PR_BASE_REF:refs/autofix/base"
if ! git_ cat-file -e "$HEAD_SHA^{commit}" 2>/dev/null; then
    echo "Head $HEAD_SHA no longer reachable on $BASE_REPO/$HEAD_BRANCH; skipping."
    exit 0
fi

# Attribute the patch to the PR: bazel.yml builds the PR MERGE commit, so the
# patch also carries any drift main has. Only files the PR's own changes
# (merge-base..head) can explain are pushed:
#   * D/BUILD.bazel when the PR changed a file whose nearest package (the
#     closest directory with a BUILD.bazel) is D; for the root package only
#     root-level Go/build inputs count, not every path outside a package;
#   * MODULE.bazel / MODULE.bazel.lock when it changed go.mod, go.sum or either
#     MODULE file;
#   * everything when it changed the sync tooling (tools/bazel/, root BUILD.bazel).
MERGE_BASE="$(git_ merge-base refs/autofix/base "$HEAD_SHA" 2>/dev/null || true)"
if [ -z "$MERGE_BASE" ]; then
    cd / && comment_fallback "CI could not tell which files this PR changed"
    exit 0
fi
declare -A PKG_DIRS=() PR_PKGS=()
PR_ALL=0 PR_MODULE=0
while IFS= read -r -d '' path; do
    case "$path" in
        BUILD.bazel) PKG_DIRS[.]=1 ;;
        */BUILD.bazel) PKG_DIRS["${path%/BUILD.bazel}"]=1 ;;
    esac
done < <(git_ ls-tree -r -z --name-only "$HEAD_SHA")
PATCH_PATHS=()
while IFS=$'\t' read -r -d '' _ _ path; do
    PATCH_PATHS+=("$path")
    case "$path" in */BUILD.bazel) PKG_DIRS["${path%/BUILD.bazel}"]=1 ;; esac
done < <(git apply --numstat -z "$PATCH_FILE")
PKG_DIRS[.]=1
while IFS= read -r -d '' path; do
    case "$path" in
        go.mod | go.sum | MODULE.bazel | MODULE.bazel.lock) PR_MODULE=1 ;;
        BUILD.bazel | tools/bazel/*) PR_ALL=1 ;;
    esac
    dir="$(dirname -- "$path")"
    while [ -z "${PKG_DIRS[$dir]:-}" ]; do dir="$(dirname -- "$dir")"; done
    # The walk ends at the root for every path outside a package (docs/,
    # .github/, README.md, ...); only the root's own Go and build inputs
    # can change what gazelle writes to the root BUILD.bazel.
    if [ "$dir" = . ]; then
        case "$path" in
            */*) continue ;;
            *.go | *.s | *.c | *.h | *.bzl | go.mod | go.sum | BUILD.bazel | MODULE.bazel | MODULE.bazel.lock) ;;
            *) continue ;;
        esac
    fi
    PR_PKGS["$dir"]=1
done < <(git_ diff --name-only --no-renames -z "$MERGE_BASE" "$HEAD_SHA")

OURS=() NOT_OURS=()
for path in "${PATCH_PATHS[@]}"; do
    case "$path" in
        MODULE.bazel | MODULE.bazel.lock) pkg="" ours=$PR_MODULE ;;
        BUILD.bazel) pkg=. ours=0 ;;
        *) pkg="${path%/BUILD.bazel}" ours=0 ;;
    esac
    if [ "$PR_ALL" = 1 ] || [ "$ours" = 1 ] || { [ -n "$pkg" ] && [ -n "${PR_PKGS[$pkg]:-}" ]; }; then
        OURS+=("$path")
    else
        NOT_OURS+=("$path")
    fi
done
NOT_OURS_NOTE=""
if [ "${#NOT_OURS[@]}" -gt 0 ]; then
    echo "Not pushed (outside what the PR changed, likely base-branch drift):"
    printf '  %s\n' "${NOT_OURS[@]}"
    NOT_OURS_NOTE="Not pushed, because this PR did not change those packages (likely drift on the base branch; rebasing after it is fixed clears it): $(printf '%s ' "${NOT_OURS[@]}")"
fi
if [ "${#OURS[@]}" -eq 0 ]; then
    cd / && comment_fallback "the drift is in files this PR did not change - most likely the base branch is out of sync, so no fix was pushed"
    exit 0
fi

INCLUDES=()
for path in "${OURS[@]}"; do INCLUDES+=("--include=$path"); done
git_ read-tree "$HEAD_SHA"
if ! git_ -c "$AUTH_CONFIG" apply --cached "${INCLUDES[@]}" "$PATCH_FILE" 2>/dev/null; then
    cd / && comment_fallback "the sync patch no longer applies cleanly to the PR head"
    exit 0
fi

# Belt and braces: what actually got staged must pass the same rules, and
# only include the files attributed to the PR.
check_staged "$HEAD_SHA"
declare -A OURS_SET=()
for path in "${OURS[@]}"; do OURS_SET["$path"]=1; done
for path in "${STAGED_PATHS[@]}"; do
    if [ -z "${OURS_SET[$path]:-}" ]; then
        echo "REFUSED: staged change not attributed to the PR: $path"
        exit 1
    fi
done
if [ "${#STAGED_PATHS[@]}" -eq 0 ]; then
    echo "Patch changes nothing on $HEAD_SHA; nothing to push."
    exit 0
fi

# Circuit breaker, once the PR's own fix is known to be non-empty: if the
# failing head is already one of our autofix commits, the sync is not
# converging - stacking more bot commits would loop. Read from the fetched
# commit (no API); failing to read it counts as non-convergent.
if ! HEAD_SUBJECT="$(git_ log -1 --format=%s "$HEAD_SHA")" || [[ "$HEAD_SUBJECT" == "$AUTOFIX_SUBJECT"* ]]; then
    echo "Head $HEAD_SHA is (or may be) an autofix commit; refusing to stack another."
    cd / && comment_fallback "the previous auto-fix commit did not fix this PR's own files - please run make bazel-sync on the branch and commit the result"
    exit 0
fi

TREE="$(git_ write-tree)"
NEW_SHA="$(GIT_AUTHOR_NAME="github-actions[bot]" GIT_COMMITTER_NAME="github-actions[bot]" \
    GIT_AUTHOR_EMAIL="41898282+github-actions[bot]@users.noreply.github.com" \
    GIT_COMMITTER_EMAIL="41898282+github-actions[bot]@users.noreply.github.com" \
    git_ commit-tree "$TREE" -p "$HEAD_SHA" -m "$AUTOFIX_SUBJECT

Applied from the bazel-sync-patch artifact of $RUN_URL
(\`make bazel-sync\`: gazelle, tools/bazel/go_srcs.py, bazel mod tidy).
Only BUILD.bazel, MODULE.bazel and MODULE.bazel.lock are ever pushed; see
scripts/bazel-autofix-push.sh.")"

# Leased to HEAD_SHA: if the branch moved at all since the run (including a
# force-push back to an ancestor), the push is refused rather than resurrecting
# commits the author dropped.
if ! git_ -c "$AUTH_CONFIG" push --quiet \
    "--force-with-lease=refs/heads/$HEAD_BRANCH:$HEAD_SHA" \
    origin "$NEW_SHA:refs/heads/$HEAD_BRANCH"; then
    cd /
    comment_fallback "pushing the fix to $HEAD_BRANCH failed (branch protection or a concurrent push)"
    exit 0
fi
cd /

echo "Pushed sync commit $NEW_SHA to $BASE_REPO/$HEAD_BRANCH."

BODY="$(mktemp)"
cat > "$BODY" <<EOF
$COMMENT_MARKER
**Pushed \`${NEW_SHA:0:12}\` syncing the Bazel BUILD files** (\`make bazel-sync\` output from the [failing run]($RUN_URL)). Pull before pushing again.
EOF
if [ -n "$NOT_OURS_NOTE" ]; then
    printf '\n%s\n' "$NOT_OURS_NOTE" >> "$BODY"
fi
if [ "$AUTOFIX_TOKEN_KIND" = "default" ]; then
    cat >> "$BODY" <<'EOF'

Note: this commit was pushed with the default workflow token, which does **not** retrigger PR checks - re-run them (or push any commit) to refresh the gate. Configuring the `DOCS_AUTOFIX_TOKEN` repo secret (shared with the docs autofix) removes this step.
EOF
fi
post_or_update_comment "$BODY"
rm -f "$BODY"
