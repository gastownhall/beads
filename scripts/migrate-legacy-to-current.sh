#!/usr/bin/env bash
set -euo pipefail

# Build a fresh current workspace from a sealed copy of a historical SQLite one.
# The original SOURCE is fingerprinted before and after every migration step and
# is never renamed, removed, or opened by either migration binary.

usage() {
    cat <<'EOF'
Usage: migrate-legacy-to-current.sh \
  --source DIR --destination DIR --source-version VERSION \
  --old-bd PATH --new-bd PATH --prefix PREFIX \
  [--canonicalizer-bd PATH]

SOURCE must contain one historical SQLite database under .beads. DESTINATION
must not exist and must resolve outside SOURCE.

Versions before v0.49.6 require a verified v0.49.6 binary supplied through
--canonicalizer-bd. The script opens only a sealed copy, exports canonical
JSONL, imports it into DESTINATION/cutover, and compares the candidate export
semantically before reporting success.
EOF
}

source=""
destination=""
source_version=""
old_bd=""
new_bd=""
canonicalizer_bd=""
prefix=""
while (($#)); do
    case "$1" in
        --source) source=${2:?}; shift 2 ;;
        --destination) destination=${2:?}; shift 2 ;;
        --source-version) source_version=${2:?}; shift 2 ;;
        --old-bd) old_bd=${2:?}; shift 2 ;;
        --new-bd) new_bd=${2:?}; shift 2 ;;
        --canonicalizer-bd) canonicalizer_bd=${2:?}; shift 2 ;;
        --prefix) prefix=${2:?}; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) printf 'unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
done

for value in source destination source_version old_bd new_bd prefix; do
    [ -n "${!value}" ] || { usage >&2; exit 2; }
done

die() {
    printf 'legacy migration: %s\n' "$*" >&2
    exit 1
}

require_regular_executable() {
    [ -f "$1" ] && [ ! -L "$1" ] && [ -x "$1" ] ||
        die "required executable must be a non-symlink regular file: $1"
}

canonical_file() {
    local path=$1 parent base
    parent=$(dirname "$path")
    base=$(basename "$path")
    parent=$(cd -P "$parent" && pwd -P) || return 1
    printf '%s/%s\n' "$parent" "$base"
}

canonical_new_path() {
    local path=${1%/} parent base
    [ -n "$path" ] || return 1
    base=${path##*/}
    [ "$base" != "." ] && [ "$base" != ".." ] && [ -n "$base" ] || return 1
    parent=${path%/*}
    [ "$parent" != "$path" ] || parent=.
    parent=$(cd -P "$parent" && pwd -P) || return 1
    printf '%s/%s\n' "$parent" "$base"
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        die 'no SHA-256 utility is available (need sha256sum or shasum)'
    fi
}

sha256_stream() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 | awk '{print $1}'
    else
        die 'no SHA-256 utility is available (need sha256sum or shasum)'
    fi
}

fingerprint() {
    (
        cd "$1"
        find . -type f -print | LC_ALL=C sort |
            while IFS= read -r file; do
                printf '%s\n' "$file"
                sha256_file "$file"
            done
    ) | sha256_stream
}

classify_source_version() {
    case "$1" in
        v0.9.1) printf 'materialize-and-canonicalize\n' ;;
        v0.17.0) printf 'canonicalize\n' ;;
        v0.49.6|v0.50.3) printf 'direct\n' ;;
        *) die "source version is not one of the authenticated SQLite releases: $1" ;;
    esac
}

verify_binary_version() {
    local expected=$1 binary=$2 output bare
    output=$(run_at "$destination/probe" "$binary" version 2>&1) || die "$binary does not run"
    bare=${expected#v}
    if [ "$expected" = v0.9.1 ]; then
        grep -Fq 'bd version 0.9.0 (dev)' <<< "$output" ||
            die "v0.9.1 source binary has unexpected version output: $output"
    else
        grep -Eq "(^|[[:space:]])${bare//./\\.}([[:space:]]|\\(|$)" <<< "$output" ||
            die "source binary reports an unexpected version: $output"
    fi
}

normalize_export() {
    jq -S -s '
        def stamp:
            if type != "string" or (test("\\.[0-9]+Z$") | not) then .
            else
                . as $raw
                | (
                    (sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601)
                    + (if ($raw | test("\\.[5-9][0-9]*Z$")) then 1 else 0 end)
                  )
                | todateiso8601
            end;
        def normalize_times:
            reduce ["created_at", "updated_at", "started_at", "closed_at",
                    "due_at", "defer_until", "compacted_at"][] as $key (.;
                if has($key) and .[$key] != null
                then .[$key] |= stamp
                else .
                end);
        def dependency:
            {
                issue_id: (.issue_id // ""),
                depends_on_id: (.depends_on_id // .id // ""),
                type: (.type // .dependency_type // "blocks"),
                created_at: ((.created_at // "") | stamp),
                created_by: (.created_by // "unknown"),
                metadata: (.metadata // "{}")
            };
        def comment:
            {
                issue_id: (.issue_id // ""),
                author: (.author // ""),
                text: (.text // ""),
                created_at: ((.created_at // "") | stamp)
            };
        map(
            if (._type // "issue") == "memory" then
                {kind: "memory", key: .key, value: .value}
            else
                del(._type, .comment_count, .dependency_count, .dependent_count)
                | normalize_times
                | .labels = ((.labels // []) | sort)
                | .dependencies = ((.dependencies // []) | map(dependency) |
                    sort_by(.issue_id, .depends_on_id, .type))
                | .comments = ((.comments // []) | map(comment) |
                    sort_by(.issue_id, .author, .text))
                | .kind = "issue"
            end
        )
        | sort_by(.kind, (.id // .key))
    ' "$1"
}

validate_sqlite_descriptor() {
    local descriptor=$1 name=$2
    [ ! -e "$descriptor" ] && [ ! -L "$descriptor" ] && return 0
    [ -f "$descriptor" ] && [ ! -L "$descriptor" ] ||
        die "source $name must be a non-symlink regular file"
    jq -e '
        type == "object" and
        ((.backend // "sqlite") == "sqlite") and
        ([keys[] | ascii_downcase |
            select(
                startswith("postgres_") or
                startswith("mysql_") or
                startswith("dolt_") or
                contains("provider") or
                contains("dsn") or
                contains("url")
            )
        ] | length == 0)
    ' "$descriptor" >/dev/null ||
        die "source $name must describe SQLite without alternate storage settings"
}

validate_sqlite_database_reference() {
    local descriptor=$1 name=$2 database_name=$3
    [ ! -e "$descriptor" ] && return 0
    jq -e --arg database_name "$database_name" '
        def local_database:
            . == null or . == "" or . == $database_name;
        (.database | local_database) and
        (.sqlite_path | local_database)
    ' "$descriptor" >/dev/null ||
        die "source $name points outside the sole local SQLite database"
}

verify_current_cutover() {
    local beads_dir=$1 metadata active_path
    metadata="$beads_dir/metadata.json"
    [ -f "$metadata" ] && [ ! -L "$metadata" ] ||
        die 'candidate did not write a non-symlink metadata.json'
    jq -e '.backend == "dolt" and .dolt_mode == "embedded"' "$metadata" >/dev/null ||
        die 'candidate did not select embedded Dolt'
    [ -d "$beads_dir/embeddeddolt" ] && [ ! -L "$beads_dir/embeddeddolt" ] ||
        die 'candidate did not create a non-symlink embedded Dolt directory'
    if ! (set +o pipefail; find "$beads_dir/embeddeddolt" -type f -print | grep -q .); then
        die 'candidate embedded Dolt directory contains no storage files'
    fi
    [ ! -e "$beads_dir/dolt" ] && [ ! -L "$beads_dir/dolt" ] ||
        die 'candidate left an active legacy Dolt directory'
    for active_path in "$beads_dir/"*.db; do
        [ ! -e "$active_path" ] && [ ! -L "$active_path" ] ||
            die 'candidate left an active SQLite database'
    done
    for active_path in "$beads_dir"/legacy-dolt-*; do
        [ ! -e "$active_path" ] && [ ! -L "$active_path" ] ||
            die 'candidate left a legacy Dolt directory'
    done
}

require_regular_executable "$old_bd"
require_regular_executable "$new_bd"
command -v jq >/dev/null 2>&1 || die 'jq is required'
command -v git >/dev/null 2>&1 || die 'git is required'

source=$(cd -P "$source" && pwd -P) || die "source directory is unavailable: $source"
destination=$(canonical_new_path "$destination") ||
    die 'destination parent must already exist and resolve to a physical directory'
old_bd=$(canonical_file "$old_bd") || die "cannot resolve old bd binary: $old_bd"
new_bd=$(canonical_file "$new_bd") || die "cannot resolve new bd binary: $new_bd"
source_version="v${source_version#v}"
strategy=$(classify_source_version "$source_version")

if [ "$strategy" != direct ]; then
    [ -n "$canonicalizer_bd" ] ||
        die "$source_version requires --canonicalizer-bd with a verified v0.49.6 binary"
    require_regular_executable "$canonicalizer_bd"
    canonicalizer_bd=$(canonical_file "$canonicalizer_bd") ||
        die "cannot resolve canonicalizer binary: $canonicalizer_bd"
fi

[ -d "$source/.beads" ] && [ ! -L "$source/.beads" ] ||
    die "source has no non-symlink .beads directory: $source"
[ ! -e "$source/.beads/dolt" ] && [ ! -L "$source/.beads/dolt" ] &&
    [ ! -e "$source/.beads/embeddeddolt" ] && [ ! -L "$source/.beads/embeddeddolt" ] ||
    die 'source contains Dolt data; this helper accepts historical SQLite only'
if (set +o pipefail; find "$source/.beads" -type l -print | grep -q .); then
    die 'source .beads contains a symlink; refusing to follow it'
fi
validate_sqlite_descriptor "$source/.beads/metadata.json" metadata.json
validate_sqlite_descriptor "$source/.beads/config.json" config.json

sqlite_database=""
sqlite_database_count=0
for candidate_database in "$source/.beads/"*.db; do
    [ -f "$candidate_database" ] || continue
    sqlite_database=$candidate_database
    sqlite_database_count=$((sqlite_database_count + 1))
done
[ "$sqlite_database_count" -eq 1 ] ||
    die 'source must contain exactly one historical SQLite *.db file'
cmp -s <(dd if="$sqlite_database" bs=16 count=1 2>/dev/null) \
    <(printf 'SQLite format 3\0') ||
    die "source database does not have a SQLite header: $sqlite_database"
sqlite_database_name=${sqlite_database##*/}
validate_sqlite_database_reference "$source/.beads/metadata.json" metadata.json "$sqlite_database_name"
validate_sqlite_database_reference "$source/.beads/config.json" config.json "$sqlite_database_name"

case "$destination/" in
    "$source/"*) die "destination must resolve outside source: $destination" ;;
esac
[ ! -e "$destination" ] && [ ! -L "$destination" ] ||
    die "destination already exists; refusing to overwrite: $destination"

source_before=$(fingerprint "$source/.beads")
mkdir -p "$destination/sealed-source" "$destination/cutover/.beads" \
    "$destination/home/config" "$destination/home/cache" "$destination/probe"
cp -Rp "$source/.beads" "$destination/sealed-source/.beads"
sealed_fingerprint=$(fingerprint "$destination/sealed-source/.beads")
[ "$sealed_fingerprint" = "$source_before" ] ||
    die 'sealed copy fingerprint does not match source'
[ "$(fingerprint "$source/.beads")" = "$source_before" ] ||
    die 'source changed while its sealed copy was made; stop and retry'

for repo in "$destination/sealed-source" "$destination/cutover"; do
    git -c core.hooksPath=.git/hooks -C "$repo" init --quiet
    git -C "$repo" config core.hooksPath .git/hooks
    git -C "$repo" config user.name legacy-migration
    git -C "$repo" config user.email legacy-migration@invalid
done

run_at() {
    local directory=$1
    shift
    (
        cd "$directory"
        env -i PATH="$PATH" HOME="$destination/home" \
            XDG_CONFIG_HOME="$destination/home/config" \
            XDG_CACHE_HOME="$destination/home/cache" \
            GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
            GIT_TERMINAL_PROMPT=0 BD_DISABLE_METRICS=1 \
            BD_DISABLE_EVENT_FLUSH=1 BD_NON_INTERACTIVE=1 \
            BEADS_NO_DAEMON=1 BEADS_DOLT_AUTO_START=0 \
            BRIDGE_TEST_MARKER="${BRIDGE_TEST_MARKER:-}" "$@"
    )
}

verify_binary_version "$source_version" "$old_bd"
if [ "$strategy" != direct ]; then
    verify_binary_version v0.49.6 "$canonicalizer_bd"
fi

export_jsonl="$destination/export.jsonl"
case "$strategy" in
    materialize-and-canonicalize)
        run_at "$destination/sealed-source" "$old_bd" --no-auto-import \
            export --format jsonl --output .beads/issues.jsonl >/dev/null
        run_at "$destination/sealed-source" "$canonicalizer_bd" \
            --no-daemon --no-auto-import init --force --prefix "$prefix" >/dev/null
        run_at "$destination/sealed-source" "$canonicalizer_bd" \
            --no-daemon --no-auto-import export --format jsonl > "$export_jsonl"
        ;;
    canonicalize)
        run_at "$destination/sealed-source" "$canonicalizer_bd" \
            --no-daemon --no-auto-import init --force --prefix "$prefix" >/dev/null
        run_at "$destination/sealed-source" "$canonicalizer_bd" \
            --no-daemon --no-auto-import export --format jsonl > "$export_jsonl"
        ;;
    direct)
        run_at "$destination/probe" "$new_bd" \
            migrate legacy-sqlite \
            --source-db "$destination/sealed-source/.beads/$sqlite_database_name" \
            --output "$export_jsonl"
        ;;
esac

[ -f "$export_jsonl" ] && [ ! -L "$export_jsonl" ] ||
    die 'historical export was not written as a non-symlink regular file'
# Only the direct strategy uses the candidate's authenticated SQLite reader.
# Older exporter failures must not be allowed to masquerade as an empty source.
if [ "$strategy" != direct ] && [ ! -s "$export_jsonl" ]; then
    die 'historical export was empty'
fi
jq -s -e '
    all(.[];
        type == "object" and
        (if (._type // "") == "memory"
         then (.key | type) == "string" and (.value | type) == "string"
         else (.id | type) == "string" and (.id | length) > 0
         end))
' "$export_jsonl" >/dev/null ||
    die 'historical export is not a valid JSONL bridge payload'
[ "$(fingerprint "$source/.beads")" = "$source_before" ] ||
    die 'historical source changed during canonical export'

cp -f "$export_jsonl" "$destination/cutover/.beads/issues.jsonl"
run_at "$destination/cutover" "$new_bd" init --from-jsonl --quiet \
    --non-interactive --skip-hooks --skip-agents --prefix "$prefix"
verify_current_cutover "$destination/cutover/.beads"

candidate_export="$destination/candidate-export.jsonl"
run_at "$destination/cutover" "$new_bd" export --all -o "$candidate_export" >/dev/null
[ -f "$candidate_export" ] && [ ! -L "$candidate_export" ] ||
    die 'candidate export was not written as a non-symlink regular file'
# A zero-record candidate is valid only for a zero-record expected export.
if [ -s "$export_jsonl" ] && [ ! -s "$candidate_export" ]; then
    die 'candidate export was empty after import'
fi
normalize_export "$export_jsonl" > "$destination/expected-normalized.json"
normalize_export "$candidate_export" > "$destination/candidate-normalized.json"
if ! cmp -s "$destination/expected-normalized.json" "$destination/candidate-normalized.json"; then
    diff -u "$destination/expected-normalized.json" \
        "$destination/candidate-normalized.json" >&2 || true
    die 'candidate data does not semantically match the canonical historical export'
fi
[ "$(fingerprint "$source/.beads")" = "$source_before" ] ||
    die 'historical source changed during candidate verification'

printf 'cutover created at %s/cutover\n' "$destination"
printf 'sealed source retained at %s/sealed-source; review before activating manually.\n' "$destination"
