#!/usr/bin/env bash
# bdp-vendor.sh — vendor the pinned gastownhall/bdp files into third_party/bdp.
#
# Copies the files in BDP_PATHS below, byte for byte, from a tagged bdp
# checkpoint into third_party/bdp/spec/<bdp-relative path>, and writes
# third_party/bdp/bdp.pin: the pin (tag, tag object, commit, specRevision),
# the lock (size, sha256 and git blob id of every vendored file) and a
# `lock` line, the sha256 of all its other non-comment lines, in one file.
# Two runs that differ anywhere write different `lock` lines, so two PRs
# that each change bdp.pin always conflict on that line: git can never merge
# them into a pin that mixes two checkpoints' files. This script is their
# only writer. third_party/bdp/pin_test.go (TestVendoredBDPMatchesPin) fails
# when a vendored file is changed, added or removed, when bdp.pin is
# malformed, its non-comment lines are not in this script's exact format or
# its `lock` line does not match them, or when LICENSE is missing. It cannot
# check the checkpoint itself without bdp's history, so every PR that
# touches bdp.pin is reviewed with --check. The rules are in
# engdocs/design/bdp.md.
#
# Usage: scripts/bdp-vendor.sh BDP_CHECKOUT TAG [--check]
#   BDP_CHECKOUT  a full (not shallow) clone of gastownhall/bdp with its tags
#                 and an up-to-date origin/main (git -C BDP_CHECKOUT fetch
#                 --tags origin)
#   TAG           an annotated tag named spec/*, e.g. spec/sprint1-20261109
#   --check       regenerate into a temporary directory and exit 1 if
#                 third_party/bdp/spec/ or bdp.pin differ, or if spec/ holds
#                 a symlink or an executable file; write nothing
#
# Refuses a shallow clone, a missing or lightweight tag, a tag not named
# spec/*, a commit not reachable from the checkout's origin/main, and (when
# writing) a commit that does not descend from (or equal) the one the
# current bdp.pin records, or local refs that disagree with the remote's.
# --check does not check that direction: it compares the tree with what TAG
# generates, so the reviewer of a pin bump checks it by hand
# (engdocs/design/bdp.md). Files are read with `git show <commit>:<path>`,
# so the checkout's work tree is never used.
#
# A slice that adds a consumer of another bdp file adds the path to
# BDP_PATHS and reruns this script at the same pin.

set -euo pipefail

# Every vendored file, with the consumer that justifies it.
BDP_PATHS=(
    LICENSE                                                    # MIT notice; license compliance
    packages/conformance/catalog/current/callables-v1.json     # BDP's conformance tests: the row census
    packages/conformance/catalog/current/issue-package-v1.json # BDP's conformance tests: the row census
    specs/profiles/core/bdp-v0.1.schema.json                   # BDP's conformance tests: response admission
)

# bdp's specRevision rule (scripts/canonical-spec-revision.mjs): the last
# commit at or before the pin that changed one of these, by a plain
# `git log -1`, not --first-parent. Checked against the .mjs at the pin: its
# canonicalSources must be exactly these, and its git call a plain log -1.
SPEC_REVISION_SCRIPT=scripts/canonical-spec-revision.mjs
SPEC_REVISION_SOURCES=(
    specs/profiles/core/core.md
    specs/profiles/write/write.md
    specs/profiles/types/types.md
    specs/profiles/events/events.md
)
# Keep REPOSITORY and SPEC_REVISION_RULE in step with pinRepository and
# pinSpecRevisionRule in third_party/bdp/pin_test.go.
SPEC_REVISION_RULE="git log -1 --format=%H <commit> -- ${SPEC_REVISION_SOURCES[*]} (bdp $SPEC_REVISION_SCRIPT; not --first-parent)"
REPOSITORY="https://github.com/gastownhall/bdp"

die() {
    echo "bdp-vendor: $*" >&2
    exit 1
}

usage() {
    echo "usage: scripts/bdp-vendor.sh BDP_CHECKOUT TAG [--check]" >&2
    echo "  BDP_CHECKOUT is a full (not shallow), freshly fetched clone of $REPOSITORY" >&2
    exit 2
}

[ "$#" -eq 2 ] || [ "$#" -eq 3 ] || usage
BDP="$1"
TAG="$2"
CHECK=0
if [ "$#" -eq 3 ]; then
    [ "$3" = "--check" ] || usage
    CHECK=1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
DEST="$PROJECT_ROOT/third_party/bdp"
PIN="$DEST/bdp.pin"

if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
    die "need sha256sum or shasum"
fi

bdp_git() {
    git -C "$BDP" "$@"
}

bdp_git rev-parse --git-dir >/dev/null 2>&1 || die "$BDP is not a git checkout of gastownhall/bdp"
[ "$(bdp_git rev-parse --is-shallow-repository)" = false ] ||
    die "$BDP is a shallow clone; specRevision needs its full history (git -C $BDP fetch --unshallow --tags origin)"

# The checkout must be gastownhall/bdp itself, not a fork.
origin_url="$(bdp_git remote get-url origin 2>/dev/null || true)"
case "$origin_url" in
    https://github.com/gastownhall/bdp | https://github.com/gastownhall/bdp.git | \
        git@github.com:gastownhall/bdp | git@github.com:gastownhall/bdp.git | \
        ssh://git@github.com/gastownhall/bdp | ssh://git@github.com/gastownhall/bdp.git) ;;
    *) die "$BDP's origin is '${origin_url:-<none>}', not $REPOSITORY" ;;
esac

case "$TAG" in
    spec/*) ;;
    *) die "tag '$TAG' is not a bdp checkpoint tag (spec/*)" ;;
esac
bdp_git rev-parse --verify -q "refs/tags/$TAG" >/dev/null ||
    die "no tag '$TAG' in $BDP (git -C $BDP fetch --tags origin)"
[ "$(bdp_git cat-file -t "refs/tags/$TAG")" = "tag" ] ||
    die "tag '$TAG' is lightweight; a checkpoint is an annotated tag"
TAG_OBJECT="$(bdp_git rev-parse "refs/tags/$TAG")"
COMMIT="$(bdp_git rev-parse --verify -q "refs/tags/$TAG^{commit}")" ||
    die "tag '$TAG' does not name a commit"

bdp_git rev-parse --verify -q refs/remotes/origin/main >/dev/null ||
    die "$BDP has no origin/main; run: git -C $BDP fetch origin"
ORIGIN_MAIN="$(bdp_git rev-parse refs/remotes/origin/main)"

# When writing, the local refs must be what gastownhall/bdp has now: a tag
# that exists only locally, or was moved, is not a checkpoint.
if [ "$CHECK" -eq 0 ]; then
    remote_tag="$(bdp_git ls-remote origin "refs/tags/$TAG" | awk -v r="refs/tags/$TAG" '$2 == r { print $1 }')" ||
        die "cannot reach $BDP's origin to verify $TAG"
    [ "$remote_tag" = "$TAG_OBJECT" ] ||
        die "origin's $TAG is '${remote_tag:-<missing>}', not the local $TAG_OBJECT; fetch first (git -C $BDP fetch --tags --force origin)"
    remote_main="$(bdp_git ls-remote origin refs/heads/main | awk '$2 == "refs/heads/main" { print $1 }')" ||
        die "cannot reach $BDP's origin to verify main"
    [ "$remote_main" = "$ORIGIN_MAIN" ] ||
        die "origin's main is '${remote_main:-<missing>}', not the local origin/main $ORIGIN_MAIN; fetch first (git -C $BDP fetch origin)"
fi

bdp_git merge-base --is-ancestor "$COMMIT" "$ORIGIN_MAIN" ||
    die "$TAG ($COMMIT) is not reachable from bdp origin/main (fetch origin if main moved)"

# Forward only: the new commit descends from (or equals) the current pin's.
# --check skips this gate: it compares the tree with what TAG generates, so a
# hand-edited commit line reports "bdp.pin differs" instead.
OLD=""
if [ -f "$PIN" ]; then
    OLD="$(sed -n 's/^commit \([0-9a-f]\{40\}\)$/\1/p' "$PIN")"
fi
if [ "$CHECK" -eq 0 ] && [ -f "$PIN" ]; then
    [ -n "$OLD" ] || die "$PIN has no commit line"
    bdp_git cat-file -e "$OLD^{commit}" 2>/dev/null ||
        die "the current pin's commit $OLD is not in $BDP; run: git -C $BDP fetch origin"
    bdp_git merge-base --is-ancestor "$OLD" "$COMMIT" ||
        die "$TAG ($COMMIT) does not descend from the current pin $OLD; pins only move forward"
fi

# specRevision, by bdp's own rule as it stands at the pin: refuse if the
# .mjs is gone, lists other sources than this script, or no longer runs a
# plain `git log -1` (e.g. --first-parent).
rule_script="$(bdp_git show "$COMMIT:$SPEC_REVISION_SCRIPT" 2>/dev/null)" ||
    die "$SPEC_REVISION_SCRIPT does not exist at $COMMIT; bdp's specRevision rule moved, update this script"
for src in "${SPEC_REVISION_SOURCES[@]}"; do
    bdp_git cat-file -e "$COMMIT:$src" 2>/dev/null ||
        die "specRevision source $src does not exist at $COMMIT; update this script's rule"
done
listed="$(awk '/const canonicalSources = \[/ { f = 1 } f { print } f && /\];/ { exit }' <<<"$rule_script" |
    grep -o '"[^"]*"' | tr -d '"' || true)"
[ "$listed" = "$(printf '%s\n' "${SPEC_REVISION_SOURCES[@]}")" ] ||
    die "$SPEC_REVISION_SCRIPT at $COMMIT lists other sources than this script; update the rule"
grep -qE '\["log",[[:space:]]*"-1",[[:space:]]*"--format=%H",[[:space:]]*"--",' <<<"$rule_script" ||
    die "$SPEC_REVISION_SCRIPT at $COMMIT no longer runs a plain git log -1; update the rule"
SPEC_REVISION="$(bdp_git log -1 --format=%H "$COMMIT" -- "${SPEC_REVISION_SOURCES[@]}")"
[[ "$SPEC_REVISION" =~ ^[0-9a-f]{40}$ ]] || die "could not derive specRevision at $COMMIT"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
OUT="$WORK/bdp"
mkdir -p "$OUT/spec"

lines=()
while IFS= read -r path; do
    [[ "$path" =~ ^[A-Za-z0-9._/-]+$ ]] || die "unsupported path '$path'"
    mode_type="$(bdp_git ls-tree "$COMMIT" -- "$path" | cut -f1)"
    [ -n "$mode_type" ] || die "$path does not exist at $COMMIT"
    case "$mode_type" in
        "100644 blob "*) ;;
        *) die "$path at $COMMIT is not a regular file ($mode_type)" ;;
    esac
    blob="$(bdp_git rev-parse "$COMMIT:$path")"
    mkdir -p "$OUT/spec/$(dirname "$path")"
    bdp_git show "$COMMIT:$path" >"$OUT/spec/$path"
    [ "$(git hash-object --no-filters "$OUT/spec/$path")" = "$blob" ] ||
        die "$path: copied bytes do not hash to blob $blob"
    size="$(wc -c <"$OUT/spec/$path" | tr -d ' ')"
    lines+=("file $(sha256 "$OUT/spec/$path") $size $blob $path")
done < <(printf '%s\n' "${BDP_PATHS[@]}" | LC_ALL=C sort -u)

{
    echo "repository $REPOSITORY"
    echo "tag $TAG"
    echo "tag-object $TAG_OBJECT"
    echo "commit $COMMIT"
    echo "spec-revision $SPEC_REVISION"
    echo "spec-revision-rule $SPEC_REVISION_RULE"
} >"$WORK/keys"
printf '%s\n' "${lines[@]}" >"$WORK/files"
# The lock line: the sha256 of every other non-comment line, in file order,
# each ending in a newline, i.e. of
#   grep -v -e '^#' -e '^lock ' third_party/bdp/bdp.pin
# Keep it in step with lockOf in third_party/bdp/pin_test.go.
cat "$WORK/keys" "$WORK/files" >"$WORK/locked"
LOCK="$(sha256 "$WORK/locked")"

{
    echo "# BDP pin and lock: the gastownhall/bdp checkpoint vendored in spec/"
    echo "# beside this file, and the size, sha256 and git blob id of every"
    echo "# vendored file. Generated by scripts/bdp-vendor.sh; never edit by hand."
    echo "# Rules: engdocs/design/bdp.md. The pin moves only in a pin-bump PR"
    echo "# approved by Julian Knutsen; a PR that adds a consumer of another bdp"
    echo "# file reruns the script at the same pin. TestVendoredBDPMatchesPin"
    echo "# checks the files against this lock and its format; reviewers check"
    echo "# the checkpoint with scripts/bdp-vendor.sh <bdp checkout> <tag> --check."
    echo "# The lock line is the sha256 of the other non-comment lines, so two"
    echo "# PRs that change this file conflict on it. Resolve that by taking"
    echo "# main's bdp.pin and rerunning the script; never merge it by hand."
    cat "$WORK/keys"
    echo "lock $LOCK"
    cat "$WORK/files"
} >"$OUT/bdp.pin"

if [ "$CHECK" -eq 1 ]; then
    status=0
    if ! diff -r "$DEST/spec" "$OUT/spec" >&2; then
        echo "bdp-vendor: third_party/bdp/spec/ differs from $TAG" >&2
        status=1
    fi
    if [ -d "$DEST/spec" ]; then
        odd="$(find "$DEST/spec" -type l -o -type f \( -perm -u+x -o -perm -g+x -o -perm -o+x \))"
        if [ -n "$odd" ]; then
            echo "bdp-vendor: symlink or executable under spec/:" >&2
            echo "$odd" >&2
            status=1
        fi
    fi
    if ! diff "$PIN" "$OUT/bdp.pin" >&2; then
        echo "bdp-vendor: third_party/bdp/bdp.pin differs from what $TAG generates" >&2
        status=1
    fi
    if [ "$status" -ne 0 ]; then
        echo "bdp-vendor: regenerate with scripts/bdp-vendor.sh $BDP $TAG" >&2
        exit 1
    fi
    echo "bdp-vendor: third_party/bdp matches $TAG ($COMMIT)"
    exit 0
fi

mkdir -p "$DEST"
rm -rf "$DEST/spec"
cp -R "$OUT/spec" "$DEST/spec"
cp "$OUT/bdp.pin" "$PIN"
echo "bdp-vendor: vendored $TAG ($COMMIT), specRevision $SPEC_REVISION"
echo "bdp-vendor: ${#lines[@]} files in third_party/bdp/spec/; review the bdp.pin diff and"
if [ -z "$OLD" ]; then
    echo "  git -C $BDP log --oneline -1 $COMMIT -- ${BDP_PATHS[*]}  (first vendoring: no previous pin)"
elif [ "$OLD" != "$COMMIT" ]; then
    echo "  git -C $BDP log --oneline $OLD..$COMMIT -- ${BDP_PATHS[*]}"
else
    echo "  git -C $BDP log --oneline -1 $COMMIT -- ${BDP_PATHS[*]}  (same pin as before)"
fi
