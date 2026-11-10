#!/usr/bin/env bash
#
# Mutation equivalence: does test B catch what test A catches?
#
#   ./scripts/mutation-equivalence.sh <prod-file> <mutation.py> <pkgA> <runA> <pkgB> <runB>
#
# The question this answers is NOT "do these two tests look similar". That
# reading is how you delete real coverage and keep every gate green. It is:
# when the production body breaks, do BOTH suites notice?
#
#   A red, B red    the contract catches it too   — A is redundant
#   A red, B green  only A caught it              — A is UNIQUE; port it, then delete
#   A green, B red  A does not cover this break   — wrong mutation for this pairing
#   both green      neither covers it             — proves nothing about either
#
# WHAT THIS TOOL CANNOT TELL YOU, and what a caller must supply: a verdict is
# only true of the BODY THE MUTATION WAS IN. The uow provider runs its own
# bodies under internal/storage/domain/ where the two store backends share
# others, so "A red, B red" against a shared body says nothing about a wrapper
# or a per-backend composition that the same test also observes. Three separate
# coverage losses in this repo came from accepting a red/red verdict for the
# wrong body. Name the body the assertion observes, then mutate THAT.
#
# <mutation.py> receives the file path as argv[1] and rewrites it in place.
#
# The guards below all exist because their absence produced a wrong answer at
# least once:
#
#   - All mutation happens in a DISPOSABLE worktree. The tool is routinely
#     interrupted mid-run, and a trap does not survive SIGKILL; mutating the
#     checkout you are working in strands broken production code that compiles.
#   - A `-run` regex matching nothing is fatal. `go test -run NoSuchTest` EXITS
#     0, so a typo reads as PASS forever.
#   - A mutation that changes no bytes is fatal. It reports agreement.
#   - A baseline that is not green is fatal. The verdict would be noise.
#   - A nonzero exit is not a catch. A build failure or timeout is reported as
#     UNUSABLE, never as FAIL, because either would turn an assertion-free test
#     into a false REDUNDANT.
#   - The worktree path must be a worktree of $REPO. It is hard-reset and
#     cleaned, so pointing it at a live checkout would destroy that checkout.
#   - A failed checkout/reset is fatal: it would measure a DIFFERENT COMMIT and
#     still exit 0.
#   - The mutation must change only the file it names, and A and B must differ.
#   - It measures $REPO's HEAD COMMIT, not its working tree; uncommitted work is
#     invisible and the run warns rather than silently measuring the wrong code.
#   - Result greps do not anchor leading whitespace: `go test -v` indents
#     subtests by 4 and NESTED subtests by 8, so an over-anchored pattern finds
#     nothing and reads as "did not fail". scripts/conformance.sh documents the
#     mirror image of this hazard for its own column-0 case.
#
set -uo pipefail

usage() {
  sed -n '2,51p' "$0" | sed 's|^#||'
  exit 2
}
[ $# -eq 6 ] || usage

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
WT="${MUTEQ_WT:-${TMPDIR:-/tmp}/beads-mutation-worktree}"
TIMEOUT="${MUTEQ_TIMEOUT:-1200}"

PROD="$1"; MUT="$2"; APKG="$3"; ARUN="$4"; BPKG="$5"; BRUN="$6"

# Comparing a suite with itself yields REDUNDANT mechanically, because B IS A.
if [ "$APKG" = "$BPKG" ] && [ "$ARUN" = "$BRUN" ]; then
  echo "FATAL: A and B are the same selector; the verdict would be tautological" >&2; exit 2
fi

[ -f "$REPO/$PROD" ] || { echo "FATAL: no such file in $REPO: $PROD" >&2; exit 2; }
[ -f "$MUT" ]        || { echo "FATAL: no such mutation script: $MUT" >&2; exit 2; }

HEAD_SHA="$(git -C "$REPO" rev-parse HEAD)"

# THIS TOOL MEASURES $REPO's HEAD COMMIT, NOT ITS WORKING TREE. The scratch
# worktree is built from HEAD_SHA, so uncommitted work — including the very test
# you are trying to verify — is invisible to the run. That failure is silent and
# reads as INCONCLUSIVE, which is indistinguishable from "the test is weak"
# unless you know to look. Commit first, or accept that the verdict is about
# what is committed.
if [ -n "$(git -C "$REPO" status --porcelain)" ]; then
  echo "WARNING: $REPO has uncommitted changes. This run measures HEAD" >&2
  echo "         ($(git -C "$REPO" rev-parse --short HEAD)); those changes are NOT in it." >&2
  git -C "$REPO" status --short >&2 | head -10
  echo >&2
fi

# The worktree path is hard-reset and cleaned below, so pointing it at a live
# checkout DESTROYS that checkout — including $REPO itself, which is the exact
# accident the disposable-worktree design exists to prevent. Only a path this
# script created, or one git already lists as a worktree of $REPO, is eligible.
if [ -e "$WT" ]; then
  WT_ABS="$(cd "$WT" 2>/dev/null && pwd)" || WT_ABS=""
  REPO_ABS="$(cd "$REPO" && pwd)"
  if [ -z "$WT_ABS" ] || [ "$WT_ABS" = "$REPO_ABS" ]; then
    echo "FATAL: MUTEQ_WT resolves to \$REPO itself. This script hard-resets and" >&2
    echo "       cleans that path; running would destroy the checkout it is measuring." >&2
    exit 2
  fi
  # Captured into a variable, NOT piped. `grep -q` exits on the first match,
  # which SIGPIPEs git; with `pipefail` that makes the whole pipeline nonzero and
  # the negation below then rejects a perfectly good worktree. It only bites once
  # the list is large enough for git to still be writing — this repo has ~370
  # worktrees / 53 KB — so it looks intermittent and machine-specific. It killed
  # 5 of 9 runs for the first caller who hit it.
  WT_LIST="$(git -C "$REPO" worktree list --porcelain)"
  if ! grep -qx "worktree $WT_ABS" <<<"$WT_LIST"; then
    echo "FATAL: $WT exists but is not a worktree of $REPO." >&2
    echo "       This script hard-resets and cleans that path; refusing to touch it." >&2
    exit 2
  fi
  # A worktree with a branch checked out is somebody's working copy, not a
  # scratch one this tool created. Only a DETACHED head is disposable.
  if git -C "$WT" symbolic-ref -q HEAD >/dev/null 2>&1; then
    echo "FATAL: $WT has a branch checked out ($(git -C "$WT" rev-parse --abbrev-ref HEAD))." >&2
    echo "       Only a detached scratch worktree is safe to hard-reset. Point" >&2
    echo "       MUTEQ_WT at a path this tool created, or at nothing." >&2
    exit 2
  fi
fi
if [ ! -e "$WT/.git" ]; then
  echo "== creating mutation worktree at $WT =="
  git -C "$REPO" worktree add --detach "$WT" "$HEAD_SHA" >/dev/null 2>&1 \
    || { echo "FATAL: could not create worktree at $WT" >&2; exit 2; }
else
  # A failure here would silently measure a DIFFERENT COMMIT and still exit 0,
  # so it is fatal rather than suppressed.
  git -C "$WT" checkout -q --detach "$HEAD_SHA" \
    || { echo "FATAL: could not check out $HEAD_SHA in $WT" >&2; exit 2; }
fi
git -C "$WT" reset -q --hard "$HEAD_SHA" \
  || { echo "FATAL: could not reset $WT to $HEAD_SHA" >&2; exit 2; }
git -C "$WT" clean -qfd
if [ "$(git -C "$WT" rev-parse HEAD)" != "$HEAD_SHA" ]; then
  echo "FATAL: $WT is at $(git -C "$WT" rev-parse --short HEAD), not $REPO's HEAD" >&2
  exit 2
fi

# A previous run that was killed can leave the worktree dirty, which would make
# this run's "baseline" someone else's mutation.
if [ -n "$(git -C "$WT" status --porcelain)" ]; then
  echo "FATAL: mutation worktree is dirty after reset; refusing to run" >&2
  git -C "$WT" status --short >&2
  exit 2
fi

cd "$WT" || exit 2
# shellcheck disable=SC1091
[ -f .buildflags ] && source .buildflags
export BEADS_TEST_EMBEDDED_DOLT="${BEADS_TEST_EMBEDDED_DOLT:-1}"

restore() { git -C "$WT" checkout -q -- . 2>/dev/null; }
trap restore EXIT INT TERM

# Count executed tests. Leading whitespace deliberately unanchored.
count() {
  timeout "$TIMEOUT" go test "$1" -run "$2" -count=1 -v 2>/dev/null | grep -cE '^ *=== RUN'
}
# A nonzero `go test` exit is NOT the same as "this suite caught the mutation".
# A build failure and a timeout both exit nonzero, and both would otherwise read
# as a catch — turning an assertion-free test into a false REDUNDANT, which is
# the one wrong answer that costs coverage.
result() {
  local out rc
  out="$(timeout "$TIMEOUT" go test "$1" -run "$2" -count=1 2>&1)"; rc=$?
  if [ $rc -eq 124 ]; then echo TIMEOUT; return; fi
  if grep -qE '^(# |.*\[build failed\]|.*\[setup failed\])' <<<"$out"; then echo BUILDFAIL; return; fi
  [ $rc -eq 0 ] && echo PASS || echo FAIL
}

echo "== baseline ($WT @ $(git rev-parse --short HEAD)) =="
AN=$(count "$APKG" "$ARUN")
BN=$(count "$BPKG" "$BRUN")
[ "${AN:-0}" -gt 0 ] || { echo "FATAL: -run '$ARUN' matches no test in $APKG" >&2; exit 3; }
[ "${BN:-0}" -gt 0 ] || { echo "FATAL: -run '$BRUN' matches no test in $BPKG" >&2; exit 3; }
A0=$(result "$APKG" "$ARUN"); B0=$(result "$BPKG" "$BRUN")
printf "  A %-4s  %3s executed  %s %s\n" "$A0" "$AN" "$APKG" "$ARUN"
printf "  B %-4s  %3s executed  %s %s\n" "$B0" "$BN" "$BPKG" "$BRUN"
if [ "$A0" != PASS ] || [ "$B0" != PASS ]; then
  echo "FATAL: baseline is not green; a mutation result would mean nothing" >&2; exit 4
fi

echo "== mutating $PROD =="
BEFORE="$(mktemp)"; cp "$PROD" "$BEFORE"
python3 "$MUT" "$PROD" || { rm -f "$BEFORE"; echo "FATAL: mutation script failed" >&2; exit 5; }
if cmp -s "$BEFORE" "$PROD"; then
  rm -f "$BEFORE"; echo "FATAL: mutation changed nothing — it would report false agreement" >&2; exit 6
fi
if ! go build ./... >/dev/null 2>&1; then
  rm -f "$BEFORE"; echo "FATAL: mutated tree does not compile; use a semantic mutation" >&2; exit 7
fi
# The displayed diff covers only $PROD, so a script that also edited something
# else would show a harmless-looking change while the verdict came from a break
# the operator never saw — the wrong-body hazard, manufactured by this tool.
STRAY="$(git -C "$WT" status --porcelain | awk '{print $2}' | grep -vx "$PROD" || true)"
if [ -n "$STRAY" ]; then
  rm -f "$BEFORE"
  echo "FATAL: the mutation also changed files it did not name:" >&2
  printf '  %s\n' $STRAY >&2
  exit 8
fi
diff "$BEFORE" "$PROD" | head -20; rm -f "$BEFORE"

A1=$(result "$APKG" "$ARUN"); B1=$(result "$BPKG" "$BRUN")
printf "== under mutation ==\n  A %s\n  B %s\n" "$A1" "$B1"

echo "== verdict =="
case "$A1/$B1" in
  FAIL/FAIL)
    echo "  REDUNDANT    B catches this too — for THIS body, and for THIS promise."
    echo "               A retires only when every promise it pins has its own verdict."
    ;;
  FAIL/PASS)  echo "  UNIQUE       Only A caught it. Port the case into B, THEN delete A." ;;
  PASS/FAIL)  echo "  MISDIRECTED  A does not cover this mutation. Wrong pairing." ;;
  PASS/PASS)  echo "  INCONCLUSIVE Neither caught it. Proves nothing about either." ;;
  *TIMEOUT*|*BUILDFAIL*)
    echo "  UNUSABLE     A suite did not fail on an ASSERTION: A=$A1 B=$B1."
    echo "               A build failure or a timeout exits nonzero like a real"
    echo "               catch does. Treating it as one is how an assertion-free"
    echo "               test reads as REDUNDANT. Fix the mutation and re-run."
    exit 9
    ;;
esac
