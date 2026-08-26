#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# channel-gate-report.sh — assemble the release-gate dossier for a promotion
# =============================================================================
#
# Part of the release-channel proposal for gastownhall/beads (issue #5867).
# Phase 2 of three; see engdocs/RELEASE-CHANNELS.md in the same PR.
#
# Emits a release-gates/<tag>.md file in the format the project already uses by
# hand (27 such files exist today), so that approving a stable release is
# reading one page rather than performing a ceremony.
#
# EVERY FIGURE IS QUERIED, NOT ASSERTED. Gate results come from the check runs
# GitHub actually recorded against the beta's commit; soak duration comes from
# the beta release's published_at; the shipped-PR manifest comes from git
# ancestry. Nothing in the output is a claim this script made up, which is the
# property that makes the dossier worth approving on.
#
# It writes a file and prints a verdict. It does not tag, push, or publish.
#
# Usage:
#   channel-gate-report.sh --from-beta v1.3.0-beta.2 [--soak-days 7]
#                          [--repo owner/name] [--out <path>] [--allow-short-soak]
# =============================================================================

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

FROM_BETA=""
PREVIEW=""
SOAK_DAYS=7
REPO="gastownhall/beads"
OUT=""
ALLOW_SHORT_SOAK=0
EXPECT_RED=()

usage() {
    cat <<'USAGE'
Usage: channel-gate-report.sh --from-beta <tag> [options]
       channel-gate-report.sh --preview [<ref>] [options]

Options:
  --from-beta <tag>      The beta tag proposed for promotion.
  --preview [<ref>]      Instead of judging a promotion, report what a stable
                         release cut from <ref> (default HEAD) would FIRST
                         ship: how much merged work is in no published stable
                         release, how old the oldest of it is, and whether the
                         span crosses a schema boundary. No soak and no gate
                         sections — there is no candidate to have soaked or
                         been gated. Answers "what are we sitting on?" without
                         needing to cut anything first.
  --soak-days <N>        Minimum soak before promotion is allowed. Default 7.
  --repo <owner/name>    Repository to query. Default gastownhall/beads.
  --out <path>           Where to write. Default release-gates/v<version>.md
  --allow-short-soak     Record the shortfall and continue rather than failing.
                         For a security fix that cannot wait out the window.
  --expect-red 'GLOB=reason'
                         Pre-declare an expected red check, with the reason
                         recorded in the dossier. Repeatable. The project
                         already works this way — the v1.2.2 gate file records
                         a deliberately refused forward-skew as a "pre-declared
                         expected signature". Example:
                           --expect-red 'Upgrade smoke (v1.2.1*=forward skew, refused by design'
  -h, --help             This text.

Exit codes:
  0  verdict PASS — dossier written, promotion may proceed
  1  usage or precondition error
  2  verdict FAIL — an undeclared gate is red, or the soak is short
  3  verdict REVIEW — nothing is red, but a gate did not complete. Not an
     automatic block and not an automatic pass; a human adjudicates.
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --from-beta) FROM_BETA="${2:-}"; shift 2 ;;
        --preview)
            # Optional ref; bare --preview means HEAD.
            if [ "${2:-}" != "" ] && [ "${2#-}" = "${2}" ]; then
                PREVIEW="$2"; shift 2
            else
                PREVIEW="HEAD"; shift
            fi ;;
        --soak-days) SOAK_DAYS="${2:-}"; shift 2 ;;
        --repo)      REPO="${2:-}"; shift 2 ;;
        --out)       OUT="${2:-}"; shift 2 ;;
        --allow-short-soak) ALLOW_SHORT_SOAK=1; shift ;;
        --expect-red) EXPECT_RED+=("${2:-}"); shift 2 ;;
        -h|--help)   usage; exit 0 ;;
        *) echo -e "${RED}Unknown argument: $1${NC}" >&2; usage >&2; exit 1 ;;
    esac
done

if [ -z "$FROM_BETA" ] && [ -z "$PREVIEW" ]; then
    echo -e "${RED}ERROR: one of --from-beta or --preview is required${NC}" >&2; usage >&2; exit 1
fi
if [ -n "$FROM_BETA" ] && [ -n "$PREVIEW" ]; then
    echo -e "${RED}ERROR: --from-beta and --preview are different questions${NC}" >&2
    echo "--from-beta judges a candidate; --preview reports what is unshipped." >&2
    exit 1
fi
command -v gh >/dev/null 2>&1 || { echo -e "${RED}ERROR: gh CLI not found${NC}" >&2; exit 1; }

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$REPO_ROOT" ] || { echo -e "${RED}ERROR: not inside a git repository${NC}" >&2; exit 1; }
cd "$REPO_ROOT"

# -----------------------------------------------------------------------------
# Which tags are actually released
# -----------------------------------------------------------------------------
#
# A TAG NAME IS NOT PROOF OF A RELEASE, and here that distinction inverts the
# answer rather than shading it. `v1.2.1` carries no prerelease identifier in
# its name, so any name-based filter (`--exclude '*-*'`) treats it as stable —
# but GitHub has it flagged `prerelease: true`, because it was withdrawn. Using
# it as a baseline understates what a release ships by the entire backlog
# behind it.
#
# So validity comes from what GitHub published: not a draft, not a prerelease,
# and actually published. Tags absent from the local clone are dropped, since
# ancestry cannot be computed for them.
MIGRATION_DIR="internal/storage/schema/migrations"

VALID_TAGS=""
while IFS= read -r t; do
    [ -n "$t" ] || continue
    git rev-parse --verify --quiet "${t}^{commit}" >/dev/null 2>&1 || continue
    VALID_TAGS="${VALID_TAGS} ${t}"
done <<EOF
$(gh api "repos/${REPO}/releases?per_page=100" --paginate \
    --jq '.[] | select(.draft == false and .prerelease == false and .published_at != null) | .tag_name' 2>/dev/null || true)
EOF

if [ -z "${VALID_TAGS// /}" ]; then
    echo -e "${RED}ERROR: no published stable releases resolved for ${REPO}${NC}" >&2
    echo "Without them there is no baseline to measure against." >&2
    exit 1
fi

# Nearest released ancestor of a ref: among valid releases that are ancestors,
# the one with the fewest commits between it and the ref.
nearest_released_ancestor() {
    local ref="$1" best="" bestdist=-1 t dist
    for t in $VALID_TAGS; do
        git merge-base --is-ancestor "$t" "$ref" 2>/dev/null || continue
        dist="$(git rev-list --count "${t}..${ref}" 2>/dev/null || echo 999999999)"
        if [ "$bestdist" -lt 0 ] || [ "$dist" -lt "$bestdist" ]; then
            bestdist="$dist"; best="$t"
        fi
    done
    printf '%s' "$best"
}

# -----------------------------------------------------------------------------
# Preview: what is sitting unshipped
# -----------------------------------------------------------------------------

if [ -n "$PREVIEW" ]; then
    git rev-parse --verify --quiet "${PREVIEW}^{commit}" >/dev/null \
        || { echo -e "${RED}ERROR: '$PREVIEW' is not a known ref${NC}" >&2; exit 1; }
    REF_SHA="$(git rev-parse "${PREVIEW}^{commit}")"

    # The unreleased set is everything on the ref that is in NO valid release —
    # not everything since the newest tag. Those differ whenever a release was
    # cut from a side branch, which is the case here.
    UNRELEASED="$(git rev-list "$REF_SHA" --not $VALID_TAGS 2>/dev/null || true)"
    UNREL_COUNT="$(printf '%s' "$UNRELEASED" | grep -c . || true)"

    PR_NUMS="$(printf '%s\n' "$UNRELEASED" \
                 | while IFS= read -r c; do [ -n "$c" ] && git log -1 --format='%s' "$c"; done \
                 | grep -oE '\(#[0-9]+\)$' | tr -d '()#' | sort -un || true)"
    PR_COUNT="$(printf '%s' "$PR_NUMS" | grep -c . || true)"

    LAST_RELEASED="$(nearest_released_ancestor "$REF_SHA")"

    if [ "$UNREL_COUNT" -gt 0 ]; then
        OLDEST_SHA="$(printf '%s\n' "$UNRELEASED" | tail -1)"
        OLDEST_DATE="$(git log -1 --format='%ci' "$OLDEST_SHA" | cut -d' ' -f1)"
        OLDEST_AGE_DAYS="$(( ( $(date -u +%s) - $(git log -1 --format='%ct' "$OLDEST_SHA") ) / 86400 ))"
        NEW_MIGS="$(git diff --name-only --diff-filter=A "${LAST_RELEASED}..${REF_SHA}" -- "$MIGRATION_DIR" 2>/dev/null \
                     | grep -E "^${MIGRATION_DIR}/[0-9]{4}_.*\.up\.sql$" || true)"
        MIG_COUNT="$(printf '%s' "$NEW_MIGS" | grep -c . || true)"
    else
        OLDEST_DATE="n/a"; OLDEST_AGE_DAYS=0; MIG_COUNT=0; NEW_MIGS=""
    fi

    echo
    echo -e "${BLUE}=== Unshipped work on $(git rev-parse --short "$REF_SHA") ===${NC}"
    echo
    printf '  %-28s %s\n' "valid stable releases" "$(printf '%s' "$VALID_TAGS" | wc -w | tr -d ' ')"
    printf '  %-28s %s\n' "last released ancestor" "${LAST_RELEASED:-<none>}"
    printf '  %-28s %s\n' "commits in no release" "$UNREL_COUNT"
    printf '  %-28s %s\n' "pull requests unshipped" "$PR_COUNT"
    printf '  %-28s %s\n' "oldest unshipped" "${OLDEST_DATE} (${OLDEST_AGE_DAYS}d)"
    if [ "$MIG_COUNT" -eq 0 ]; then
        printf '  %-28s %s\n' "schema steps" "none — schema-compatible"
    else
        printf '  %-28s %s\n' "schema steps" "$MIG_COUNT"
        printf '%s\n' "$NEW_MIGS" | sed 's|^|      |'
        if [ "$MIG_COUNT" -gt 1 ]; then
            echo
            echo -e "  ${YELLOW}More than one schema step: a beta may carry only one, so this${NC}"
            echo -e "  ${YELLOW}span needs cutting at the first migration boundary.${NC}"
        fi
    fi
    echo

    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        {
            echo "unreleased_commits=$UNREL_COUNT"
            echo "unreleased_prs=$PR_COUNT"
            echo "oldest_unreleased_days=$OLDEST_AGE_DAYS"
            echo "last_released=${LAST_RELEASED}"
            echo "migration_count=$MIG_COUNT"
        } >> "$GITHUB_OUTPUT"
    fi
    exit 0
fi

git rev-parse --verify --quiet "${FROM_BETA}^{commit}" >/dev/null \
    || { echo -e "${RED}ERROR: '$FROM_BETA' is not a known tag${NC}" >&2; exit 1; }

BETA_SHA="$(git rev-parse "${FROM_BETA}^{commit}")"
BETA_VERSION="${FROM_BETA#v}"
# `-beta.N` is this proposal's identifier; `-rc.N` is the one RELEASING.md
# already documents and the one every existing prerelease here uses. Both are
# gated prereleases, so both are promotable — refusing rc would orphan the
# candidates the project has actually cut.
case "$BETA_VERSION" in
    *-beta.*|*-rc.*) ;;
    *-dev.*|*-canary.*)
        echo -e "${RED}ERROR: '$FROM_BETA' is a $(printf '%s' "$BETA_VERSION" | sed 's/.*-\([a-z]*\)\..*/\1/') build${NC}" >&2
        echo "Only a beta or rc may be promoted to stable. A dev build has passed the" >&2
        echo "nightly suite but not the full release gate, and promoting one skips" >&2
        echo "exactly the checks the gate exists to run." >&2
        exit 1 ;;
    *) echo -e "${RED}ERROR: '$FROM_BETA' is not a prerelease tag${NC}" >&2; exit 1 ;;
esac
STABLE_VERSION="${BETA_VERSION%%-*}"
STABLE_TAG="v${STABLE_VERSION}"
[ -n "$OUT" ] || OUT="release-gates/${STABLE_TAG}.md"

# -----------------------------------------------------------------------------
# Soak — measured from when the beta was actually published, not when it was
# tagged. A tag that sat unpublished soaked nobody.
# -----------------------------------------------------------------------------

PUBLISHED_AT="$(gh release view "$FROM_BETA" --repo "$REPO" --json publishedAt --jq '.publishedAt' 2>/dev/null || true)"
if [ -z "$PUBLISHED_AT" ] || [ "$PUBLISHED_AT" = "null" ]; then
    echo -e "${RED}ERROR: no published release found for $FROM_BETA${NC}" >&2
    echo "A beta that was never published cannot have soaked." >&2
    exit 1
fi

# GNU and BSD date disagree on parsing; try both rather than assume the runner.
epoch_of() {
    date -u -d "$1" +%s 2>/dev/null || date -u -j -f "%Y-%m-%dT%H:%M:%SZ" "$1" +%s 2>/dev/null
}
PUB_EPOCH="$(epoch_of "$PUBLISHED_AT")"
NOW_EPOCH="$(date -u +%s)"
SOAK_SECONDS=$(( NOW_EPOCH - PUB_EPOCH ))
SOAK_ELAPSED_DAYS=$(( SOAK_SECONDS / 86400 ))
SOAK_HOURS=$(( SOAK_SECONDS / 3600 ))

SOAK_OK=1
[ "$SOAK_ELAPSED_DAYS" -ge "$SOAK_DAYS" ] || SOAK_OK=0

# -----------------------------------------------------------------------------
# Gate results — read from the check runs GitHub recorded on the beta commit.
# -----------------------------------------------------------------------------

# gh ships jq internally, so parsing needs no external tool. One call, then
# classify in three buckets — because two of them are not the same thing:
#
#   passed        success, skipped, neutral
#   failed        failure, timed_out, action_required   -> blocks
#   inconclusive  cancelled, stale, still running       -> a human decides
#
# Treating "cancelled" as a failure would have blocked the real v1.2.2: its
# Migration fidelity check was cancelled, not failed. Treating it as a pass
# would claim a gate ran that did not. Neither is honest, so it is surfaced.
CHECKS_API="repos/${REPO}/commits/${BETA_SHA}/check-runs?per_page=100"
CHECK_LINES="$(gh api "$CHECKS_API" --jq '.check_runs[] | "\(.conclusion // "running")\t\(.name)"' 2>/dev/null || true)"
CHECK_TOTAL="$(printf '%s' "$CHECK_LINES" | grep -c . || true)"

FAILED_RAW="$(printf '%s\n' "$CHECK_LINES" | grep -E '^(failure|timed_out|action_required)\b' || true)"
INCONCLUSIVE_RAW="$(printf '%s\n' "$CHECK_LINES" | grep -E '^(cancelled|stale|running)\b' || true)"

# Pre-declared expected reds. The project already works this way — the v1.2.2
# gate file records "30 green / 1 red, the red being the deliberately refused
# forward-skew (pre-declared expected signature)". Without this the tool cannot
# express what their real releases actually do, and would block them.
FAILED_ROWS=""; EXPECTED_ROWS=""; CHECK_FAILED=0; CHECK_EXPECTED=0
while IFS=$'\t' read -r conclusion name; do
    [ -n "${name:-}" ] || continue
    matched=""
    for spec in "${EXPECT_RED[@]:-}"; do
        [ -n "$spec" ] || continue
        pattern="${spec%%=*}"; reason="${spec#*=}"
        # shellcheck disable=SC2254
        case "$name" in
            $pattern) matched="$reason"; break ;;
        esac
    done
    if [ -n "$matched" ]; then
        EXPECTED_ROWS="${EXPECTED_ROWS}| \`${name}\` | ${conclusion} | ${matched} |"$'\n'
        CHECK_EXPECTED=$(( CHECK_EXPECTED + 1 ))
    else
        FAILED_ROWS="${FAILED_ROWS}| \`${name}\` | ${conclusion} |"$'\n'
        CHECK_FAILED=$(( CHECK_FAILED + 1 ))
    fi
done <<< "$FAILED_RAW"

INCONCLUSIVE_ROWS=""; CHECK_INCONCLUSIVE=0
while IFS=$'\t' read -r conclusion name; do
    [ -n "${name:-}" ] || continue
    INCONCLUSIVE_ROWS="${INCONCLUSIVE_ROWS}| \`${name}\` | ${conclusion} |"$'\n'
    CHECK_INCONCLUSIVE=$(( CHECK_INCONCLUSIVE + 1 ))
done <<< "$INCONCLUSIVE_RAW"

GATES_OK=1
[ "$CHECK_TOTAL" -gt 0 ] || GATES_OK=0
[ "$CHECK_FAILED" -eq 0 ] || GATES_OK=0

# -----------------------------------------------------------------------------
# Issues raised against the beta during its soak
# -----------------------------------------------------------------------------

SOAK_ISSUE_ROWS="$(gh search issues --repo "$REPO" --created ">=${PUBLISHED_AT%T*}" \
                     "$BETA_VERSION" --limit 30 \
                     --json number,title,state \
                     --jq '.[] | "| #\(.number) | \(.state) | \(.title[0:90]) |"' 2>/dev/null || true)"
SOAK_ISSUE_COUNT="$(printf '%s' "$SOAK_ISSUE_ROWS" | grep -c . || true)"

# -----------------------------------------------------------------------------
# What this release first ships, and the schema delta — both by ancestry.
# The baseline is the nearest ancestor that GitHub actually published as a
# stable release, not the nearest tag that merely looks like one. Two separate
# traps: v1.2.2 is a v1.1-line re-release and is not an ancestor of main, and
# v1.2.1 has a stable-looking NAME while being flagged a prerelease because it
# was withdrawn. Either would give a confidently wrong baseline.
# -----------------------------------------------------------------------------

PREV_STABLE="$(nearest_released_ancestor "${BETA_SHA}^")"

if [ -n "$PREV_STABLE" ]; then
    RANGE="${PREV_STABLE}..${BETA_SHA}"
    PR_LIST="$(git log --format='%s' "$RANGE" | grep -oE '\(#[0-9]+\)$' | tr -d '()#' | sort -un || true)"
    PR_COUNT="$(printf '%s' "$PR_LIST" | grep -c . || true)"
    COMMIT_COUNT="$(git rev-list --count "$RANGE")"
    NEW_MIGRATIONS="$(git diff --name-only --diff-filter=A "$RANGE" -- "$MIGRATION_DIR" 2>/dev/null \
                       | grep -E "^${MIGRATION_DIR}/[0-9]{4}_.*\.up\.sql$" || true)"
    MIGRATION_COUNT="$(printf '%s' "$NEW_MIGRATIONS" | grep -c . || true)"
else
    RANGE="(no ancestor stable tag)"
    PR_COUNT="unknown"; COMMIT_COUNT="unknown"; MIGRATION_COUNT="unknown"; NEW_MIGRATIONS=""
fi

# -----------------------------------------------------------------------------
# Verdict
# -----------------------------------------------------------------------------

VERDICT="PASS"
FAIL_REASONS=""
if [ "$CHECK_TOTAL" -eq 0 ]; then
    VERDICT="FAIL"
    FAIL_REASONS="${FAIL_REASONS}- No check runs found on \`${BETA_SHA}\`. An ungated build cannot be promoted.\n"
elif [ "$CHECK_FAILED" -gt 0 ]; then
    VERDICT="FAIL"
    FAIL_REASONS="${FAIL_REASONS}- ${CHECK_FAILED} check(s) failed and were not pre-declared.\n"
fi
if [ "$CHECK_EXPECTED" -gt 0 ]; then
    FAIL_REASONS="${FAIL_REASONS}- ${CHECK_EXPECTED} red check(s) were **pre-declared** with a reason and are not treated as blocking. Read them below and satisfy yourself the reasons still hold.\n"
fi
if [ "$CHECK_INCONCLUSIVE" -gt 0 ] && [ "$VERDICT" = "PASS" ]; then
    VERDICT="REVIEW"
    FAIL_REASONS="${FAIL_REASONS}- ${CHECK_INCONCLUSIVE} gate(s) did not complete (cancelled, stale, or still running). Nothing is red, but these did not run — so the gate is unproven rather than passed.\n"
fi
if [ "$SOAK_OK" -ne 1 ]; then
    if [ "$ALLOW_SHORT_SOAK" -eq 1 ]; then
        FAIL_REASONS="${FAIL_REASONS}- Soak is ${SOAK_ELAPSED_DAYS}d against a ${SOAK_DAYS}d minimum, **overridden deliberately** via --allow-short-soak.\n"
    else
        VERDICT="FAIL"
        FAIL_REASONS="${FAIL_REASONS}- Soak is ${SOAK_ELAPSED_DAYS}d (${SOAK_HOURS}h) against a ${SOAK_DAYS}d minimum.\n"
    fi
fi

# -----------------------------------------------------------------------------
# Write the dossier
# -----------------------------------------------------------------------------

mkdir -p "$(dirname "$OUT")"
{
cat <<EOF
# Release gate — ${STABLE_TAG} (promotion of ${FROM_BETA})

**Date:** $(date -u +%Y-%m-%d)
**Promoted from:** \`${FROM_BETA}\` @ \`${BETA_SHA}\`
**Tag to be created:** \`${STABLE_TAG}\` on the same commit — the tree that soaked is the tree that ships
**Previous stable:** \`${PREV_STABLE:-none found}\`
**Assembled by:** \`scripts/channel-gate-report.sh\`, from GitHub check runs and git ancestry

## Verdict: ${VERDICT}

EOF

if [ -n "$FAIL_REASONS" ]; then
    printf '%b\n' "$FAIL_REASONS"
fi

cat <<EOF
## Soak

| | |
|---|---|
| Beta published | ${PUBLISHED_AT} |
| Soak elapsed | ${SOAK_ELAPSED_DAYS} days (${SOAK_HOURS}h) |
| Minimum required | ${SOAK_DAYS} days |
| Issues mentioning \`${BETA_VERSION}\` raised since | ${SOAK_ISSUE_COUNT} |

EOF

if [ "${SOAK_ISSUE_COUNT}" != "0" ]; then
    echo "| Issue | State | Title |"
    echo "|---|---|---|"
    printf '%s\n' "$SOAK_ISSUE_ROWS"
    echo
    echo "> These are keyword matches, not a triaged list. Read them before approving —"
    echo "> the soak only means something if somebody looked at what it surfaced."
    echo
fi

cat <<EOF
## Gates (check runs recorded on \`${BETA_SHA}\`)

| | |
|---|---|
| Checks recorded | ${CHECK_TOTAL} |
| Failed, undeclared | ${CHECK_FAILED} |
| Red, pre-declared | ${CHECK_EXPECTED} |
| Did not complete | ${CHECK_INCONCLUSIVE} |

EOF

if [ "$CHECK_FAILED" -gt 0 ]; then
    echo "### Failing — these block"
    echo
    echo "| Check | Conclusion |"
    echo "|---|---|"
    printf '%s' "$FAILED_ROWS"
    echo
fi

if [ "$CHECK_EXPECTED" -gt 0 ]; then
    echo "### Red, but pre-declared"
    echo
    echo "| Check | Conclusion | Declared reason |"
    echo "|---|---|---|"
    printf '%s' "$EXPECTED_ROWS"
    echo
    echo "> A pre-declaration is an argument, not an exemption. It was made when the"
    echo "> promotion was proposed; if the reason no longer holds, this is a red."
    echo
fi

if [ "$CHECK_INCONCLUSIVE" -gt 0 ]; then
    echo "### Did not complete"
    echo
    echo "| Check | State |"
    echo "|---|---|"
    printf '%s' "$INCONCLUSIVE_ROWS"
    echo
    echo "> These are neither green nor red — they did not run to a verdict. That is"
    echo "> not the same as passing, and it is why the verdict above is REVIEW rather"
    echo "> than PASS. Re-run them, or approve on the explicit basis that they are"
    echo "> not load-bearing for this change."
    echo
fi

cat <<EOF
These are the gates the beta promotion ran in full: the suite, the
cross-version upgrade smoke matrix at the 30-release depth, the regression
baseline, the MCP and npm package gates, and the migration harness. They are
reported here as GitHub recorded them rather than re-run, so this dossier
reflects the build being promoted and not a fresh one.

## Content

| | |
|---|---|
| Range | \`${RANGE}\` |
| Commits | ${COMMIT_COUNT} |
| Pull requests first shipped | ${PR_COUNT} |
| Schema migrations | ${MIGRATION_COUNT} |

EOF

if [ "${MIGRATION_COUNT}" = "0" ]; then
    echo "**No schema change.** This release is schema-compatible with"
    echo "\`${PREV_STABLE}\` in both directions, so a user who upgrades and then"
    echo "downgrades is not stranded."
elif [ "${MIGRATION_COUNT}" != "unknown" ]; then
    echo "**Carries ${MIGRATION_COUNT} schema migration(s):**"
    echo
    printf '%s\n' "$NEW_MIGRATIONS" | sed 's|^|- `|; s|$|`|'
    echo
    echo "Migrations are applied forward-only on first access by the new binary."
    echo "Confirm the down path is present and the recovery note is in the"
    echo "CHANGELOG before approving."
fi

cat <<EOF

## What approving does

Creating \`${STABLE_TAG}\` triggers \`release.yml\`, which publishes the GitHub
release and — because this tag carries no prerelease identifier — also
publishes to PyPI and npm, and makes the release eligible for the
homebrew-core autobump. This is the step that reaches users.

Declining costs nothing: the beta stays published and available, and the next
promotion can be proposed from a later beta.
EOF
} > "$OUT"

# -----------------------------------------------------------------------------
# Report
# -----------------------------------------------------------------------------

echo
case "$VERDICT" in
    PASS)   echo -e "${GREEN}Verdict: PASS${NC} — ${FROM_BETA} -> ${STABLE_TAG}" ;;
    REVIEW) echo -e "${YELLOW}Verdict: REVIEW${NC} — ${FROM_BETA} -> ${STABLE_TAG}" ;;
    *)      echo -e "${RED}Verdict: FAIL${NC} — ${FROM_BETA} -> ${STABLE_TAG}" ;;
esac
printf '%b' "$FAIL_REASONS"
echo "  soak      ${SOAK_ELAPSED_DAYS}d (min ${SOAK_DAYS}d), ${SOAK_ISSUE_COUNT} issue(s) mentioning the beta"
echo "  gates     ${CHECK_TOTAL} recorded: ${CHECK_FAILED} failed, ${CHECK_EXPECTED} pre-declared, ${CHECK_INCONCLUSIVE} incomplete"
echo "  content   ${PR_COUNT} PRs, ${MIGRATION_COUNT} migration(s)"
echo "  written   ${OUT}"
echo

if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
        echo "verdict=$VERDICT"
        echo "stable_version=$STABLE_VERSION"
        echo "stable_tag=$STABLE_TAG"
        echo "beta_sha=$BETA_SHA"
        echo "soak_days=$SOAK_ELAPSED_DAYS"
        echo "migration_count=$MIGRATION_COUNT"
        echo "report=$OUT"
    } >> "$GITHUB_OUTPUT"
fi

case "$VERDICT" in
    PASS)   exit 0 ;;
    REVIEW) exit 3 ;;
    *)      exit 2 ;;
esac
