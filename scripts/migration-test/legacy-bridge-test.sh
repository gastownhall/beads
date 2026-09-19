#!/usr/bin/env bash
set -euo pipefail

# Fast safety checks for the public sealed-copy SQLite bridge. Authentic
# historical binaries exercise its successful data path in the upgrade corpus.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BRIDGE="$SCRIPT_DIR/../migrate-legacy-to-current.sh"
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT

old="$tmp/old-bd"
new="$tmp/new-bd"
old_generic_export_marker="$tmp/old-generic-export"
new_legacy_sqlite_reader_marker="$tmp/new-legacy-sqlite-reader"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  '[ -z "${BRIDGE_TEST_MARKER:-}" ] || : > "$BRIDGE_TEST_MARKER"' \
  'for arg in "$@"; do' \
  '  [ "$arg" != version ] || { printf "%s\\n" "bd version 0.49.6"; exit 0; }' \
  'done' \
  'for arg in "$@"; do' \
  '  [ "$arg" != export ] || { : > "'"$old_generic_export_marker"'"; exit 91; }' \
  'done' \
  'exit 2' >"$old"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'case "$1" in' \
  '  migrate)' \
  '    test "$2" = legacy-sqlite' \
  '    source_db=""' \
  '    output=""' \
  '    shift 2' \
  '    while (($#)); do' \
  '      case "$1" in' \
  '        --source-db) source_db="$2"; shift 2 ;;' \
  '        --output) output="$2"; shift 2 ;;' \
  '        *) exit 2 ;;' \
  '      esac' \
  '    done' \
  '    case "$source_db" in /*/sealed-source/.beads/beads.db) ;; *) exit 2 ;; esac' \
  '    test "$source_db" != "$PWD/.beads/beads.db"' \
  '    test -n "$output"' \
  '    printf "%s\\n" "{\"id\":\"historical-1\",\"title\":\"Historical issue\",\"created_at\":\"2020-01-01T00:00:00.600000000Z\"}" > "$output"' \
  "    : > \"$new_legacy_sqlite_reader_marker\"" \
  '    ;;' \
  '  init)' \
  '    test -s .beads/issues.jsonl' \
  '    mkdir -p .beads/embeddeddolt/hist' \
  '    : > .beads/embeddeddolt/hist/storage' \
  '    printf "%s\\n" "{\"backend\":\"dolt\",\"dolt_mode\":\"embedded\"}" > .beads/metadata.json' \
  '    [ -z "${BRIDGE_TEST_MARKER:-}" ] || : > "$BRIDGE_TEST_MARKER"' \
  '    ;;' \
  '  export)' \
  '    [ "$2" = --all ] && [ "$3" = -o ] && [ -n "${4:-}" ] || exit 2' \
  '    printf "%s\\n" "{\"id\":\"historical-1\",\"title\":\"Historical issue\",\"created_at\":\"2020-01-01T00:00:01Z\"}" > "$4"' \
  '    ;;' \
  '  *) exit 2 ;;' \
  'esac' >"$new"
chmod +x "$old" "$new"
old_v0503="$tmp/old-v0503-bd"
sed 's/bd version 0.49.6/bd version 0.50.3/' "$old" >"$old_v0503"
chmod +x "$old_v0503"

# The release-binary version probe must not run an untrusted historical binary
# in the caller's workspace or with its HOME/Git/runtime environment.
probe_records="$tmp/release-probe-records"
probe_caller="$tmp/release-probe-caller"
probe_home="$tmp/release-probe-caller-home"
probe_tmp="$tmp/release-probe-tmp"
probe_binary="$tmp/release-probe-bd"
probe_hanging_binary="$tmp/release-probe-hanging-bd"
mkdir -p "$probe_records" "$probe_caller/.beads" "$probe_caller/bin" "$probe_home" "$probe_tmp"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf "%s\\n" "$PWD" > "'"$probe_records"'/cwd"' \
  'printf "%s\\n" "$HOME" > "'"$probe_records"'/home"' \
  'printf "%s\\n" "${XDG_CONFIG_HOME:-}" > "'"$probe_records"'/xdg-config"' \
  'printf "%s\\n" "${GIT_CONFIG_GLOBAL:-}" > "'"$probe_records"'/git-global"' \
  'printf "%s\\n" "$PATH" > "'"$probe_records"'/path"' \
  'printf "%s\\n" "${BEADS_DIR:-}" > "'"$probe_records"'/beads-dir"' \
  'printf "%s\\n" "${RELEASE_PROBE_SECRET:-}" > "'"$probe_records"'/secret"' \
  'printf "%s\\n" "bd version 0.49.6"' >"$probe_binary"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'while :; do sleep 1; done' >"$probe_hanging_binary"
chmod +x "$probe_binary" "$probe_hanging_binary"
(
  export HOME="$probe_home" XDG_CONFIG_HOME="$probe_home/config" XDG_CACHE_HOME="$probe_home/cache" TMPDIR="$probe_tmp"
  source "$SCRIPT_DIR/lib/binary.sh"
  (
    cd "$probe_caller"
    PATH="$probe_caller/bin:$PATH" BEADS_DIR="$probe_caller/.beads" RELEASE_PROBE_SECRET=caller-secret \
      verify_release_binary_version v0.49.6 "$probe_binary"
  )
  test "$(<"$probe_records/cwd")" != "$probe_caller"
  test "$(<"$probe_records/home")" != "$probe_home"
  test "$(<"$probe_records/xdg-config")" != "$probe_home/config"
  test "$(<"$probe_records/git-global")" = /dev/null
  test "$(<"$probe_records/path")" = /usr/bin:/bin
  test -z "$(<"$probe_records/beads-dir")"
  test -z "$(<"$probe_records/secret")"
  started=$(date +%s)
  if RELEASE_BINARY_VERSION_TIMEOUT=1 verify_release_binary_version v0.49.6 "$probe_hanging_binary" >/dev/null 2>&1; then
    printf 'release binary version probe accepted a hanging binary\n' >&2
    exit 1
  fi
  test "$(( $(date +%s) - started ))" -lt 8
  if compgen -G "$probe_tmp/bd-release-version-probe.*" >/dev/null; then
    printf 'release binary version probe left its isolated workspace behind\n' >&2
    exit 1
  fi
)

fingerprint() {
  (cd "$1" && find . -type f -print0 | LC_ALL=C sort -z | xargs -r -0 sha256sum) | sha256sum | awk '{print $1}'
}

run_lane() {
  local name=$1
  local source_version=$2
  local source_binary=$3
  local source="$tmp/$name-source"
  local destination="$tmp/$name-destination"
  mkdir -p "$source/.beads"
  printf 'SQLite format 3\000' >"$source/.beads/beads.db"
  case "$name" in
    sqlite)
      printf '%s\n' '{"backend":"sqlite"}' >"$source/.beads/metadata.json"
      printf '%s\n' '{"backend":"sqlite"}' >"$source/.beads/config.json"
      ;;
    sqlite-implicit)
      printf '%s\n' '{"database":"beads.db"}' >"$source/.beads/metadata.json"
      ;;
  esac
  local before
  before=$(fingerprint "$source/.beads")
  "$BRIDGE" --source "$source" --destination "$destination" --source-version "$source_version" \
    --old-bd "$source_binary" --new-bd "$new" --prefix hist
  [ "$(fingerprint "$source/.beads")" = "$before" ] || {
    printf '%s bridge mutated source\n' "$name" >&2
    exit 1
  }
  test -f "$destination/sealed-source/.beads/beads.db"
  test -f "$destination/cutover/.beads/issues.jsonl"
  jq -e '.backend == "dolt" and .dolt_mode == "embedded"' "$destination/cutover/.beads/metadata.json" >/dev/null
  test -d "$destination/cutover/.beads/embeddeddolt"
  test ! -L "$destination/cutover/.beads/embeddeddolt"
  test ! -e "$destination/cutover/.beads/beads.db"
  test ! -e "$destination/cutover/.beads/dolt"
}

run_lane sqlite v0.49.6 "$old"
run_lane sqlite-implicit v0.49.6 "$old"
run_lane sqlite-absent v0.50.3 "$old_v0503"
test ! -e "$old_generic_export_marker" || {
  printf 'direct bridge invoked the historical generic export\n' >&2
  exit 1
}
test -e "$new_legacy_sqlite_reader_marker" || {
  printf 'direct bridge did not invoke the candidate legacy SQLite reader\n' >&2
  exit 1
}

probe_mutator="$tmp/probe-mutator-bd"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'for arg in "$@"; do' \
  '  [ "$arg" != version ] || { : > version-probe-marker; printf "%s\\n" "bd version 0.49.6"; exit 0; }' \
  'done' \
  'for arg in "$@"; do' \
  '  [ "$arg" != export ] || { printf "%s\\n" "{\"id\":\"historical-1\",\"title\":\"Historical issue\",\"created_at\":\"2020-01-01T00:00:00.600000000Z\"}"; exit 0; }' \
  'done' \
  'exit 2' >"$probe_mutator"
chmod +x "$probe_mutator"
source="$tmp/probe-source"
destination="$tmp/probe-destination"
mkdir -p "$source/.beads"
printf 'SQLite format 3\000' >"$source/.beads/beads.db"
before=$(fingerprint "$source/.beads")
(
  cd "$source"
  "$BRIDGE" --source "$source" --destination "$destination" --source-version v0.49.6 \
    --old-bd "$probe_mutator" --new-bd "$new" --prefix hist
)
[ "$(fingerprint "$source/.beads")" = "$before" ] || {
  printf 'bridge version probe mutated source\n' >&2
  exit 1
}
test ! -e "$source/version-probe-marker" || {
  printf 'bridge version probe ran from source\n' >&2
  exit 1
}
test -e "$destination/probe/version-probe-marker" || {
  printf 'bridge version probe did not run from destination/probe\n' >&2
  exit 1
}

source="$tmp/version-source"
mkdir -p "$source/.beads"
printf 'SQLite format 3\000' >"$source/.beads/beads.db"
for version in v0.9.0 v0.9.2 v0.17.1 v0.49.5 v0.50.2 v0.51.0 v1.0.0; do
  if "$BRIDGE" --source "$source" --destination "$tmp/version-$version" \
      --source-version "$version" --old-bd "$old" --new-bd "$new" --prefix hist >/dev/null 2>&1; then
    printf 'bridge accepted unqualified source version %s\n' "$version" >&2
    exit 1
  fi
done

for descriptor in metadata config; do
  for shape in postgres mysql dolt unknown provider dsn postgres-dsn mysql-dsn dolt-host outside-sqlite; do
    source="$tmp/$descriptor-$shape-source"
    destination="$tmp/$descriptor-$shape-destination"
    marker="$tmp/$descriptor-$shape-old-invoked"
    mkdir -p "$source/.beads"
    printf 'SQLite format 3\000' >"$source/.beads/beads.db"
    case "$shape" in
      postgres|mysql|dolt|unknown) payload=$(printf '{"backend":"%s"}' "$shape") ;;
      provider) payload='{"backend":"sqlite","provider":"postgres"}' ;;
      dsn) payload='{"backend":"sqlite","dsn":"postgres://example"}' ;;
      postgres-dsn) payload='{"backend":"sqlite","postgres_dsn":"postgres://example"}' ;;
      mysql-dsn) payload='{"backend":"sqlite","mysql_dsn":"mysql://example"}' ;;
      dolt-host) payload='{"backend":"sqlite","dolt_server_host":"example.invalid"}' ;;
      outside-sqlite) payload='{"backend":"sqlite","sqlite_path":"../outside.db"}' ;;
    esac
    printf '%s\n' "$payload" >"$source/.beads/$descriptor.json"
    before=$(fingerprint "$source/.beads")
    if BRIDGE_TEST_MARKER="$marker" "$BRIDGE" --source "$source" --destination "$destination" \
        --source-version v0.49.6 --old-bd "$old" --new-bd "$new" --prefix hist >/dev/null 2>&1; then
      printf 'bridge accepted %s %s storage descriptor\n' "$descriptor" "$shape" >&2
      exit 1
    fi
    test ! -e "$marker" || {
      printf 'bridge executed the old binary before rejecting %s %s\n' "$descriptor" "$shape" >&2
      exit 1
    }
    [ "$(fingerprint "$source/.beads")" = "$before" ] || {
      printf 'bridge mutated rejected %s %s source\n' "$descriptor" "$shape" >&2
      exit 1
    }
  done
done

old_v017="$tmp/old-v017-bd"
sed 's/bd version 0.49.6/bd version 0.17.0/' "$old" >"$old_v017"
chmod +x "$old_v017"
source="$tmp/canonicalizer-gate-source"
marker="$tmp/canonicalizer-gate-invoked"
mkdir -p "$source/.beads"
printf 'SQLite format 3\000' >"$source/.beads/beads.db"
printf '%s\n' '{"backend":"dolt"}' >"$source/.beads/metadata.json"
if BRIDGE_TEST_MARKER="$marker" "$BRIDGE" --source "$source" --destination "$tmp/canonicalizer-gate-destination" \
    --source-version v0.17.0 --old-bd "$old_v017" --canonicalizer-bd "$old" \
    --new-bd "$new" --prefix hist >/dev/null 2>&1; then
  printf 'bridge accepted an ambiguous pre-canonicalizer source\n' >&2
  exit 1
fi
test ! -e "$marker" || {
  printf 'bridge executed an old or canonicalizer binary before source validation\n' >&2
  exit 1
}

source="$tmp/pre-canonical-source"
mkdir -p "$source/.beads"
printf 'SQLite format 3\000' >"$source/.beads/beads.db"
if "$BRIDGE" --source "$source" --destination "$tmp/pre-canonical-destination" \
    --source-version v0.17.0 --old-bd "$old" --new-bd "$new" --prefix hist >/dev/null 2>&1; then
  printf 'bridge accepted pre-v0.49.6 source without canonicalizer\n' >&2
  exit 1
fi

source="$tmp/containment-source"
mkdir -p "$source/.beads"
printf 'SQLite format 3\000' >"$source/.beads/beads.db"
ln -s "$source" "$tmp/source-alias"
if "$BRIDGE" --source "$source" --destination "$tmp/source-alias/nested-cutover" \
    --source-version v0.49.6 --old-bd "$old" --new-bd "$new" --prefix hist >/dev/null 2>&1; then
  printf 'bridge accepted a destination that resolves inside source\n' >&2
  exit 1
fi
test ! -e "$source/nested-cutover"

bad_new="$tmp/bad-new-bd"
candidate_was_invoked="$tmp/candidate-was-invoked"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'case "$1" in' \
  '  migrate)' \
  '    test "$2" = legacy-sqlite' \
  '    output=""' \
  '    shift 2' \
  '    while (($#)); do' \
  '      case "$1" in' \
  '        --source-db) shift 2 ;;' \
  '        --output) output="$2"; shift 2 ;;' \
  '        *) exit 2 ;;' \
  '      esac' \
  '    done' \
  '    printf "%s\\n" "{\"title\":\"missing id\"}" "{\"id\":\"historical-1\",\"title\":\"Historical issue\"}" > "$output"' \
  '    ;;' \
  '  init)' \
  "    : > \"$candidate_was_invoked\"" \
  '    exit 2' \
  '    ;;' \
  '  *) exit 2 ;;' \
  'esac' >"$bad_new"
chmod +x "$bad_new"
if "$BRIDGE" --source "$source" \
    --destination "$tmp/invalid-jsonl-destination" --source-version v0.49.6 --old-bd "$old" \
    --new-bd "$bad_new" --prefix hist >/dev/null 2>&1; then
  printf 'bridge accepted an invalid JSONL record\n' >&2
  exit 1
fi
test ! -e "$candidate_was_invoked" || {
  printf 'bridge invoked the candidate before validating every JSONL record\n' >&2
  exit 1
}

lossy_new="$tmp/lossy-new-bd"
candidate_init_marker="$tmp/lossy-candidate-init-invoked"
candidate_export_marker="$tmp/lossy-candidate-export-invoked"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'case "$1" in' \
  '  migrate)' \
  '    test "$2" = legacy-sqlite' \
  '    output=""' \
  '    shift 2' \
  '    while (($#)); do' \
  '      case "$1" in' \
  '        --source-db) shift 2 ;;' \
  '        --output) output="$2"; shift 2 ;;' \
  '        *) exit 2 ;;' \
  '      esac' \
  '    done' \
  '    printf "%s\\n" "{\"id\":\"historical-1\",\"title\":\"Historical issue\",\"created_at\":\"2020-01-01T00:00:00.600000000Z\"}" > "$output"' \
  '    ;;' \
  '  init)' \
  "    : > \"$candidate_init_marker\"" \
  '    test -s .beads/issues.jsonl' \
  '    mkdir -p .beads/embeddeddolt/hist' \
  '    : > .beads/embeddeddolt/hist/storage' \
  '    printf "%s\\n" "{\"backend\":\"dolt\",\"dolt_mode\":\"embedded\"}" > .beads/metadata.json' \
  '    ;;' \
  '  export)' \
  "    : > \"$candidate_export_marker\"" \
  '    [ "$2" = --all ] && [ "$3" = -o ] && [ -n "${4:-}" ] || exit 2' \
  '    printf "%s\\n" "{\"id\":\"historical-1\",\"title\":\"Lossy issue\",\"created_at\":\"2020-01-01T00:00:01Z\"}" > "$4"' \
  '    ;;' \
  '  *) exit 2 ;;' \
  'esac' >"$lossy_new"
chmod +x "$lossy_new"
source="$tmp/lossy-semantic-source"
destination="$tmp/lossy-semantic-destination"
mkdir -p "$source/.beads"
printf 'SQLite format 3\000' >"$source/.beads/beads.db"
before=$(fingerprint "$source/.beads")
if output=$("$BRIDGE" --source "$source" --destination "$destination" --source-version v0.49.6 \
    --old-bd "$old" --new-bd "$lossy_new" --prefix hist 2>&1); then
  printf 'bridge accepted semantically lossy candidate export\n' >&2
  exit 1
fi
grep -Fq 'candidate data does not semantically match' <<<"$output" || {
  printf 'bridge did not reject the lossy candidate export semantically:\n%s\n' "$output" >&2
  exit 1
}
test -e "$candidate_init_marker" || {
  printf 'bridge did not invoke candidate init for lossy export\n' >&2
  exit 1
}
test -e "$candidate_export_marker" || {
  printf 'bridge did not invoke candidate export for lossy export\n' >&2
  exit 1
}
[ "$(fingerprint "$source/.beads")" = "$before" ] || {
  printf 'bridge mutated source during lossy candidate verification\n' >&2
  exit 1
}

printf 'sealed-copy bridge smoke: PASS\n'
