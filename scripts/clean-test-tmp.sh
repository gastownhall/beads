#!/usr/bin/env bash
# clean-test-tmp.sh — sweep orphaned cmd/bd test temp dirs.
#
# The cmd/bd test suite creates several MkdirTemp parents under $TMPDIR
# (HOME isolation, built test binaries, etc.). They are normally cleaned
# by testMainInner's defer, but a SIGKILLed / OOMed test run leaves them
# behind. On tmpfs hosts (e.g. Bluefin's 20GB /tmp) these can grow to
# several GB and exhaust /tmp.
#
# This script removes orphaned dirs matching the known patterns. It is
# safe to run between test invocations. Dirs whose lockfile / mtime
# suggests an in-progress run are skipped.
#
# See bd-3q2u / gastownhall/beads#4106.

set -euo pipefail

tmpdir="${TMPDIR:-/tmp}"

# Patterns created by cmd/bd test helpers (see test_repo_beads_guard_test.go
# and the package-level sync.Once builders).
patterns=(
    "beads-bd-tests-*"
    "beads-shared-server-bd-*"
    "bd-testbin-*"
    "bd-init-test-*"
    "bd-init-permissions-test-*"
    "bd-embedded-init-test-*"
)

# Only sweep dirs older than this many minutes, so we don't blow away
# a test run that's mid-flight in another worktree.
min_age_min="${BEADS_CLEAN_TEST_TMP_MIN_AGE:-30}"

removed=0
skipped=0
total_bytes=0

for pat in "${patterns[@]}"; do
    # -mindepth/-maxdepth 1 so we only match top-level entries.
    # -mmin +N skips dirs newer than N minutes.
    while IFS= read -r -d '' dir; do
        # Size for reporting (du in KB; cheap on tmpfs).
        sz_kb=$(du -sk "$dir" 2>/dev/null | awk '{print $1}' || echo 0)
        # chmod -R u+w so we can remove read-only Go modcache files.
        chmod -R u+w "$dir" 2>/dev/null || true
        if rm -rf "$dir" 2>/dev/null; then
            removed=$((removed + 1))
            total_bytes=$((total_bytes + sz_kb))
            printf '  removed %s (%s KB)\n' "$dir" "$sz_kb"
        else
            skipped=$((skipped + 1))
            printf '  skipped %s (rm failed; may be in use)\n' "$dir" >&2
        fi
    done < <(find "$tmpdir" -mindepth 1 -maxdepth 1 -type d -name "$pat" -mmin "+$min_age_min" -print0 2>/dev/null)
done

printf 'Removed %d orphaned dir(s), reclaimed ~%d MB. Skipped %d.\n' \
    "$removed" "$((total_bytes / 1024))" "$skipped"
