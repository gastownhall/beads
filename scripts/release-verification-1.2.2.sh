#!/usr/bin/env bash
# Release verification for v1.2.2 (the recovery re-release of the tested 1.1
# line after the accidental v1.2.1 release).
#
# Verifies, with real binaries against real embedded-Dolt databases:
#   L1  candidate binary reports 1.2.2 and passes the schema skew unit tests
#   L2  fresh workspace: init + create/update/dep/comment/close/list round-trip
#   L3  reopening an existing v53 database is a no-op (no migration, no warnings)
#   L4  a v1.2.1-migrated database (v65) is refused with the incident recovery
#       message (not the generic stale-binary advice), non-zero exit
#   L5  BD_IGNORE_SCHEMA_SKEW=1 stopgap: reads on the v65 DB are byte-identical
#       to the pre-migration baseline; writes land correctly
#   L6  cursor rollback per docs/RECOVERY-1.2.1.md: candidate then opens the DB
#       cleanly (no env var, empty stderr) and can write
#   L7  optional events re-track per the runbook: dolt_ignore row gone, events
#       table committed again
#   L8  replay safety: a v1.2.1 binary re-migrates the rolled-back DB to v65
#       without error and with issue data intact (proves a future tested 1.2.x
#       upgrade works after recovery)
#   L9  release version-consistency gates pass in strict (tag-simulated) mode
#
# Usage:
#   BD_121=/path/to/bd-1.2.1 ./scripts/release-verification-1.2.2.sh
#
# BD_121 is a binary built from the v1.2.1 tag (CGO_ENABLED=1, -tags
# gms_pure_go). Without it, the migrated-database legs (L4-L8) are SKIPPED and
# the script fails, since those legs are the point of this release.
# Requires: go, dolt CLI, python3.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d /tmp/bd-122-verify.XXXXXX)"
export HOME="$WORK/home"   # isolate from user config/daemons
mkdir -p "$HOME"
PASS=0; FAIL=0; SKIP=0

say()  { printf '\n=== %s\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf 'PASS  %s\n' "$*"; }
bad()  { FAIL=$((FAIL+1)); printf 'FAIL  %s\n' "$*"; }
skip() { SKIP=$((SKIP+1)); printf 'SKIP  %s\n' "$*"; }

json_ids() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(sorted((i["id"],i["status"]) for i in d))'; }

say "Building candidate binary from this tree (CGO, gms_pure_go — release configuration)"
BD_NEW="$WORK/bd-1.2.2"
(cd "$ROOT" && CGO_ENABLED=1 go build -tags gms_pure_go -o "$BD_NEW" ./cmd/bd) || { bad "candidate build"; exit 1; }

say "L1: version + schema skew unit tests"
v="$("$BD_NEW" version 2>/dev/null | head -1)"
case "$v" in *1.2.2*) ok "L1a bd version reports 1.2.2 ($v)";; *) bad "L1a bd version = '$v', want 1.2.2";; esac
if (cd "$ROOT" && go test -tags gms_pure_go ./internal/storage/schema/ -run 'TestSchemaSkew' -count=1 >/dev/null 2>&1); then
    ok "L1b schema skew unit tests"
else
    bad "L1b schema skew unit tests"
fi

say "L2: fresh workspace round-trip"
WS="$WORK/ws"; mkdir -p "$WS"; cd "$WS" && git init -q .
if "$BD_NEW" init --non-interactive --stealth >/dev/null 2>&1; then ok "L2a init"; else bad "L2a init"; fi
A=$("$BD_NEW" create "Parent epic" -t epic -p 1 --json 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
B=$("$BD_NEW" create "Blocked child" -p 2 --json 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
C=$("$BD_NEW" create "Ready work" -p 3 --json 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
D=$("$BD_NEW" create "To be closed" -p 3 --json 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
"$BD_NEW" dep add "$B" "$C" >/dev/null 2>&1 \
    && "$BD_NEW" comment "$C" "baseline comment" >/dev/null 2>&1 \
    && "$BD_NEW" close "$D" >/dev/null 2>&1 \
    && ok "L2b create/dep/comment/close" || bad "L2b create/dep/comment/close"
n=$("$BD_NEW" list --json 2>/dev/null | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')
[ "$n" = "3" ] && ok "L2c list shows 3 open" || bad "L2c list shows $n open, want 3"
DBDIR="$WS/.beads/embeddeddolt/$(basename "$WS")"
cur=$(cd "$DBDIR" && dolt sql -r csv -q "SELECT MAX(version) FROM schema_migrations" 2>/dev/null | tail -1)
[ "$cur" = "53" ] && ok "L2d schema cursor at 53" || bad "L2d schema cursor at '$cur', want 53"

say "L3: reopen existing v53 DB — no migration, no warnings"
errout=$("$BD_NEW" list --json 2>&1 >/dev/null)
cur=$(cd "$DBDIR" && dolt sql -r csv -q "SELECT MAX(version) FROM schema_migrations" 2>/dev/null | tail -1)
[ -z "$errout" ] && [ "$cur" = "53" ] && ok "L3 reopen clean" || bad "L3 reopen: stderr='$errout' cursor=$cur"
"$BD_NEW" list --json 2>/dev/null | json_ids > "$WORK/baseline-ids.txt"
"$BD_NEW" ready --json 2>/dev/null > "$WORK/baseline-ready.json"

if [ -z "${BD_121:-}" ] || [ ! -x "${BD_121:-}" ]; then
    skip "L4-L8 require BD_121 (a v1.2.1 binary); set BD_121=/path/to/bd-1.2.1"
    FAIL=$((FAIL+1))  # the migrated-DB legs are mandatory for this release
else
    say "L4: v1.2.1 migrates the DB; candidate refuses with incident message"
    "$BD_121" list >/dev/null 2>&1
    cur=$(cd "$DBDIR" && dolt sql -r csv -q "SELECT MAX(version) FROM schema_migrations" 2>/dev/null | tail -1)
    [ "$cur" = "65" ] && ok "L4a 1.2.1 migrated cursor to 65" || bad "L4a cursor='$cur' after 1.2.1 run, want 65"
    out=$("$BD_NEW" list 2>&1); rc=$?
    [ $rc -ne 0 ] && ok "L4b candidate exits non-zero on v65 DB (rc=$rc)" || bad "L4b candidate exit code $rc, want non-zero"
    echo "$out" | grep -q 'RECOVERY-1.2.1.md' && ok "L4c incident recovery message shown" || bad "L4c incident message missing:\n$out"
    echo "$out" | grep -q 'install the latest release' && bad "L4d generic stale-binary advice still shown" || ok "L4d no stale-binary advice loop"

    say "L5: BD_IGNORE_SCHEMA_SKEW stopgap on v65"
    BD_IGNORE_SCHEMA_SKEW=1 "$BD_NEW" list --json 2>/dev/null | json_ids > "$WORK/skew-ids.txt"
    diff -q "$WORK/baseline-ids.txt" "$WORK/skew-ids.txt" >/dev/null && ok "L5a reads identical to baseline" || bad "L5a reads differ under skew bypass"
    E=$(BD_IGNORE_SCHEMA_SKEW=1 "$BD_NEW" create "written under bypass" -p 2 --json 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')
    [ -n "$E" ] && ok "L5b write works under bypass ($E)" || bad "L5b write failed under bypass"

    say "L6: cursor rollback per docs/RECOVERY-1.2.1.md"
    (cd "$DBDIR" \
        && dolt sql -q "DELETE FROM schema_migrations WHERE version > 53; CALL DOLT_ADD('schema_migrations'); CALL DOLT_COMMIT('-m', 'recovery: roll schema cursor back to v53 (accidental v1.2.1)', '--author', 'bd recovery <recovery@beads.invalid>')") >/dev/null 2>&1 \
        && ok "L6a rollback commands from the runbook" || bad "L6a rollback commands failed"
    errout=$("$BD_NEW" list --json 2>&1 >/dev/null); rc=$?
    [ $rc -eq 0 ] && [ -z "$errout" ] && ok "L6b candidate opens clean, empty stderr, no env var" || bad "L6b rc=$rc stderr='$errout'"
    "$BD_NEW" list --json 2>/dev/null | python3 -c 'import json,sys; d={i["id"] for i in json.load(sys.stdin)}' 2>/dev/null \
        && "$BD_NEW" close "$E" >/dev/null 2>&1 && ok "L6c write works post-rollback" || bad "L6c write failed post-rollback"

    say "L7: optional events re-track per the runbook"
    (cd "$DBDIR" \
        && dolt sql -q "DELETE FROM dolt_ignore WHERE pattern = 'events'; CALL DOLT_ADD('-f', 'events'); CALL DOLT_COMMIT('-m', 'recovery: re-track events table', '--author', 'bd recovery <recovery@beads.invalid>')") >/dev/null 2>&1 \
        && ok "L7a re-track commands from the runbook" || bad "L7a re-track commands failed"
    ign=$(cd "$DBDIR" && dolt sql -r csv -q "SELECT COUNT(*) FROM dolt_ignore WHERE pattern='events'" 2>/dev/null | tail -1)
    tracked=$(cd "$DBDIR" && dolt ls 2>/dev/null | grep -c '^events$\|	events$\| events$')
    [ "$ign" = "0" ] && [ "$tracked" -ge 1 ] && ok "L7b events versioned again" || bad "L7b dolt_ignore rows=$ign tracked=$tracked"

    say "L8: replay safety — 1.2.1 re-migrates the recovered DB without error"
    pre_ids=$("$BD_NEW" list --json 2>/dev/null | json_ids)
    "$BD_121" list >/dev/null 2>&1; rc=$?
    cur=$(cd "$DBDIR" && dolt sql -r csv -q "SELECT MAX(version) FROM schema_migrations" 2>/dev/null | tail -1)
    [ $rc -eq 0 ] && [ "$cur" = "65" ] && ok "L8a re-migration to 65 succeeded" || bad "L8a rc=$rc cursor=$cur"
    post_ids=$("$BD_121" list --json 2>/dev/null | json_ids)
    [ "$pre_ids" = "$post_ids" ] && ok "L8b issue data intact across re-migration" || bad "L8b data changed across re-migration"
fi

say "L9: release version-consistency gates (strict, tag-simulated)"
if (cd "$ROOT" && GITHUB_REF=refs/tags/v1.2.2 GITHUB_REF_TYPE=tag ./scripts/check-versions.sh >/dev/null 2>&1); then
    ok "L9 check-versions.sh strict mode"
else
    bad "L9 check-versions.sh strict mode (rerun without redirect for details)"
fi

say "Result: $PASS passed, $FAIL failed, $SKIP skipped   (workdir kept at $WORK)"
[ $FAIL -eq 0 ]
