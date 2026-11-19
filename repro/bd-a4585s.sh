#!/usr/bin/env bash
# Repro for bd-a4585s (GH#8113): on darwin/ios, Go's os.UserConfigDir()
# returns $HOME/Library/Application Support, so any launch-path resolver that
# calls it directly stats ~/Library/Application Support/bd/config.yaml (and
# .../beads/PRIME.md) on every bd launch, which macOS 26 reports as a TCC
# "access data from other apps" prompt. This cannot be executed on a Linux
# host, so this test-shaped check asserts the CORRECT behaviour statically:
# none of the three launch-path resolvers may call os.UserConfigDir() directly
# (they must go through a darwin-aware resolver that maps to $XDG_CONFIG_HOME
# or $HOME/.config on darwin/ios).
set -u
cd "$(git rev-parse --show-toplevel)"
fail=0

# 0. Confirm the stdlib premise: darwin/ios branch of os.UserConfigDir appends
#    "Library/Application Support".
stdlib="$(go env GOROOT)/src/os/file.go"
if ! awk '/^func UserConfigDir\(/,/^}/' "$stdlib" | grep -q 'Library/Application Support'; then
  echo "premise not met: stdlib os.UserConfigDir has no Application Support branch"; exit 2
fi

check() { # file func
  local body
  body=$(awk -v f="$2" '$0 ~ "^func " f "\\(" {p=1} p {print} p && /^}/ {exit}' "$1")
  if [ -z "$body" ]; then echo "SKIP: $2 not found in $1"; return; fi
  if grep -n 'os\.UserConfigDir()' <<<"$body" >/dev/null; then
    echo "FAIL: $1 $2() calls os.UserConfigDir() directly -> ~/Library/Application Support on darwin"
    fail=1
  else
    echo "ok:   $1 $2()"
  fi
}
check internal/config/user_config_path.go currentUserConfigYamlCandidates  # runs from init() on every launch
check cmd/bd/prime.go resolveGlobalPrimePath                                # bd prime (SessionStart hook)
check cmd/bd/doctor/config_values.go findConfigPath                         # bd doctor
exit $fail
