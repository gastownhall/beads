#!/usr/bin/env bash
# Shadow-mode validation harness for revdepscope (FR-4 / be-mv0ww.3.1).
#
# revdepscope's output is not trusted for anything until it has run clean,
# in shadow mode, against a real sample of commits — see be-mv0ww.3.1's
# acceptance criteria. This script never wires revdepscope into a gate; it
# only compares its output against a full-suite ground truth and reports
# divergences for a human to read.
#
# For each of the last N real commits on origin/main, this:
#   1. Computes that commit's actual changed Go packages (git diff vs its
#      parent, resolved against the CURRENT tree — see Limitations below).
#   2. Runs revdepscope on those changed packages.
#   3. Checks that every package currently FAILing in a fresh full-suite
#      ground-truth run (step 0, done once) is included in revdepscope's
#      scoped set for that commit (or that "full suite required" was
#      returned, which trivially covers everything).
#
# A "missed-failure divergence" means: a package fails in the full run, but
# revdepscope's scoped set for some commit's changed packages did not
# include it. That would mean a scoped CI run could have missed a real
# failure — the exact risk this tool exists to avoid introducing.
#
# Limitations (read before trusting a clean run too far):
#   - Changed-package resolution uses the CURRENT checkout, not each
#     historical commit's own tree. A file/directory a historical commit
#     touched but that has since been renamed or deleted resolves to
#     nothing and that commit is skipped (reported, not silently dropped).
#     Package structure is stable enough over a handful of recent commits
#     for this to be a reasonable approximation, not a full historical
#     replay.
#   - Ground truth is one fresh full-suite run at current HEAD, reused for
#     every sampled commit's comparison — not a per-commit historical full
#     run. This is what makes the harness affordable to actually run.
#   - If the ground-truth run has zero failures (the common case on a
#     repo that keeps main green), every commit's divergence check is
#     vacuously true. That is a real limitation of validating against green
#     history, not a flaw in the check itself: see the posted results for
#     how this run's ground truth came out and what that does and doesn't
#     demonstrate.
#
# Usage:
#   shadow-mode.sh [num_commits]
#
# num_commits defaults to 5 and samples the last N commits on origin/main.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

NUM_COMMITS="${1:-5}"
if ! [[ "$NUM_COMMITS" =~ ^[0-9]+$ ]] || ((NUM_COMMITS < 1)); then
    echo "num_commits must be a positive integer" >&2
    exit 1
fi

cd "$REPO_ROOT"

# shellcheck source=../../.buildflags
source "$REPO_ROOT/.buildflags"

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
BIN="$BUILD_DIR/revdepscope"

echo "Building revdepscope..." >&2
go build -o "$BIN" ./scripts/ci/revdepscope

echo "Ground truth: running the full suite at $(git rev-parse --short HEAD)..." >&2
FULL_LOG="$BUILD_DIR/full-suite.log"
set +e
./scripts/test.sh 2>&1 | tee "$FULL_LOG"
set -e

mapfile -t FAILED_PACKAGES < <(awk '/^FAIL[[:space:]]/{print $2}' "$FULL_LOG" | sort -u)
echo "" >&2
echo "Ground truth: ${#FAILED_PACKAGES[@]} package(s) FAILing at HEAD" >&2

mapfile -t COMMITS < <(git log origin/main -n "$NUM_COMMITS" --format=%H)

echo ""
echo "commit,changed_packages,scoped_result,divergence"

DIVERGENCES=0
for commit in "${COMMITS[@]}"; do
    short="${commit:0:8}"

    mapfile -t changed_files < <(git diff --name-only "${commit}^" "$commit" -- '*.go' 2>/dev/null || true)
    if ((${#changed_files[@]} == 0)); then
        echo "$short,(no .go changes),skipped,no"
        continue
    fi

    mapfile -t changed_dirs < <(for f in "${changed_files[@]}"; do dirname "$f"; done | sort -u)
    changed_pkgs=()
    for dir in "${changed_dirs[@]}"; do
        pkg="$(go list "./$dir" 2>/dev/null || true)"
        [[ -n "$pkg" ]] && changed_pkgs+=("$pkg")
    done
    if ((${#changed_pkgs[@]} == 0)); then
        echo "$short,(no packages resolvable in current tree),skipped,no"
        continue
    fi

    scoped_output="$("$BIN" "${changed_pkgs[@]}" 2>/dev/null || echo "ERROR")"

    if [[ "$scoped_output" == "full suite required" ]]; then
        result="full suite required"
        divergence="no"
    elif [[ "$scoped_output" == "ERROR" ]]; then
        result="ERROR running revdepscope"
        divergence="yes"
        DIVERGENCES=$((DIVERGENCES + 1))
    else
        pkg_count=$(printf '%s\n' "$scoped_output" | grep -c .)
        result="${pkg_count} package(s)"
        divergence="no"
        for failed in "${FAILED_PACKAGES[@]:-}"; do
            [[ -z "$failed" ]] && continue
            if ! grep -qxF "$failed" <<<"$scoped_output"; then
                divergence="yes"
            fi
        done
        [[ "$divergence" == "yes" ]] && DIVERGENCES=$((DIVERGENCES + 1))
    fi

    echo "$short,\"${changed_pkgs[*]}\",\"$result\",$divergence"
done

echo ""
if ((DIVERGENCES > 0)); then
    echo "SHADOW MODE FAILED: $DIVERGENCES commit(s) showed a missed-failure divergence or error." >&2
    exit 1
fi
echo "SHADOW MODE PASSED: 0 missed-failure divergences across ${#COMMITS[@]} sampled commit(s) (ground truth: ${#FAILED_PACKAGES[@]} FAILing package(s) at HEAD)." >&2
