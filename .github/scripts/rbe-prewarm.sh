#!/usr/bin/env bash
# bazel.yml's rbe-prewarm job: best-effort pre-warm of gastownhall/gascity's
# rbe-west OSS worker pool (one Blacksmith-hosted NativeLink worker,
# rbe-worker-pool.yml) so capacity is already booting instead of beads'
# Bazel lanes waiting ~120s at "N actions, 0 running" while the pool scales
# from zero. gascity pre-warms its own pool from inside its bazel-test.yml
# job (same-repo dispatch, github.token); beads cannot use its GITHUB_TOKEN
# against gascity, so the rbe-prewarm job mints a short-lived installation
# token for the "bazel-allocator" GitHub App (installed on
# gastownhall/gascity only, Actions read/write + Metadata read, nothing
# else; app id and private key are secrets.RBE_POOL_APP_ID /
# secrets.RBE_POOL_APP_PRIVATE_KEY) and passes it to this script as
# GH_TOKEN. See engdocs/CI_REQUIRED_CHECK_TOPOLOGY.md, "rbe-west Pre-warm".
#
# Best effort by design: every failure path (missing token, gascity
# unreachable, gh rate-limited, dispatch rejected) prints a ::warning:: and
# this script still exits 0, so a pre-warm problem never fails the
# rbe-prewarm job, let alone the bazel.yml run it is part of or pr.yml's
# gate (the job is advisory: not a `needs` of any lane, not in ci-gate's
# required list).
#
# Inputs (environment):
#   GH_TOKEN                 gastownhall/gascity-scoped installation token,
#                             minted by the job's own
#                             actions/create-github-app-token step (empty
#                             when the app secrets are not provisioned, or
#                             when the mint step itself fails). Required for
#                             every gh call below; empty or invalid fails
#                             closed into a warning, never a non-zero exit.
#   RBE_PREWARM_WORKERS       desired worker count N (repo variable; default
#                             "1" when unset or non-numeric, clamped to
#                             1..4). "0" (or any value <= 0 after clamping)
#                             is the kill switch: pre-warm is disabled.
#   RBE_PREWARM_IDLE_MINUTES  optional idle_minutes passed to the dispatched
#                             workflow_dispatch; omitted (gascity's own
#                             default applies) when unset or empty.
#
# Exits 0 unconditionally.
set -u

POOL_REPO="gastownhall/gascity"
POOL_WORKFLOW="rbe-worker-pool.yml"
POOL_REF="main"

n="${RBE_PREWARM_WORKERS:-1}"
case "$n" in
  '') n=1 ;;
  *[!0-9]*) n=1 ;;
esac
if [ "$n" -gt 4 ]; then
  n=4
fi

if [ "$n" -le 0 ]; then
  echo "RBE_PREWARM_WORKERS=0: rbe-west pre-warm disabled (kill switch)"
  exit 0
fi

if [ -z "${GH_TOKEN:-}" ]; then
  echo "::warning title=rbe-west pre-warm::no dispatch token (bazel-allocator app not provisioned, or the mint step failed); skipping"
  exit 0
fi

active="$(gh run list -R "$POOL_REPO" --workflow "$POOL_WORKFLOW" --limit 50 \
  --json status --jq '[.[] | select(.status != "completed")] | length' 2>/dev/null)"
case "$active" in
  '' | *[!0-9]*)
    echo "::warning title=rbe-west pre-warm::could not read $POOL_REPO/$POOL_WORKFLOW run status; skipping dispatch"
    exit 0
    ;;
esac

missing=$((n - active))
if [ "$missing" -le 0 ]; then
  echo "${active} $POOL_REPO/$POOL_WORKFLOW worker(s) already active (want ${n}); nothing to dispatch"
  exit 0
fi

args=(workflow run "$POOL_WORKFLOW" -R "$POOL_REPO" --ref "$POOL_REF")
if [ -n "${RBE_PREWARM_IDLE_MINUTES:-}" ]; then
  args+=(-f "idle_minutes=${RBE_PREWARM_IDLE_MINUTES}")
fi

dispatched=0
i=1
while [ "$i" -le "$missing" ]; do
  if gh "${args[@]}"; then
    dispatched=$((dispatched + 1))
  else
    echo "::warning title=rbe-west pre-warm::gh workflow run $POOL_WORKFLOW -R $POOL_REPO failed (attempt $i/$missing)"
  fi
  i=$((i + 1))
done

echo "dispatched ${dispatched}/${missing} $POOL_REPO/$POOL_WORKFLOW worker(s) (active before: ${active}, want: ${n})"
exit 0
