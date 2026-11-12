#!/usr/bin/env bash
# Repro for bd-49w8x2 (GH #3059): user-facing URLs that bd prints at runtime
# (install script, docs, examples, issue links) must point at the current repo
# gastownhall/beads, not the pre-move steveyegge/beads. Go import paths are
# excluded on purpose: they follow go.mod's module path (a separate decision).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
hits=$(git grep -nE 'https://(raw\.githubusercontent\.com|github\.com)/steveyegge/beads|steveyegge\.github\.io' \
  -- 'cmd/**/*.go' 'internal/**/*.go' ':!*_test.go' || true)
if [[ -n "$hits" ]]; then
  echo "FAIL: runtime (non-test) Go source still emits pre-move steveyegge/beads URLs:"
  echo "$hits"
  exit 1
fi
echo "PASS: no runtime steveyegge/beads URLs"
