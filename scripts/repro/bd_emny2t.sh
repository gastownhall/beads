#!/usr/bin/env bash
# bd-emny2t (GH#6330): integers past 2^53 in metadata must survive
# `bd update --set-metadata/--metadata` + `bd show` byte-exact, in --json and in
# the human METADATA block (fresh embedded store). Adopted from the c0_c1 repro
# lane (b30313784a98), extended with the human-output check.
set -euo pipefail
BD="$(cd "$(dirname "$0")/../.." && pwd)/bazel-bin/cmd/bd/bd_/bd"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
cd "$work"; git init -q .
scrub=(); for v in $(env | grep -oE '^(BEADS|BD)_[A-Z0-9_]*'); do scrub+=(-u "$v"); done
run() { env "${scrub[@]}" BD_DISABLE_METRICS=1 DO_NOT_TRACK=1 HOME="$work" XDG_CONFIG_HOME="$work/.config" "$BD" "$@"; }
run init --prefix t >/dev/null 2>&1
echo "store: $(cat .beads/metadata.json 2>/dev/null | tr -d '\n')"
id="$(run create "precision repro" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
run update "$id" --set-metadata big=1546130668394913822 >/dev/null
run update "$id" --metadata '{"num":1546130668394913822,"just_over":9007199254740993}' >/dev/null
out="$(run show "$id" --json)"
echo "$out" | sed -n '/"metadata"/,/}/p'
human="$(run show "$id")"
fail=0
echo "$out" | grep -Eq '"big": *"?1546130668394913822([^0-9]|$)' || { echo "FAIL: big rounded (--json)"; fail=1; }
echo "$out" | grep -Eq '"num": *1546130668394913822([^0-9]|$)' || { echo "FAIL: num rounded (--json)"; fail=1; }
echo "$out" | grep -Eq '"just_over": *9007199254740993([^0-9]|$)' || { echo "FAIL: just_over rounded (--json)"; fail=1; }
echo "$human" | grep -Eq 'big: 1546130668394913822([^0-9]|$)' || { echo "FAIL: big rounded (human)"; fail=1; }
echo "$human" | grep -Eq 'num: 1546130668394913822([^0-9]|$)' || { echo "FAIL: num rounded (human)"; fail=1; }
echo "$human" | grep -Eq 'just_over: 9007199254740993([^0-9]|$)' || { echo "FAIL: just_over rounded (human)"; fail=1; }
[ "$fail" -eq 0 ] && echo "OK: all digits intact in --json and human output"
exit $fail
