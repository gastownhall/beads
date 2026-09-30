#!/usr/bin/env bash
set -euo pipefail

# Authentic historical upgrades. Historical SQLite and server-Dolt use explicit
# export/import bridges; the reviewed embedded-Dolt release upgrades directly.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
source "$SCRIPT_DIR/lib/versions.sh"
source "$SCRIPT_DIR/lib/binary.sh"

readonly OP_TIMEOUT="${HISTORICAL_DOLT_E2E_TIMEOUT:-45}"
readonly RUN_ROOT="${HISTORICAL_DOLT_E2E_ROOT:-${TMPDIR:-/tmp}}"
workspace=""
public_bridge_destination=""
server_pid=""
server_port=""
sqlite_writer_pid=""
sqlite_writer_stop=""
keep_workspace=false
DOLT_BIN="${DOLT_BIN:-dolt}"

usage() {
    cat <<'EOF'
Usage: historical-dolt-upgrade-test.sh [--version VERSION]

Runs the authentic historical SQLite bridges (v0.9.1, v0.17.0, v0.49.6, v0.50.3), historical server-Dolt corpus
(v0.55.4, v0.56.1, v0.57.0, v0.62.0), and direct embedded-Dolt corpus
(v0.63.3, v1.0.0, v1.0.1, v1.1.0, v1.1.2, v1.2.2) against CANDIDATE_BIN. Every release archive is pinned and verified.
The wisp-capable sources (v1.0.1, v1.1.0, v1.1.2, v1.2.2) additionally run a wisp-plane lane in a fresh workspace,
which needs the pinned external Dolt runtime as a read-only oracle.
EOF
}

die() {
    keep_workspace=true
    printf 'historical-upgrade: %s\n' "$*" >&2
    if [ -n "$workspace" ]; then
        printf 'historical-upgrade: retained diagnostics at %s\n' "$workspace" >&2
    fi
    if [ -n "$public_bridge_destination" ]; then
        printf 'historical-upgrade: retained public bridge at %s\n' "$public_bridge_destination" >&2
    fi
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "required command is unavailable: $1"
}

verify_dolt_runtime() {
    local resolved output bare
    resolved=$(command -v "$DOLT_BIN") ||
        die "pinned external Dolt runtime ${DOLT_TEST_RUNTIME_VERSION} is unavailable: $DOLT_BIN"
    resolved=$(resolve_existing_path "$resolved") || die "cannot resolve Dolt runtime: $DOLT_BIN"
    output=$("$resolved" version 2>&1) ||
        die "pinned external Dolt runtime does not run: $resolved"
    bare="${DOLT_TEST_RUNTIME_VERSION#v}"
    if ! grep -Eq "(^|[^0-9])${bare//./\\.}([^0-9]|$)" <<< "$output"; then
        die "unpinned external Dolt runtime: $resolved reports '$output'; require ${DOLT_TEST_RUNTIME_VERSION} (CI pins the linux/amd64 archive, SHA-256 ${DOLT_TEST_RUNTIME_SHA256}; any native build reporting this version is accepted)"
    fi
    DOLT_BIN="$resolved"
}

declare -a SELECTED_VERSIONS=()
while (($#)); do
    case "$1" in
        --version)
            (($# >= 2)) || die '--version requires a release tag'
            SELECTED_VERSIONS+=("$2")
            shift 2
            ;;
        -h|--help) usage; exit 0 ;;
        *) die "unknown argument: $1 (use --help)" ;;
    esac
done
if ((${#SELECTED_VERSIONS[@]} == 0)); then
    SELECTED_VERSIONS=("$SOURCE_TAG_SQLITE_VERSION" "$PRE_CANONICAL_SQLITE_VERSION" "$CLASSIC_SQLITE_VERSION" "$CONFIGURED_SQLITE_VERSION" "${HISTORICAL_DOLT_VERSIONS[@]}" "${EMBEDDED_DOLT_VERSIONS[@]}")
fi
for version in "${SELECTED_VERSIONS[@]}"; do
    case " $SOURCE_TAG_SQLITE_VERSION $PRE_CANONICAL_SQLITE_VERSION $CLASSIC_SQLITE_VERSION $CONFIGURED_SQLITE_VERSION ${HISTORICAL_DOLT_VERSIONS[*]} ${EMBEDDED_DOLT_VERSIONS[*]} " in
        *" $version "*) ;;
        *) die "unqualified historical release: $version" ;;
    esac
done

require_command jq
require_command git
require_command timeout
require_command sha256sum
require_command python3
case "$(uname -s)-$(uname -m)" in
    Linux-x86_64|Darwin-arm64) ;;
    *) die 'the pinned authentic-binary corpus supports linux/amd64 or darwin/arm64 only' ;;
esac
[[ "$OP_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || die 'HISTORICAL_DOLT_E2E_TIMEOUT must be a positive number of seconds'
# The server-Dolt lanes need the pinned runtime to host their fixture; the
# wisp-plane lanes need the same binary as a read-only plane oracle.
for version in "${SELECTED_VERSIONS[@]}"; do
    case " ${HISTORICAL_DOLT_VERSIONS[*]} ${WISP_PLANE_VERSIONS[*]} " in
        *" $version "*) verify_dolt_runtime; break ;;
    esac
done

candidate="${CANDIDATE_BIN:-}"
if [ -z "$candidate" ]; then candidate=$(build_candidate); fi
candidate=$(resolve_existing_path "$candidate") || die 'candidate binary cannot be resolved'
[ -x "$candidate" ] || die "candidate binary is not executable: $candidate"

cleanup() {
    stop_sqlite_writer
    stop_historical_server
    if ! $keep_workspace; then
        [ -z "$workspace" ] || rm -rf -- "$workspace"
        [ -z "$public_bridge_destination" ] || rm -rf -- "$public_bridge_destination"
    fi
}

stop_sqlite_writer() {
    [ -n "$sqlite_writer_pid" ] || return 0
    : > "$sqlite_writer_stop"
    wait "$sqlite_writer_pid" 2>/dev/null || true
    sqlite_writer_pid=""
    sqlite_writer_stop=""
}
trap cleanup EXIT

isolated_env() {
    local auto_start="${BEADS_DOLT_AUTO_START:-0}"
    if [ "${ISOLATED_ENV_EXEC:-0}" = 1 ]; then
        exec env -i PATH="$PATH" HOME="$workspace/home" XDG_CONFIG_HOME="$workspace/home/config" \
            XDG_CACHE_HOME="$workspace/home/cache" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
            GIT_TERMINAL_PROMPT=0 BD_DISABLE_METRICS=1 BD_DISABLE_EVENT_FLUSH=1 \
            BD_NON_INTERACTIVE=1 BEADS_NO_DAEMON=1 BEADS_DOLT_AUTO_START="$auto_start" BD_AUTHOR="${BD_AUTHOR:-}" "$@"
    fi
    env -i PATH="$PATH" HOME="$workspace/home" XDG_CONFIG_HOME="$workspace/home/config" \
        XDG_CACHE_HOME="$workspace/home/cache" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
        GIT_TERMINAL_PROMPT=0 BD_DISABLE_METRICS=1 BD_DISABLE_EVENT_FLUSH=1 \
        BD_NON_INTERACTIVE=1 BEADS_NO_DAEMON=1 BEADS_DOLT_AUTO_START="$auto_start" BD_AUTHOR="${BD_AUTHOR:-}" "$@"
}

run_in_workspace() {
    run_in_directory "$workspace" "$@"
}

run_in_directory() {
    local directory="$1" bin="$2"
    shift 2
    (cd "$directory" && isolated_env timeout --kill-after=5s "$OP_TIMEOUT" "$bin" "$@")
}

start_historical_server() {
    local data_dir="$1" requested_port="${2:-}" attempt port log ready
    for ((attempt = 0; attempt < 40; attempt++)); do
        if [ -n "$requested_port" ]; then
            port="$requested_port"
        else
            port=$((30000 + ((RANDOM * 37 + BASHPID + attempt * 997) % 20000)))
        fi
        log="$workspace/dolt-$port.log"
        (
            cd "$data_dir"
            ISOLATED_ENV_EXEC=1 isolated_env "$DOLT_BIN" sql-server -H 127.0.0.1 -P "$port" --loglevel=warning
        ) >"$log" 2>&1 &
        server_pid=$!
        for ((ready = 0; ready < 50; ready++)); do
            kill -0 "$server_pid" 2>/dev/null || break
            if (exec 3<>"/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1; then
                sleep 0.1
                kill -0 "$server_pid" 2>/dev/null || break
                server_port="$port"
                return 0
            fi
            sleep 0.1
        done
        stop_historical_server
        [ -z "$requested_port" ] || break
    done
    die "could not start isolated pinned Dolt ${DOLT_TEST_RUNTIME_VERSION}; inspect $workspace"
}

stop_historical_server() {
    [ -n "$server_pid" ] || return 0
    kill -TERM "$server_pid" 2>/dev/null || true
    if ! timeout 10 bash -c 'while kill -0 "$1" 2>/dev/null; do sleep 0.1; done' _ "$server_pid"; then
        kill -KILL "$server_pid" 2>/dev/null || true
    fi
    wait "$server_pid" 2>/dev/null || true
    server_pid=""
    server_port=""
}

create_issue() {
    local bin="$1" title="$2" type="$3" priority="$4" output id
    output=$(run_in_workspace "$bin" create --title "$title" --type "$type" --priority "$priority" \
        --description 'Authentic historical upgrade fixture') || return 1
    id=$(sed -n 's/.*Created issue: \([^[:space:]]*\).*/\1/p' <<< "$output" | head -1)
    [ -n "$id" ] || return 1
    printf '%s\n' "$id"
}

create_historical_fixture() {
    local version="$1" source="$2" task blocker closed snapshot task_status=open task_type=task
    if [ "$version" = v1.0.0 ]; then
        run_in_workspace "$source" config set types.custom research >/dev/null || die "$version: could not configure custom type"
        task_status=review
        task_type=research
    fi
    task=$(create_issue "$source" "Historical dependency target $version" "$task_type" 2) || die "$version: could not create source task"
    if [ "$task_status" != open ]; then
        # v1.0.0's update validator cannot read category-annotated statuses,
        # although its config command stores them and its status command reads
        # them. Use its legacy spelling for this one update, then restore the
        # authentic final config the candidate must preserve.
        run_in_workspace "$source" config set status.custom review >/dev/null || die "$version: could not prepare source custom status"
        run_in_workspace "$source" update "$task" --status "$task_status" >/dev/null || die "$version: could not set source task status"
        run_in_workspace "$source" config set status.custom review:active >/dev/null || die "$version: could not restore source custom status"
    fi
    blocker=$(create_issue "$source" "Historical dependent $version" bug 1) || die "$version: could not create source dependent"
    closed=$(create_issue "$source" "Historical completed $version" task 3) || die "$version: could not create source completed issue"
    run_in_workspace "$source" dep add "$blocker" "$task" >/dev/null || die "$version: could not create source dependency"
    if is_classic_sqlite_version "$version"; then
        run_in_workspace "$source" dep add "$blocker" "$closed" --type related >/dev/null ||
            die "$version: could not create source related dependency"
    fi
    run_in_workspace "$source" label add "$task" historical-upgrade >/dev/null || die "$version: could not label source fixture issue"
    run_in_workspace "$source" comments add "$task" 'Historical comment must survive the upgrade.' --author historical-upgrade >/dev/null || die "$version: could not comment on source fixture issue"
    if [ "$version" = v0.62.0 ]; then
        run_in_workspace "$source" remember 'Historical v0.62 memory must survive the upgrade.' --key historical-upgrade-memory >/dev/null ||
            die "$version: could not create source memory fixture"
    fi
    run_in_workspace "$source" close "$closed" >/dev/null || die "$version: could not close source fixture issue"
    snapshot=$(run_in_workspace "$source" list --json -n 0 --all) || die "$version: could not read source fixture"
    jq -e --arg version "$version" --arg task "$task" --arg blocker "$blocker" --arg closed "$closed" --arg task_status "$task_status" --arg task_type "$task_type" '
        type == "array" and length == 3 and
        any(.[]; .id == $task and .title == ("Historical dependency target " + $version) and .status == $task_status and .issue_type == $task_type) and
        any(.[]; .id == $closed and .title == ("Historical completed " + $version) and .status == "closed") and
        any(.[]; .id == $blocker and .issue_type == "bug" and .priority == 1)
    ' <<< "$snapshot" >/dev/null || die "$version: source fixture fields are incomplete"
    run_in_workspace "$source" show "$blocker" --json > "$workspace/source-dependent.json" || die "$version: could not read source dependency"
    jq -e --arg task "$task" '(if type == "array" then .[0] else . end) | ((.dependencies // []) | any(.[]; (.id // .depends_on_id) == $task))' \
        "$workspace/source-dependent.json" >/dev/null || die "$version: source dependency is missing"
    if is_classic_sqlite_version "$version"; then
        jq -e --arg closed "$closed" '(if type == "array" then .[0] else . end) | ((.dependencies // []) | any(.[]; (.id // .depends_on_id) == $closed and (.dependency_type // .type) == "related"))' \
            "$workspace/source-dependent.json" >/dev/null || die "$version: source related dependency is missing"
    fi
    printf '%s\n%s\n%s\n' "$task" "$blocker" "$closed" > "$workspace/fixture-ids"
}

is_classic_sqlite_version() {
    [ "$1" = "$CLASSIC_SQLITE_VERSION" ] || [ "$1" = "$CONFIGURED_SQLITE_VERSION" ]
}

record_retained_legacy() {
    local version="$1"
    find "$workspace/.beads" -maxdepth 1 -type f -name "legacy-$version-*" -print0
    # Dolt may finish writing derived query statistics after sql-server exits.
    # Retain that directory, but byte-check the user database and configuration
    # rather than the disposable .dolt/stats cache.
    find "$workspace/.beads/legacy-dolt-$version" \
        -path '*/.dolt/stats' -prune -o -type f -print0
    printf '%s\0' "$workspace/.beads/historical-$version-export.jsonl"
    [ ! -f "$workspace/.beads/historical-$version-comments.jsonl" ] ||
        printf '%s\0' "$workspace/.beads/historical-$version-comments.jsonl"
}

record_retained_classic() {
    local version="$1"
    find "$workspace/.beads" -maxdepth 1 -type f -name '*.pre-migration' -print0
    printf '%s\0' "$workspace/classic-$version-current-reader.jsonl"
}

record_retained_v017() {
    find "$workspace/v0.17.0-source" -type f -print0
    printf '%s\0' "$workspace/.beads/v0.17.0-bridge-export.jsonl"
}

record_retained_v091() {
    find "$workspace/v0.9.1-source" -type f -print0
    printf '%s\0' "$workspace/.beads/v0.9.1-bridge-export.jsonl"
}

save_retained_digest() {
    local kind="$1" version="$2"
    "record_retained_$kind" "$version" | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | awk '{print $1}' > "$workspace/$kind.sha256"
}

verify_retained_digest() {
    local kind="$1" version="$2" actual
    actual=$("record_retained_$kind" "$version" | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | awk '{print $1}')
    [ "$actual" = "$(cat "$workspace/$kind.sha256")" ] || die "$version: explicit bridge changed a retained historical rollback artifact"
}

seed_legacy_issues_jsonl_sentinel() {
    local version="$1" sentinel="$workspace/.beads/issues.jsonl"
    [ "$version" = v0.55.4 ] || return 0
    [ ! -e "$sentinel" ] && [ ! -L "$sentinel" ] ||
        die "$version: authentic fixture unexpectedly contains issues.jsonl; refusing to overwrite source evidence"
    printf '%s\n' 'legacy-v0.55.4 issues.jsonl rollback sentinel' > "$sentinel"
    cp -f "$sentinel" "$workspace/legacy-v0.55.4-issues.jsonl.expected" ||
        die "$version: could not save issues.jsonl rollback sentinel"
}

verify_legacy_issues_jsonl_sentinel() {
    local version="$1"
    local retained="$workspace/.beads/legacy-$version-issues.jsonl"
    [ "$version" = v0.55.4 ] || return 0
    cmp -s "$workspace/legacy-v0.55.4-issues.jsonl.expected" "$retained" ||
        die "$version: residual issues.jsonl was not retained byte-for-byte as legacy-$version-issues.jsonl"
}

classic_source_fingerprint() {
    beads_dir_fingerprint "$workspace/.beads"
}

export_classic_sqlite_with_candidate() {
    local version="$1" output="$2" task ready before after
    task=$(sed -n '1p' "$workspace/fixture-ids")
    ready="$workspace/sqlite-wal-ready"
    sqlite_writer_stop="$workspace/sqlite-wal-stop"
    python3 - "$workspace/.beads/beads.db" "$task" "$ready" "$sqlite_writer_stop" <<'PY' &
import pathlib
import sqlite3
import sys
import time

database, issue_id, ready, stop = sys.argv[1:]
connection = sqlite3.connect(database)
if connection.execute("PRAGMA journal_mode=WAL").fetchone()[0].lower() != "wal":
    raise SystemExit("could not enable WAL")
connection.execute("PRAGMA wal_autocheckpoint=0")
connection.execute(
    "UPDATE issues SET notes = ?, due_at = ? WHERE id = ?",
    ("Committed WAL data survived the current reader.", "2026-01-04T05:06:07.123456789Z", issue_id),
)
connection.execute(
    "UPDATE comments SET created_at = ? WHERE issue_id = ?",
    ("2026-01-07T11:12:13.876543210Z", issue_id),
)
connection.commit()
pathlib.Path(ready).touch()
while not pathlib.Path(stop).exists():
    time.sleep(0.05)
connection.close()
PY
    sqlite_writer_pid=$!
    for _ in {1..100}; do
        [ -e "$ready" ] && break
        kill -0 "$sqlite_writer_pid" 2>/dev/null ||
            die "$version: SQLite WAL writer exited before becoming ready"
        sleep 0.05
    done
    [ -e "$ready" ] && [ -s "$workspace/.beads/beads.db-wal" ] ||
        die "$version: committed WAL fixture was not created"

    before=$(classic_source_fingerprint) ||
        die "$version: could not fingerprint committed WAL source"
    run_in_workspace "$candidate" migrate legacy-sqlite \
        --source-db "$workspace/.beads/beads.db" --output "$output" ||
        die "$version: current legacy SQLite reader failed"
    after=$(classic_source_fingerprint) ||
        die "$version: could not resnapshot committed WAL source"
    [ "$after" = "$before" ] ||
        die "$version: current legacy SQLite reader changed its source"
    jq -s -e --arg task "$task" '
        length == 3 and
        any(.[]; .id == $task and .notes == "Committed WAL data survived the current reader.")
    ' "$output" >/dev/null ||
        die "$version: current reader omitted committed WAL data"
    stop_sqlite_writer
}

# GNU find's `-printf '%p\0%y\0%l\0'` (path, type letter, symlink target) has
# no BSD find equivalent — BSD find has no -printf action at all. This
# reproduces its exact three NUL-terminated fields for one already-found
# entry, checking -L before -f/-d so a symlink is classified 'l' rather than
# by whatever it points to (matching find's own unfollowed-by-default %y).
describe_entry_portable() {
    local entry="$1" type target=''
    if [ -L "$entry" ]; then
        type=l
        target=$(readlink -- "$entry" 2>/dev/null) || target=''
    elif [ -d "$entry" ]; then
        type=d
    elif [ -f "$entry" ]; then
        type=f
    elif [ -p "$entry" ]; then
        type=p
    elif [ -S "$entry" ]; then
        type=s
    elif [ -b "$entry" ]; then
        type=b
    elif [ -c "$entry" ]; then
        type=c
    else
        type='?'
    fi
    printf '%s\0%s\0%s\0' "$entry" "$type" "$target"
}

beads_dir_fingerprint() {
    local beads_dir="$1"
    (
        cd "$beads_dir" || return 1
        while IFS= read -r -d '' entry; do
            if [ "$OS" = linux ]; then
                find "$entry" -maxdepth 0 -printf '%p\0%y\0%l\0'
            else
                describe_entry_portable "$entry"
            fi
            if [ -f "$entry" ] && [ ! -L "$entry" ]; then
                sha256sum -- "$entry"
            fi
        done < <(find . -mindepth 1 -print0 | LC_ALL=C sort -z)
    ) | sha256sum | awk '{print $1}'
}

run_v017_source() {
    local source="$1"
    shift
    run_in_workspace "$source" --no-daemon --no-auto-import "$@"
}

run_v091_source() {
    local source="$1"
    shift
    run_in_workspace "$source" --no-auto-import "$@"
}

create_v091_fixture() {
    local source="$1" task=hist091-1 blocker=hist091-2 closed=hist091-3 target_detail closed_detail
    run_v091_source "$source" create 'Historical dependency target v0.9.1' --id "$task" \
        --description 'v0.9.1 source description' --design 'v0.9.1 source design' \
        --acceptance 'v0.9.1 source acceptance' --type feature --priority 2 \
        --labels historical-upgrade,v091-source >/dev/null || die 'v0.9.1: could not create source target'
    run_v091_source "$source" create 'Historical dependent v0.9.1' --id "$blocker" \
        --description 'v0.9.1 dependent description' --type bug --priority 1 >/dev/null || die 'v0.9.1: could not create source dependent'
    run_v091_source "$source" create 'Historical completed v0.9.1' --id "$closed" \
        --description 'v0.9.1 closed description' --type chore --priority 3 >/dev/null || die 'v0.9.1: could not create source closed issue'
    run_v091_source "$source" dep add "$blocker" "$task" >/dev/null || die 'v0.9.1: could not create source blocks dependency'
    run_v091_source "$source" dep add "$blocker" "$closed" --type related >/dev/null || die 'v0.9.1: could not create source related dependency'
    run_v091_source "$source" close "$closed" --reason 'v0.9.1 historical closure' >/dev/null || die 'v0.9.1: could not close source fixture issue'
    target_detail=$(run_v091_source "$source" show "$task" --json) || die 'v0.9.1: could not read source target'
    closed_detail=$(run_v091_source "$source" show "$closed" --json) || die 'v0.9.1: could not read source closed issue'
    jq -e --arg task "$task" '.id == $task and .description == "v0.9.1 source description" and .design == "v0.9.1 source design" and .acceptance_criteria == "v0.9.1 source acceptance" and .issue_type == "feature" and .priority == 2 and (.labels | sort == ["historical-upgrade", "v091-source"])' <<< "$target_detail" >/dev/null || die 'v0.9.1: source target fields are incomplete'
    jq -e --arg closed "$closed" '.id == $closed and .status == "closed" and .closed_at != null' <<< "$closed_detail" >/dev/null || die 'v0.9.1: source closed state or timestamp is incomplete'
    printf '%s\n%s\n%s\n' "$task" "$blocker" "$closed" > "$workspace/fixture-ids"
}

create_v017_issue() {
    local source="$1" title="$2" type="$3" priority="$4" output id
    output=$(run_v017_source "$source" create --title "$title" --type "$type" --priority "$priority") || return 1
    id=$(sed -n 's/.*Created issue: \([^[:space:]]*\).*/\1/p' <<< "$output" | head -1)
    [ -n "$id" ] || return 1
    printf '%s\n' "$id"
}

create_v017_fixture() {
    local source="$1" task blocker closed snapshot comments
    task=$(create_v017_issue "$source" 'Historical dependency target v0.17.0' task 2) || die 'v0.17.0: could not create source task'
    blocker=$(create_v017_issue "$source" 'Historical dependent v0.17.0' bug 1) || die 'v0.17.0: could not create source dependent'
    closed=$(create_v017_issue "$source" 'Historical completed v0.17.0' task 3) || die 'v0.17.0: could not create source completed issue'
    run_v017_source "$source" dep add "$blocker" "$task" >/dev/null || die 'v0.17.0: could not create source dependency'
    run_v017_source "$source" dep add "$blocker" "$closed" --type related >/dev/null || die 'v0.17.0: could not create source related dependency'
    run_v017_source "$source" label add "$task" historical-upgrade >/dev/null || die 'v0.17.0: could not label source fixture issue'
    BD_AUTHOR=historical-upgrade run_v017_source "$source" comments add "$task" 'Historical comment must survive the upgrade.' >/dev/null || die 'v0.17.0: could not comment on source fixture issue'
    run_v017_source "$source" close "$closed" >/dev/null || die 'v0.17.0: could not close source fixture issue'
    snapshot=$(run_v017_source "$source" list --json -n 0) || die 'v0.17.0: could not read source fixture'
    jq -e --arg task "$task" --arg blocker "$blocker" --arg closed "$closed" '
        type == "array" and length == 3 and
        any(.[]; .id == $task and .labels == ["historical-upgrade"]) and
        any(.[]; .id == $blocker and .issue_type == "bug" and .priority == 1) and
        any(.[]; .id == $closed and .status == "closed")
    ' <<< "$snapshot" >/dev/null || die 'v0.17.0: source fixture fields are incomplete'
    comments=$(run_v017_source "$source" comments "$task" --json) || die 'v0.17.0: could not read source comment'
    jq -e 'length == 1 and .[0].author == "historical-upgrade" and .[0].text == "Historical comment must survive the upgrade."' \
        <<< "$comments" >/dev/null || die 'v0.17.0: source comment is incomplete'
    printf '%s\n%s\n%s\n' "$task" "$blocker" "$closed" > "$workspace/fixture-ids"
}

legacy_server_source_fingerprint() {
    legacy_server_source_fingerprint_at "$workspace/.beads"
}

legacy_server_source_fingerprint_at() {
    local beads_dir="$1"
    # The server may update its derived query statistics while stopping. Every
    # other source and metadata byte must remain unchanged after refusal.
    (
        cd "$beads_dir" || return 1
        find . -path './dolt/.dolt/stats' -prune -o -type f -print0 |
            LC_ALL=C sort -z | xargs -r -0 sha256sum
    ) | sha256sum | awk '{print $1}'
}

legacy_server_runtime_artifact_inventory() {
    local artifact
    (
        cd "$workspace/.beads" || return 1
        for artifact in dolt-server.pid dolt-server.port dolt-server.lock dolt-server.log daemon.pid daemon.log daemon.lock bd.sock embeddeddolt; do
            [ ! -e "$artifact" ] && [ ! -L "$artifact" ] || find "$artifact" -print0
        done | LC_ALL=C sort -z
    ) | sha256sum | awk '{print $1}'
}

verify_legacy_server_refusal_before_bridge() {
    local version="$1" before after runtime_before runtime_after output restart_port

    # Stop the real server first: a connection failure is not evidence that
    # the candidate recognized this historical workspace.
    restart_port="$server_port"
    [ -n "$restart_port" ] || die "$version: historical server has no restart port"
    stop_historical_server
    before=$(legacy_server_source_fingerprint) || die "$version: could not snapshot historical server-Dolt source"
    runtime_before=$(legacy_server_runtime_artifact_inventory) || die "$version: could not snapshot historical runtime artifacts"
    output="$workspace/candidate-legacy-refusal.out"
    if (
        cd "$workspace"
        export BEADS_DOLT_AUTO_START=1
        isolated_env timeout --kill-after=5s "$OP_TIMEOUT" "$candidate" list
    ) > "$output" 2>&1; then
        die "$version: candidate list accepted a historical server-Dolt workspace"
    fi
    grep -Fq 'explicit migration is required' "$output" ||
        die "$version: candidate list did not refuse with explicit migration is required"
    after=$(legacy_server_source_fingerprint) || die "$version: could not resnapshot historical server-Dolt source"
    [ "$after" = "$before" ] || die "$version: candidate list changed historical server-Dolt source before refusal"
    runtime_after=$(legacy_server_runtime_artifact_inventory) || die "$version: could not resnapshot historical runtime artifacts"
    [ "$runtime_after" = "$runtime_before" ] || die "$version: candidate list created or changed a runtime artifact before refusal"
    start_historical_server "$workspace/.beads/dolt" "$restart_port"
}

export_source_jsonl() {
    local version="$1" source="$2" output="$3"
    case "$version" in
        v0.49.6|v0.50.3|v0.55.4)
            run_in_workspace "$source" export --format jsonl > "$output" ||
                die "$version: historical export failed"
            ;;
        v0.56.1)
            export_v0561_with_v0620 "$source" "$output"
            ;;
        v0.57.0|v0.62.0)
            # These releases export JSONL by default and no longer accept the
            # earlier --format flag. Their exporter also records only comment
            # counts, so capture the comment rows through the stable comments
            # command and add them to the issue records accepted by bd import.
            run_in_workspace "$source" export > "$output" ||
                die "$version: historical export failed"
            supplement_historical_comments "$version" "$source" "$output"
            ;;
        *)
            die "$version: no reviewed historical export invocation"
            ;;
    esac
    jq -s -e --arg version "$version" '
        ([.[] | select(._type != "memory")] | length == 3 and all(.[]; type == "object" and (.id | type) == "string")) and
        ($version != "v0.62.0" or any(.[]; ._type == "memory" and .key == "historical-upgrade-memory" and .value == "Historical v0.62 memory must survive the upgrade."))
    ' "$output" >/dev/null ||
        die "$version: historical export is not a three-issue JSONL bridge input"
}

export_v0561_with_v0620() {
    local source="$1" output="$2" bridge bridge_output canonicalizer restart_port original_fingerprint

    # v0.56.1 has no export command. The pinned v0.62.0 canonicalizer covers
    # the reviewed v0.56.1-v0.62.0 server sources, but opens only this
    # disposable copy; the real v0.56.1 source stays sealed for rollback.
    restart_port="$server_port"
    [ -n "$restart_port" ] || die 'v0.56.1: historical server has no restart port'
    stop_historical_server
    original_fingerprint=$(legacy_server_source_fingerprint) ||
        die 'v0.56.1: could not snapshot original source before exporter bridge'
    bridge="$workspace/v0.56.1-export-bridge"
    mkdir -p "$bridge" || die 'v0.56.1: could not create disposable exporter workspace'
    cp -af -- "$workspace/.beads" "$bridge/.beads" ||
        die 'v0.56.1: could not copy source into disposable exporter workspace'
    bridge_output="$bridge/historical-v0.56.1-export.jsonl"
    canonicalizer=$(download_verified_release_binary v0.62.0) ||
        die 'v0.56.1: verified v0.62.0 canonicalizer is unavailable'
    start_historical_server "$bridge/.beads/dolt" "$restart_port"
    run_in_directory "$bridge" "$canonicalizer" export --all > "$bridge_output" ||
        die 'v0.56.1: v0.62.0 canonicalizer bridge failed'
    supplement_historical_comments v0.56.1 "$canonicalizer" "$bridge_output" "$bridge" "$bridge/.beads"
    stop_historical_server
    [ "$(legacy_server_source_fingerprint)" = "$original_fingerprint" ] ||
        die 'v0.56.1: exporter bridge changed original historical source'
    cp -f "$bridge_output" "$output" || die 'v0.56.1: could not retain bridged export'
    cp -f "$bridge/.beads/historical-v0.56.1-comments.jsonl" \
        "$workspace/.beads/historical-v0.56.1-comments.jsonl" ||
        die 'v0.56.1: could not retain bridged comment audit'
}

supplement_historical_comments() {
    local version="$1" source="$2" output="$3" directory="${4:-$workspace}" scratch="${5:-$workspace/.beads}"
    local records="$scratch/historical-$version-comments.jsonl"
    local id comments map enriched
    : > "$records"
    while IFS= read -r id; do
        comments=$(run_in_directory "$directory" "$source" comments "$id" --json) ||
            die "$version: historical comment export failed for $id"
        jq -e 'type == "array"' <<< "$comments" >/dev/null ||
            die "$version: historical comments for $id are not a JSON array"
        jq -cn --arg id "$id" --argjson comments "$comments" \
            '{id: $id, comments: $comments}' >> "$records"
    done < <(jq -r 'select(._type != "memory") | .id' "$output")
    map="$scratch/historical-comments-map.json"
    enriched="$scratch/historical-export-with-comments.jsonl"
    jq -s 'reduce .[] as $row ({}; .[$row.id] = $row.comments)' "$records" > "$map"
    jq -c --slurpfile comments "$map" \
        'if ._type == "memory" then . else .comments = ($comments[0][.id] // []) end' "$output" > "$enriched"
    mv -f "$enriched" "$output"
}

migrate_schema_current() {
    local version="$1" label="$2" output
    output="$workspace/migrate-$label.out"
    run_in_workspace "$candidate" migrate schema > "$output" || die "$version: schema migration $label failed"
    # The candidate's latest schema version advances as new migrations land
    # (v59 -> v61 -> ...), so assert the no-op *shape* rather than a hardcoded
    # version that silently goes stale on every schema bump and reddens this
    # harness. A genuine no-op prints exactly "✓ Schema already at v<N>" on a
    # single line; an incomplete upgrade would instead print
    # "✓ Applied <n> schema migration(s); ...", which this exact-shape check
    # rejects.
    { [ "$(wc -l < "$output")" -eq 1 ] && grep -Eqx '✓ Schema already at v[0-9]+' "$output"; } ||
        die "$version: schema migration $label did not report the exact no-op output"
}

verify_empty_public_sqlite_bridge() {
    local version="$1" source="$2" before after export_file
    public_bridge_destination="${workspace}.empty-public-bridge"
    [ ! -e "$public_bridge_destination" ] && [ ! -L "$public_bridge_destination" ] ||
        die "$version: empty public bridge destination already exists"
    before=$(classic_source_fingerprint) ||
        die "$version: could not fingerprint empty SQLite source before public bridge"
    if ! timeout --kill-after=5s "$((OP_TIMEOUT * 4))" \
        "$PROJECT_ROOT/scripts/migrate-legacy-to-current.sh" \
        --source "$workspace" --destination "$public_bridge_destination" \
        --source-version "$version" --old-bd "$source" --new-bd "$candidate" \
        --prefix histclassic > "$workspace/empty-public-bridge.out" \
        2> "$workspace/empty-public-bridge.err"; then
        die "$version: empty public sealed-copy bridge failed"
    fi
    after=$(classic_source_fingerprint) ||
        die "$version: could not fingerprint empty SQLite source after public bridge"
    [ "$after" = "$before" ] ||
        die "$version: empty public bridge changed the historical source"
    jq -e '.backend == "dolt" and .dolt_mode == "embedded"' \
        "$public_bridge_destination/cutover/.beads/metadata.json" >/dev/null ||
        die "$version: empty public bridge did not create an embedded cutover"
    [ -d "$public_bridge_destination/cutover/.beads/embeddeddolt" ] ||
        die "$version: empty public bridge did not create embedded storage"
    for export_file in export.jsonl candidate-export.jsonl; do
        [ -f "$public_bridge_destination/$export_file" ] &&
            [ ! -L "$public_bridge_destination/$export_file" ] &&
            [ ! -s "$public_bridge_destination/$export_file" ] ||
            die "$version: empty public bridge $export_file was not a zero-byte regular file"
        jq -s -e 'length == 0' "$public_bridge_destination/$export_file" >/dev/null ||
            die "$version: empty public bridge $export_file did not represent zero records"
    done
    for export_file in expected-normalized.json candidate-normalized.json; do
        jq -e 'type == "array" and length == 0' "$public_bridge_destination/$export_file" >/dev/null ||
            die "$version: empty public bridge $export_file was not []"
    done
    rm -rf -- "$public_bridge_destination"
    public_bridge_destination=""
}

verify_public_sqlite_bridge() {
    local version="$1" source="$2" prefix="$3" before after canonicalizer
    local -a args
    public_bridge_destination="${workspace}.public-bridge"
    [ ! -e "$public_bridge_destination" ] && [ ! -L "$public_bridge_destination" ] ||
        die "$version: public bridge destination already exists"

    before=$(classic_source_fingerprint) ||
        die "$version: could not fingerprint source before public bridge"
    args=(
        --source "$workspace"
        --destination "$public_bridge_destination"
        --source-version "$version"
        --old-bd "$source"
        --new-bd "$candidate"
        --prefix "$prefix"
    )
    case "$version" in
        "$SOURCE_TAG_SQLITE_VERSION"|"$PRE_CANONICAL_SQLITE_VERSION")
            canonicalizer=$(download_verified_release_binary "$CLASSIC_SQLITE_VERSION") ||
                die "$version: verified v0.49.6 public-bridge canonicalizer is unavailable"
            args+=(--canonicalizer-bd "$canonicalizer")
            ;;
    esac

    if ! timeout --kill-after=5s "$((OP_TIMEOUT * 4))" \
        "$PROJECT_ROOT/scripts/migrate-legacy-to-current.sh" "${args[@]}" \
        > "$workspace/public-bridge.out" 2> "$workspace/public-bridge.err"; then
        die "$version: public sealed-copy bridge failed"
    fi
    after=$(classic_source_fingerprint) ||
        die "$version: could not fingerprint source after public bridge"
    [ "$after" = "$before" ] ||
        die "$version: public sealed-copy bridge changed the historical source"
    [ -s "$public_bridge_destination/candidate-export.jsonl" ] ||
        die "$version: public bridge produced no candidate audit export"
    jq -s -e '
        map(select((._type // "issue") != "memory")) |
        length == 3 and all(.[]; (.id | type) == "string" and (.id | length) > 0)
    ' "$public_bridge_destination/candidate-export.jsonl" >/dev/null ||
        die "$version: public bridge did not preserve the exact three-issue fixture"
}

explicit_dolt_upgrade() {
    local version="$1" source="$2"
    local export_file="$workspace/.beads/historical-$version-export.jsonl"
    export_source_jsonl "$version" "$source" "$export_file"
    stop_historical_server
    mv -f "$workspace/.beads/dolt" "$workspace/.beads/legacy-dolt-$version" || die "$version: could not retain legacy Dolt data"
    for file in "${LEGACY_DOLT_ROLLBACK_FILES[@]}"; do
        [ ! -e "$workspace/.beads/$file" ] || mv -f "$workspace/.beads/$file" "$workspace/.beads/legacy-$version-$file"
    done
    save_retained_digest legacy "$version"
    cp -f "$export_file" "$workspace/.beads/issues.jsonl"
    run_in_workspace "$candidate" init --from-jsonl --quiet --skip-hooks --skip-agents --prefix "hist${version//[^0-9]/}" ||
        die "$version: candidate could not initialize from historical export"
    migrate_schema_current "$version" first
}

preserve_classic_rollback() {
    local version="$1" file source backup
    for file in "${CLASSIC_SQLITE_ROLLBACK_FILES[@]}"; do
        source="$workspace/.beads/$file"
        backup="$source.pre-migration"
        [ ! -L "$source" ] && [ ! -L "$backup" ] || die "$version: classic rollback artifacts may not be symlinks"
        [ ! -e "$source" ] || { [ ! -e "$backup" ] || cmp -s "$source" "$backup" || die "$version: conflicting rollback artifact $backup"; cp -pf "$source" "$backup"; }
    done
}

run_classic_sqlite_upgrade() {
    local version="$1" source before after output export_file="$workspace/classic-$1-current-reader.jsonl" file task reexport reader_times current_times
    local timestamp_projection='map({
        id, created_at, updated_at, closed_at, compacted_at, due_at, defer_until,
        dependency_created_at: ((.dependencies // []) | map(.created_at) | sort),
        comment_created_at: ((.comments // []) | map(.created_at) | sort)
    }) | sort_by(.id)'
    printf '\n● Historical SQLite upgrade: %s → candidate\n' "$version"
    source=$(download_verified_release_binary "$version") || die "$version: verified release is unavailable"
    if [ "$version" = "$CONFIGURED_SQLITE_VERSION" ]; then
        run_in_workspace "$source" init --backend sqlite --quiet --prefix histclassic --skip-hooks || die "$version: source init failed"
        [ -s "$workspace/.beads/beads.db" ] || die "$version: source did not create a nonempty beads.db"
        jq -e '.database == "beads.db" and .jsonl_export == "issues.jsonl" and .backend == "sqlite"' "$workspace/.beads/metadata.json" >/dev/null ||
            die "$version: source metadata is not the reviewed configured-SQLite shape"
        [ "$(tr -d '[:space:]' < "$workspace/.beads/.local_version")" = "0.50.3" ] || die "$version: source version witness is not stable"
    else
        # v0.49.6 predates --non-interactive; its quiet init is non-prompting in
        # this isolated workspace. The candidate bridge remains noninteractive.
        run_in_workspace "$source" init --quiet --prefix histclassic || die "$version: source init failed"
    fi
    verify_empty_public_sqlite_bridge "$version" "$source"
    create_historical_fixture "$version" "$source"
    export_classic_sqlite_with_candidate "$version" "$export_file"
    before=$(classic_source_fingerprint) || die "$version: could not snapshot historical SQLite source"
    output="$workspace/candidate-classic-refusal.out"
    if run_in_workspace "$candidate" list > "$output" 2>&1; then
        die "$version: candidate list accepted a historical SQLite workspace"
    fi
    after=$(classic_source_fingerprint) || die "$version: could not resnapshot historical SQLite source"
    [ "$after" = "$before" ] || die "$version: candidate list changed historical SQLite source before refusal"
    if grep -Fq 'no beads database found' "$output"; then
        die "$version: candidate list misclassified historical SQLite as no beads database found"
    fi
    grep -Fq 'historical SQLite' "$output" ||
        die "$version: candidate list did not identify historical SQLite"
    grep -Fq 'explicit migration is required' "$output" ||
        die "$version: candidate list did not refuse with explicit migration is required"
    if [ "$version" = "$CONFIGURED_SQLITE_VERSION" ]; then
        if run_in_workspace "$candidate" init --force --quiet --non-interactive --skip-hooks --skip-agents > "$output" 2>&1; then
            die "$version: candidate init --force accepted a configured SQLite workspace"
        fi
        run_in_workspace "$candidate" doctor --json > "$output" 2>&1 || die "$version: candidate doctor did not diagnose configured SQLite"
        grep -Fq 'explicit migration is required' "$output" || die "$version: candidate doctor omitted migration guidance"
        after=$(classic_source_fingerprint) || die "$version: could not resnapshot configured SQLite source"
        [ "$after" = "$before" ] || die "$version: candidate init or doctor changed configured SQLite source before bridge"
    fi
    verify_public_sqlite_bridge "$version" "$source" histclassic
    preserve_classic_rollback "$version"
    save_retained_digest classic "$version"
    for file in "${CLASSIC_SQLITE_ROLLBACK_FILES[@]}"; do rm -f -- "$workspace/.beads/$file"; done
    cp -f "$export_file" "$workspace/.beads/issues.jsonl"
    run_in_workspace "$candidate" init --from-jsonl --quiet --skip-hooks --skip-agents --prefix histclassic ||
        die "$version: candidate could not import classic export"
    reexport="$workspace/classic-$version-current-reexport.jsonl"
    reader_times="$workspace/classic-$version-reader-timestamps.json"
    current_times="$workspace/classic-$version-current-timestamps.json"
    run_in_workspace "$candidate" export --all -o "$reexport" >/dev/null ||
        die "$version: candidate could not re-export the fresh classic import"
    jq -sS "$timestamp_projection" "$export_file" > "$reader_times" ||
        die "$version: could not project reader timestamps"
    jq -sS "$timestamp_projection" "$reexport" > "$current_times" ||
        die "$version: could not project current timestamps"
    cmp -s "$reader_times" "$current_times" ||
        die "$version: fresh current import changed canonical reader timestamps"
    task=$(sed -n '1p' "$workspace/fixture-ids")
    run_in_workspace "$candidate" show "$task" --json | jq -e '
        (if type == "array" then .[0] else . end).notes == "Committed WAL data survived the current reader."
    ' >/dev/null || die "$version: WAL-resident notes did not survive candidate import"
    migrate_schema_current "$version" first
    verify_surviving_fixture "$version" classic
    verify_idempotent_migration "$version" classic
    verify_post_bridge_semantics "$version" classic
}

run_v017_upgrade() {
    local version="$PRE_CANONICAL_SQLITE_VERSION" source bridge bridge_bin direct_export bridge_export before after
    printf '\n● Pre-canonical SQLite upgrade: %s → v0.49.6 bridge → candidate\n' "$version"
    source=$(download_verified_release_binary "$version") || die "$version: verified release is unavailable"
    run_v017_source "$source" init --quiet --prefix hist017 || die "$version: source init failed"
    create_v017_fixture "$source"
    before=$(classic_source_fingerprint) || die "$version: could not snapshot source"
    direct_export="$workspace/v0.17.0-direct-export.jsonl"
    run_v017_source "$source" export --format jsonl > "$direct_export" || die "$version: direct export failed"
    jq -s -e 'length == 3 and all(.[]; (.comments // []) | length == 0)' "$direct_export" >/dev/null ||
        die "$version: direct export unexpectedly preserved stored comments"
    [ "$(classic_source_fingerprint)" = "$before" ] || die "$version: direct export changed source"
    if run_in_workspace "$candidate" list > "$workspace/candidate-v017-refusal.out" 2>&1; then
        die "$version: candidate list accepted an untouched source"
    fi
    after=$(classic_source_fingerprint) || die "$version: could not resnapshot source"
    [ "$after" = "$before" ] || die "$version: candidate list changed source before refusal"
    verify_public_sqlite_bridge "$version" "$source" hist017
    bridge="$workspace/v0.17.0-bridge"
    mkdir -p "$bridge"
    cp -af -- "$workspace/.beads" "$bridge/.beads" || die "$version: could not copy complete source into bridge"
    [ "$(beads_dir_fingerprint "$bridge/.beads")" = "$before" ] || die "$version: bridge copy differs from source"
    git -C "$bridge" init --quiet
    git -C "$bridge" config user.name historical-upgrade-test
    git -C "$bridge" config user.email historical-upgrade@test.invalid
    bridge_bin=$(download_verified_release_binary "$CLASSIC_SQLITE_VERSION") || die "$version: verified v0.49.6 bridge is unavailable"
    run_in_directory "$bridge" "$bridge_bin" --no-daemon --no-auto-import init --force --prefix hist017 >/dev/null ||
        die "$version: v0.49.6 bridge init failed"
    bridge_export="$bridge/v0.17.0-bridge-export.jsonl"
    run_in_directory "$bridge" "$bridge_bin" --no-daemon --no-auto-import export --format jsonl > "$bridge_export" ||
        die "$version: v0.49.6 bridge export failed"
    jq -s -e 'length == 3 and
        any(.[]; .labels == ["historical-upgrade"] and (.comments | length == 1) and .comments[0].author == "historical-upgrade") and
        any(.[]; (.dependencies // []) | any(.[]; .type == "blocks")) and
        any(.[]; (.dependencies // []) | any(.[]; .type == "related"))' "$bridge_export" >/dev/null ||
        die "$version: v0.49.6 bridge did not preserve source data"
    [ "$(classic_source_fingerprint)" = "$before" ] || die "$version: bridge changed original source"
    mv -f "$workspace/.beads" "$workspace/v0.17.0-source" || die "$version: could not retain source rollback copy"
    mkdir -p "$workspace/.beads"
    cp -f "$bridge_export" "$workspace/.beads/v0.17.0-bridge-export.jsonl"
    cp -f "$bridge_export" "$workspace/.beads/issues.jsonl"
    save_retained_digest v017 "$version"
    run_in_workspace "$candidate" init --from-jsonl --quiet --skip-hooks --skip-agents --prefix hist017 ||
        die "$version: candidate could not import bridge export"
    migrate_schema_current "$version" first
    verify_surviving_fixture "$version" v017
    jq -e --arg closed "$(sed -n '3p' "$workspace/fixture-ids")" \
        '(if type == "array" then .[0] else . end) | ((.dependencies // []) | any(.[]; (.id // .depends_on_id) == $closed and (.dependency_type // .type) == "related"))' \
        "$workspace/after-first.json" >/dev/null || die "$version: candidate did not preserve related dependency type"
    verify_idempotent_migration "$version" v017
    verify_post_bridge_semantics "$version" v017
}

run_v091_upgrade() {
    local version="$SOURCE_TAG_SQLITE_VERSION" source bridge bridge_bin bridge_export before after output
    printf '\n● Source-tag SQLite upgrade: %s → v0.49.6 bridge → candidate\n' "$version"
    source=$(build_verified_v091_source_binary) || die "$version: verified source build is unavailable"
    run_v091_source "$source" init --prefix vc || die "$version: source init failed"
    create_v091_fixture "$source"
    [ "$(find "$workspace/.beads" -mindepth 1 -maxdepth 1 -exec basename {} \; | LC_ALL=C sort)" = vc.db ] ||
        die "$version: source did not produce sole .beads/vc.db"
    [ ! -e "$workspace/.beads/issues.jsonl" ] && [ ! -L "$workspace/.beads/issues.jsonl" ] ||
        die "$version: source unexpectedly auto-flushed .beads/issues.jsonl"
    [ ! -e "$workspace/.beads/metadata.json" ] && [ ! -L "$workspace/.beads/metadata.json" ] ||
        die "$version: source unexpectedly wrote .beads/metadata.json"
    before=$(classic_source_fingerprint) || die "$version: could not snapshot source"
    output="$workspace/candidate-v091-refusal.out"
    if run_in_workspace "$candidate" list > "$output" 2>&1; then
        die "$version: candidate list accepted a metadata-less SQLite workspace"
    fi
    [ "$(classic_source_fingerprint)" = "$before" ] || die "$version: candidate list changed source before refusal"
    grep -Fq 'historical SQLite' "$output" || die "$version: candidate list did not identify historical SQLite"
    grep -Fq 'explicit migration is required' "$output" || die "$version: candidate list omitted migration guidance"
    if run_in_workspace "$candidate" init --force --quiet --non-interactive --skip-hooks --skip-agents > "$output" 2>&1; then
        die "$version: candidate init --force accepted a metadata-less SQLite workspace"
    fi
    [ "$(classic_source_fingerprint)" = "$before" ] || die "$version: candidate init --force changed source before refusal"
    run_in_workspace "$candidate" doctor --json > "$output" 2>&1 || die "$version: candidate doctor did not diagnose metadata-less SQLite"
    grep -Fq 'historical SQLite' "$output" || die "$version: candidate doctor did not identify historical SQLite"
    grep -Fq 'explicit migration is required' "$output" || die "$version: candidate doctor omitted migration guidance"
    [ "$(classic_source_fingerprint)" = "$before" ] || die "$version: candidate doctor changed source before bridge"
    verify_public_sqlite_bridge "$version" "$source" hist091
    # v0.9.1 documents this explicit export; its default flush did not materialize it.
    run_v091_source "$source" export --format jsonl --output .beads/issues.jsonl >/dev/null ||
        die "$version: source could not explicitly export .beads/issues.jsonl"
    [ -f "$workspace/.beads/issues.jsonl" ] && [ ! -L "$workspace/.beads/issues.jsonl" ] ||
        die "$version: source export did not produce .beads/issues.jsonl"
    jq -s -e 'length == 3 and ([.[].id] | sort) == ["hist091-1", "hist091-2", "hist091-3"]' \
        "$workspace/.beads/issues.jsonl" >/dev/null || die "$version: source export is incomplete"
    before=$(classic_source_fingerprint) || die "$version: could not snapshot explicitly exported source"
    bridge="$workspace/v0.9.1-bridge"
    mkdir -p "$bridge"
    cp -af -- "$workspace/.beads" "$bridge/.beads" || die "$version: could not copy complete source into bridge"
    [ "$(beads_dir_fingerprint "$bridge/.beads")" = "$before" ] || die "$version: bridge copy differs from source"
    git -C "$bridge" init --quiet
    git -C "$bridge" config user.name historical-upgrade-test
    git -C "$bridge" config user.email historical-upgrade@test.invalid
    bridge_bin=$(download_verified_release_binary "$CLASSIC_SQLITE_VERSION") || die "$version: verified v0.49.6 bridge is unavailable"
    run_in_directory "$bridge" "$bridge_bin" --no-daemon --no-auto-import init --force --prefix hist091 >/dev/null || die "$version: v0.49.6 bridge init failed"
    [ -f "$bridge/.beads/beads.db" ] && [ ! -e "$bridge/.beads/vc.db" ] ||
        die "$version: v0.49.6 bridge did not canonicalize the sole source database"
    bridge_export="$bridge/v0.9.1-bridge-export.jsonl"
    run_in_directory "$bridge" "$bridge_bin" --no-daemon --no-auto-import export --format jsonl > "$bridge_export" || die "$version: v0.49.6 bridge export failed"
    jq -s -e 'length == 3 and any(.[]; .id == "hist091-1" and .description == "v0.9.1 source description" and .design == "v0.9.1 source design" and .acceptance_criteria == "v0.9.1 source acceptance" and ((.labels | sort) == ["historical-upgrade", "v091-source"])) and any(.[]; .id == "hist091-3" and .status == "closed" and .closed_at != null) and any(.[]; .id == "hist091-2" and (.dependencies | any(.[]; .type == "blocks")) and (.dependencies | any(.[]; .type == "related")))' "$bridge_export" >/dev/null || die "$version: v0.49.6 bridge did not preserve source data"
    [ "$(classic_source_fingerprint)" = "$before" ] || die "$version: bridge changed original source"
    mv -f "$workspace/.beads" "$workspace/v0.9.1-source" || die "$version: could not retain source rollback copy"
    mkdir -p "$workspace/.beads"
    cp -f "$bridge_export" "$workspace/.beads/v0.9.1-bridge-export.jsonl"
    cp -f "$bridge_export" "$workspace/.beads/issues.jsonl"
    save_retained_digest v091 "$version"
    run_in_workspace "$candidate" init --from-jsonl --quiet --skip-hooks --skip-agents --prefix hist091 || die "$version: candidate could not import bridge export"
    migrate_schema_current "$version" first
    verify_surviving_fixture "$version" v091
    verify_idempotent_migration "$version" v091
    verify_post_bridge_semantics "$version" v091
}

verify_surviving_fixture() {
    local version="$1" kind="$2" task blocker closed snapshot task_detail task_status=open task_type=task
    local expected_description='Authentic historical upgrade fixture'
    if [ "$version" = "$SOURCE_TAG_SQLITE_VERSION" ]; then
        if ! {
            IFS= read -r task &&
            IFS= read -r blocker &&
            IFS= read -r closed
        } < "$workspace/fixture-ids"; then
            die "$version: could not read source fixture IDs"
        fi
        snapshot=$(run_in_workspace "$candidate" list --json -n 0 --all) || die "$version: candidate list failed"
        jq -e 'type == "array" and length == 3 and ([.[].id] | sort) == ["hist091-1", "hist091-2", "hist091-3"]' \
            <<< "$snapshot" >/dev/null || die "$version: candidate did not preserve the exact source issue set"
        task_detail=$(run_in_workspace "$candidate" show "$task" --json) || die "$version: candidate could not show source target"
        jq -e --arg task "$task" '(if type == "array" then .[0] else . end) | .id == $task and .title == "Historical dependency target v0.9.1" and .description == "v0.9.1 source description" and .design == "v0.9.1 source design" and .acceptance_criteria == "v0.9.1 source acceptance" and .status == "open" and .issue_type == "feature" and .priority == 2 and (.labels | sort == ["historical-upgrade", "v091-source"])' <<< "$task_detail" >/dev/null || die "$version: candidate did not preserve source target fields"
        run_in_workspace "$candidate" show "$closed" --json > "$workspace/v091-closed.json" || die "$version: candidate could not show closed source issue"
        jq -e --arg closed "$closed" '(if type == "array" then .[0] else . end) | .id == $closed and .title == "Historical completed v0.9.1" and .description == "v0.9.1 closed description" and .status == "closed" and .closed_at != null and .issue_type == "chore" and .priority == 3' "$workspace/v091-closed.json" >/dev/null || die "$version: candidate did not preserve closed source fields or timestamp"
        run_in_workspace "$candidate" show "$blocker" --json > "$workspace/after-first.json" || die "$version: candidate could not show dependent issue"
        jq -e --arg task "$task" --arg closed "$closed" '(if type == "array" then .[0] else . end) | .title == "Historical dependent v0.9.1" and .description == "v0.9.1 dependent description" and .status == "open" and .issue_type == "bug" and .priority == 1 and ((.dependencies // []) | any(.[]; (.id // .depends_on_id) == $task and (.dependency_type // .type) == "blocks")) and ((.dependencies // []) | any(.[]; (.id // .depends_on_id) == $closed and (.dependency_type // .type) == "related"))' "$workspace/after-first.json" >/dev/null || die "$version: candidate did not preserve dependent fields or dependencies"
        jq -S . "$workspace/after-first.json" > "$workspace/after-first-canonical.json"
        verify_retained_digest "$kind" "$version"
        return
    fi
    [ "$version" != v1.0.0 ] || { task_status=review; task_type=research; }
    if ! {
        IFS= read -r task &&
        IFS= read -r blocker &&
        IFS= read -r closed
    } < "$workspace/fixture-ids"; then
        die "$version: could not read source fixture IDs"
    fi
    snapshot=$(run_in_workspace "$candidate" list --json -n 0 --all) || die "$version: candidate list failed"
    jq -e --arg version "$version" --arg task "$task" --arg blocker "$blocker" --arg closed "$closed" --arg task_status "$task_status" --arg task_type "$task_type" '
        type == "array" and length == 3 and
        any(.[]; .id == $task and .title == ("Historical dependency target " + $version) and .status == $task_status and .issue_type == $task_type and .priority == 2) and
        any(.[]; .id == $closed and .title == ("Historical completed " + $version) and .status == "closed" and .issue_type == "task" and .priority == 3) and
        any(.[]; .id == $blocker and .status == "open" and .issue_type == "bug" and .priority == 1)
    ' <<< "$snapshot" >/dev/null || die "$version: candidate did not preserve representative issue fields or closed status"
    run_in_workspace "$candidate" show "$blocker" --json > "$workspace/after-first.json" || die "$version: candidate could not show dependent issue"
    jq -e --arg task "$task" '(if type == "array" then .[0] else . end) | ((.dependencies // []) | any(.[]; (.id // .depends_on_id) == $task and (.dependency_type // .type) == "blocks"))' \
        "$workspace/after-first.json" >/dev/null || die "$version: candidate did not preserve dependency"
    if is_classic_sqlite_version "$version"; then
        jq -e --arg closed "$closed" '(if type == "array" then .[0] else . end) | ((.dependencies // []) | any(.[]; (.id // .depends_on_id) == $closed and (.dependency_type // .type) == "related"))' \
            "$workspace/after-first.json" >/dev/null || die "$version: candidate did not preserve related dependency type"
    fi
    [ "$version" != "$PRE_CANONICAL_SQLITE_VERSION" ] || expected_description=""
    task_detail=$(run_in_workspace "$candidate" show "$task" --json --include-comments) || die "$version: candidate could not show labeled/commented source issue"
    jq -e --arg expected_description "$expected_description" '
        (if type == "array" then .[0] else . end) |
        ($expected_description == "" or .description == $expected_description) and
        ((.labels // []) | index("historical-upgrade") != null) and
        ((.comments // []) | any(.[]; .author == "historical-upgrade" and .text == "Historical comment must survive the upgrade."))
    ' <<< "$task_detail" >/dev/null || die "$version: candidate did not preserve the historical description, label, and comment body"
    if [ "$version" = v0.62.0 ]; then
        [ "$(run_in_workspace "$candidate" recall historical-upgrade-memory)" = 'Historical v0.62 memory must survive the upgrade.' ] ||
            die "$version: candidate did not preserve the historical memory"
    fi
    jq -S . "$workspace/after-first.json" > "$workspace/after-first-canonical.json"
    [ "$kind" = direct ] || verify_retained_digest "$kind" "$version"
}

verify_idempotent_migration() {
    local version="$1" kind="$2"
    if [ "$kind" != direct ]; then
        if run_in_workspace "$candidate" init --from-jsonl --quiet --skip-hooks --skip-agents --prefix "hist${version//[^0-9]/}" > "$workspace/reinit.out" 2>&1; then
            die "$version: repeat explicit export/import bridge was not refused"
        fi
    fi
    migrate_schema_current "$version" second
    run_in_workspace "$candidate" show "$(sed -n '2p' "$workspace/fixture-ids")" --json > "$workspace/after-second.json" || die "$version: candidate data disappeared after second schema run"
    jq -S . "$workspace/after-second.json" > "$workspace/after-second-canonical.json"
    cmp -s "$workspace/after-first-canonical.json" "$workspace/after-second-canonical.json" || die "$version: second schema migration changed data"
    [ "$kind" = direct ] || verify_retained_digest "$kind" "$version"
}

verify_post_bridge_semantics() {
    local version="$1" kind="$2" task blocker task_after ready
    task=$(sed -n '1p' "$workspace/fixture-ids")
    blocker=$(sed -n '2p' "$workspace/fixture-ids")
    run_in_workspace "$candidate" update "$task" --notes 'Post-upgrade bridge mutation persisted.' >/dev/null ||
        die "$version: candidate mutation failed after explicit bridge"
    task_after=$(run_in_workspace "$candidate" show "$task" --json --include-comments) ||
        die "$version: candidate could not reopen bridged data"
    jq -e '(if type == "array" then .[0] else . end) | .notes == "Post-upgrade bridge mutation persisted."' \
        <<< "$task_after" >/dev/null || die "$version: bridged mutation did not persist across reopen"
    ready=$(run_in_workspace "$candidate" ready --json) || die "$version: candidate ready check failed after explicit bridge"
    if jq -e --arg blocker "$blocker" 'type == "array" and any(.[]; .id == $blocker)' <<< "$ready" >/dev/null; then
        die "$version: dependent became ready before its blocker closed after explicit bridge"
    fi
    run_in_workspace "$candidate" close "$task" >/dev/null || die "$version: candidate could not close bridged dependency target"
    ready=$(run_in_workspace "$candidate" ready --json) || die "$version: candidate ready recheck failed after explicit bridge"
    jq -e --arg blocker "$blocker" 'type == "array" and any(.[]; .id == $blocker)' <<< "$ready" >/dev/null ||
        die "$version: dependent did not become ready after its blocker closed after explicit bridge"
    [ "$kind" = direct ] || verify_retained_digest "$kind" "$version"
}

run_embedded_dolt_upgrade() {
    local version="$1" source metadata metadata_sha task blocker task_after ready
    printf '\n● Direct embedded Dolt upgrade: %s → candidate\n' "$version"
    source=$(download_verified_release_binary "$version") || die "$version: verified release is unavailable"
    run_in_workspace "$source" init --quiet --prefix "hist${version//[^0-9]/}" || die "$version: embedded source init failed"
    create_historical_fixture "$version" "$source"
    metadata="$workspace/.beads/metadata.json"
    [ -d "$workspace/.beads/embeddeddolt" ] || die "$version: source did not create embedded Dolt data"
    [ ! -e "$workspace/.beads/dolt" ] || die "$version: source created a phantom server-Dolt directory"
    jq -e '.backend == "dolt" and .dolt_mode == "embedded"' "$metadata" >/dev/null ||
        die "$version: source metadata does not select embedded Dolt"
    metadata_sha=$(sha256_file "$metadata") || die "$version: could not fingerprint source metadata"
    verify_surviving_fixture "$version" direct
    if [ "$version" = v1.0.0 ]; then
        [ "$(run_in_workspace "$candidate" config get status.custom)" = review:active ] || die "$version: candidate did not preserve custom status config"
        [ "$(run_in_workspace "$candidate" config get types.custom)" = research ] || die "$version: candidate did not preserve custom type config"
    fi
    [ "$(sha256_file "$metadata")" = "$metadata_sha" ] || die "$version: candidate startup rewrote metadata"
    task=$(sed -n '1p' "$workspace/fixture-ids")
    run_in_workspace "$candidate" update "$task" --notes 'Post-upgrade direct mutation persisted.' >/dev/null ||
        die "$version: candidate mutation failed after direct upgrade"
    task_after=$(run_in_workspace "$candidate" show "$task" --json --include-comments) ||
        die "$version: candidate could not reopen mutated data"
    jq -e '(if type == "array" then .[0] else . end) | .notes == "Post-upgrade direct mutation persisted."' \
        <<< "$task_after" >/dev/null || die "$version: mutation did not persist across reopen"
    jq -S . <<< "$task_after" > "$workspace/direct-after-mutation.json"
    blocker=$(sed -n '2p' "$workspace/fixture-ids")
    run_in_workspace "$candidate" show "$blocker" --json > "$workspace/after-first.json"
    jq -S . "$workspace/after-first.json" > "$workspace/after-first-canonical.json"
    migrate_schema_current "$version" first
    verify_idempotent_migration "$version" direct
    run_in_workspace "$candidate" show "$task" --json --include-comments |
        jq -S . > "$workspace/direct-after-noops.json"
    cmp -s "$workspace/direct-after-mutation.json" "$workspace/direct-after-noops.json" ||
        die "$version: schema no-ops changed direct-upgrade semantics"
    [ "$(sha256_file "$metadata")" = "$metadata_sha" ] || die "$version: direct upgrade rewrote metadata"
    ready=$(run_in_workspace "$candidate" ready --json) || die "$version: candidate ready check failed"
    if jq -e --arg blocker "$blocker" 'type == "array" and any(.[]; .id == $blocker)' <<< "$ready" >/dev/null; then
        die "$version: dependent became ready before its blocker closed"
    fi
    run_in_workspace "$candidate" close "$task" >/dev/null || die "$version: candidate could not close dependency target"
    ready=$(run_in_workspace "$candidate" ready --json) || die "$version: candidate ready recheck failed"
    jq -e --arg blocker "$blocker" 'type == "array" and any(.[]; .id == $blocker)' <<< "$ready" >/dev/null ||
        die "$version: dependent did not become ready after its blocker closed"
    [ ! -e "$workspace/.beads/dolt" ] || die "$version: candidate created a phantom server-Dolt directory"
}

run_dolt_upgrade() {
    local version="$1" source init_port
    printf '\n● Historical Dolt upgrade: %s → candidate\n' "$version"
    source=$(download_verified_release_binary "$version") || die "$version: verified release is unavailable"
    mkdir -p "$workspace/.beads/dolt"
    start_historical_server "$workspace/.beads/dolt"
    init_port="$server_port"
    # v0.55.4 bootstraps the database in-process even with --server, then
    # persists server metadata for subsequent commands. Avoid opening the same
    # directory through the external server until that bootstrap is complete.
    [ "$version" != v0.55.4 ] || stop_historical_server
    run_in_workspace "$source" init --quiet --prefix "hist${version//[^0-9]/}" --server --server-host 127.0.0.1 --server-port "$init_port" || die "$version: source init failed against isolated server"
    [ "$version" != v0.55.4 ] || start_historical_server "$workspace/.beads/dolt" "$init_port"
    create_historical_fixture "$version" "$source"
    seed_legacy_issues_jsonl_sentinel "$version"
    verify_legacy_server_refusal_before_bridge "$version"
    explicit_dolt_upgrade "$version" "$source"
    verify_legacy_issues_jsonl_sentinel "$version"
    verify_surviving_fixture "$version" legacy
    verify_idempotent_migration "$version" legacy
    verify_post_bridge_semantics "$version" legacy
    verify_legacy_issues_jsonl_sentinel "$version"
}

run_v0554_default_embedded_dolt_upgrade() {
    local version=v0.55.4 source before after output
    printf '\n● Historical default embedded Dolt upgrade: %s → candidate\n' "$version"
    source=$(download_verified_release_binary "$version") || die "$version: verified release is unavailable"
    run_in_workspace "$source" init --quiet --prefix hist055default --skip-hooks ||
        die "$version: default embedded source init failed"
    create_historical_fixture "$version" "$source"
    [ -d "$workspace/.beads/dolt" ] || die "$version: default source did not create .beads/dolt"
    [ ! -e "$workspace/.beads/embeddeddolt" ] || die "$version: default source created a current embedded-Dolt directory"
    jq -e '.database == "dolt" and .jsonl_export == "issues.jsonl" and .backend == "dolt" and
        (.dolt_mode | not) and (.dolt_database | type == "string" and startswith("beads_"))' \
        "$workspace/.beads/metadata.json" >/dev/null || die "$version: default source metadata is not the reviewed embedded-in-dolt shape"
    before=$(classic_source_fingerprint) || die "$version: could not fingerprint default embedded source"
    output="$workspace/candidate-legacy-embedded-refusal.out"
    if run_in_workspace "$candidate" list > "$output" 2>&1; then
        die "$version: candidate list accepted a historical default embedded-Dolt workspace"
    fi
    grep -Fq 'explicit migration is required' "$output" ||
        die "$version: candidate list did not refuse with explicit migration is required"
    grep -Fq 'legacy Dolt workspace from bd 0.55.4' "$output" ||
        die "$version: candidate list did not identify the legacy embedded-in-dolt layout"
    after=$(classic_source_fingerprint) || die "$version: could not refingerprint default embedded source"
    [ "$after" = "$before" ] || die "$version: candidate list changed default embedded source before refusal"
    explicit_dolt_upgrade "$version" "$source"
    verify_surviving_fixture "$version" legacy
    verify_idempotent_migration "$version" legacy
    verify_post_bridge_semantics "$version" legacy
}

# ---------------------------------------------------------------------------
# Wisp-plane upgrade corpus
# ---------------------------------------------------------------------------
#
# create_historical_fixture/verify_surviving_fixture assert exact `length == 3`
# list shapes and are shared by thirteen version lanes, so wisp coverage gets
# its own workspace and its own lane instead of widening assertions every other
# version depends on. 1.3.0's chain is dominated by the plane those fixtures
# never touch: main 0054-0066 plus ignored 0012-0025 rewrite wisps,
# wisp_dependencies, wisp_comments, leases and events over rows no existing
# upgrade test creates.
#
# There is no `bd dolt sql`, so plane state (updated_at, is_blocked,
# leases.granted_node, dolt_ignore, dolt_status, row counts) is read with the
# pinned external Dolt CLI as a read-only oracle, the way #5816's field repro
# inspected a real store. `dolt sql -q` can exit 0 on failure
# (migrations/README.md), so every oracle read is decided by its payload and
# never by an exit status, and no oracle read ever runs DML.

# Old-binary capabilities, probed against each pinned release rather than
# assumed, so the lane can stay one shape across both regimes:
#
#   v1.0.1  v1.1.0  v1.1.2
#     yes     yes     yes    bd create --ephemeral
#     yes     yes     yes    dep add: wisp->issue, wisp->wisp, conditional-blocks
#     yes     yes     yes    dep add: issue->wisp (main dependencies table)
#     yes     yes     yes    comments add / label add on a wisp
#     yes     yes     yes    bd mol wisp list --all --json
#     yes     yes     yes    bd dolt remote add / push over file://
#      no     yes     yes    a pre-upgrade remote the candidate will still migrate
#      no     yes     yes    wisps.is_blocked column
#      no     yes     yes    post-split wisp_dependencies target columns
#      no     yes     yes    ignored_schema_migrations cursor
#
# The two "no" rows in the last block are the point of keeping v1.0.1: it is the
# only regime that makes the candidate perform the split and bootstrap the
# ignored cursor over populated rows instead of finding both already done.
is_wisp_plane_version() {
    case " ${WISP_PLANE_VERSIONS[*]} " in
        *" $1 "*) return 0 ;;
    esac
    return 1
}

is_embedded_dolt_version() {
    case " ${EMBEDDED_DOLT_VERSIONS[*]} " in
        *" $1 "*) return 0 ;;
    esac
    return 1
}

# A pre-upgrade Dolt remote is authentic input only where the candidate will
# still migrate the clone. Probed at implementation time: at v1.0.1 a configured
# remote trips the #4259 designated-migrator refusal (main cursor 32 -> 66 is
# not a same-version first mover), so `bd list` refuses and nothing migrates. At
# v1.1.x the smart gate admits the upgrade as a safe first mover (#4516), so the
# remote is seeded there and the version-control checks run against it.
wisp_lane_has_remote() {
    [ "$1" != v1.0.1 ]
}

wisp_embedded_db_dir() {
    local version="$1" root="$workspace/.beads/embeddeddolt" dir
    dir=$(find "$root" -mindepth 2 -maxdepth 2 -type d -name .dolt -printf '%h\n' 2>/dev/null | head -1)
    [ -n "$dir" ] || die "$version: could not locate the embedded Dolt database under $root"
    printf '%s\n' "$dir"
}

# Raw oracle read. Returns the CSV payload on success and non-zero when Dolt
# reported an error, including the cases where it still exits 0.
oracle_try() {
    local version="$1" query="$2" db output
    db=$(wisp_embedded_db_dir "$version") || return 1
    output=$( (cd "$db" && isolated_env timeout --kill-after=5s "$OP_TIMEOUT" \
        "$DOLT_BIN" sql -r csv -q "$query") 2>&1 ) || true
    case "$output" in
        ''|*'error on line'*|*'Error '*|*'error:'*|*panic:*) return 1 ;;
    esac
    printf '%s\n' "$output"
}

oracle_sql() {
    local version="$1" query="$2" output
    output=$(oracle_try "$version" "$query") ||
        die "$version: oracle query failed: $query"
    printf '%s\n' "$output"
}

# Header row plus exactly one value row.
oracle_scalar() {
    local version="$1" query="$2" output
    output=$(oracle_sql "$version" "$query")
    [ "$(wc -l <<< "$output")" -eq 2 ] ||
        die "$version: oracle query did not return exactly one row: $query"
    sed -n '2p' <<< "$output"
}

# Value rows only, with the CSV header stripped.
oracle_rows() {
    local version="$1" query="$2"
    oracle_sql "$version" "$query" | tail -n +2
}

oracle_count() {
    local version="$1" query="$2" value
    value=$(oracle_scalar "$version" "$query")
    [[ "$value" =~ ^[0-9]+$ ]] ||
        die "$version: oracle count is not a number: $query -> $value"
    printf '%s\n' "$value"
}

# Existence probes must not die when the answer is legitimately "no", so they
# read through oracle_try and treat every failure as absent.
oracle_table_exists() {
    local version="$1" table="$2" value
    value=$(oracle_try "$version" \
        "SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_NAME = '$table'" |
        sed -n '2p') || true
    [ "$value" = 1 ]
}

oracle_column_exists() {
    local version="$1" table="$2" column="$3" value
    value=$(oracle_try "$version" \
        "SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_NAME = '$table' AND COLUMN_NAME = '$column'" |
        sed -n '2p') || true
    [ "$value" = 1 ]
}

create_wisp_lane_issue() {
    local source="$1" title="$2" type="$3" priority="$4"
    shift 4
    local output id
    output=$(run_in_workspace "$source" create --title "$title" --type "$type" \
        --priority "$priority" --description 'Wisp-plane upgrade fixture' "$@") || return 1
    id=$(sed -n 's/.*Created issue: \([^[:space:]]*\).*/\1/p' <<< "$output" | head -1)
    [ -n "$id" ] || return 1
    printf '%s\n' "$id"
}

wisp_id() {
    sed -n "$1p" "$workspace/wisp-ids"
}

create_wisp_fixture() {
    local version="$1" source="$2" i1 i2 w1 w2 w3 w4 w5 listing
    i1=$(create_wisp_lane_issue "$source" 'Wisp lane blocker issue' task 1) ||
        die "$version: could not create the wisp-lane blocker issue"
    i2=$(create_wisp_lane_issue "$source" 'Wisp-gated issue' task 2) ||
        die "$version: could not create the wisp-gated issue"
    w1=$(create_wisp_lane_issue "$source" 'wisp open plain' task 2 --ephemeral) ||
        die "$version: could not create the plain wisp"
    w2=$(create_wisp_lane_issue "$source" 'wisp blocked by issue' task 2 --ephemeral) ||
        die "$version: could not create the issue-blocked wisp"
    w3=$(create_wisp_lane_issue "$source" 'wisp blocked by wisp' task 2 --ephemeral) ||
        die "$version: could not create the wisp-blocked wisp"
    w4=$(create_wisp_lane_issue "$source" 'wisp conditionally blocked' task 2 --ephemeral) ||
        die "$version: could not create the conditionally blocked wisp"
    w5=$(create_wisp_lane_issue "$source" 'wisp closed' task 3 --ephemeral) ||
        die "$version: could not create the closed wisp"
    printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n' "$i1" "$i2" "$w1" "$w2" "$w3" "$w4" "$w5" \
        > "$workspace/wisp-ids"

    # Wisp-source dependencies route into wisp_dependencies; the issue-source
    # dependency on a wisp target lands in the main dependencies table, which is
    # the depends_on_wisp_id column 0059's stand-in recompute reads.
    run_in_workspace "$source" dep add "$w2" "$i1" >/dev/null ||
        die "$version: could not block a wisp on an issue"
    run_in_workspace "$source" dep add "$w3" "$w1" >/dev/null ||
        die "$version: could not block a wisp on a wisp"
    run_in_workspace "$source" dep add "$w4" "$i1" --type conditional-blocks >/dev/null ||
        die "$version: could not conditionally block a wisp"
    run_in_workspace "$source" dep add "$i2" "$w1" >/dev/null ||
        die "$version: could not block an issue on a wisp"
    run_in_workspace "$source" comments add "$w1" 'Wisp comment must survive the upgrade.' \
        --author historical-upgrade >/dev/null ||
        die "$version: could not comment on a wisp"
    run_in_workspace "$source" label add "$w1" historical-upgrade >/dev/null ||
        die "$version: could not label a wisp"
    # Closed but minutes old. Wisp gc's OldThreshold is 24h, so the candidate
    # must not collect it; the survival count asserts that it did not.
    run_in_workspace "$source" close "$w5" >/dev/null ||
        die "$version: could not close the closed-wisp fixture"

    listing=$(run_in_workspace "$source" mol wisp list --all --json) ||
        die "$version: source could not list its own wisps"
    jq -e --arg w1 "$w1" --arg w5 "$w5" '
        (.wisps // []) as $wisps |
        ($wisps | length) == 5 and
        ($wisps | any(.id == $w1 and .status == "open")) and
        ($wisps | any(.id == $w5 and .status == "closed"))
    ' <<< "$listing" >/dev/null || die "$version: source wisp fixture is incomplete"

    if wisp_lane_has_remote "$version"; then
        mkdir -p "$workspace/dolt-remote"
        run_in_workspace "$source" dolt remote add origin "file://$workspace/dolt-remote" >/dev/null ||
            die "$version: could not configure the file:// Dolt remote"
        run_in_workspace "$source" dolt push >/dev/null ||
            die "$version: could not publish the pre-upgrade remote state"
    fi
}

# Deliberately selects only columns that exist in every seeded regime: v1.0.1
# has no wisps.is_blocked and no ignored_schema_migrations, so the shared
# projection is id/status/updated_at and the split-shape assertions live in the
# post-upgrade checks instead.
snapshot_wisp_plane() {
    local version="$1" label="$2"
    oracle_sql "$version" 'SELECT id, status, updated_at FROM wisps ORDER BY id' \
        > "$workspace/oracle-wisps-$label.csv"
    oracle_sql "$version" 'SELECT id, updated_at FROM issues ORDER BY id' \
        > "$workspace/oracle-issues-$label.csv"
    oracle_sql "$version" 'SELECT pattern FROM dolt_ignore ORDER BY pattern' \
        > "$workspace/oracle-ignore-$label.csv"
    oracle_count "$version" 'SELECT COUNT(*) FROM wisps' > "$workspace/oracle-wisps-$label.count"
    oracle_count "$version" 'SELECT COUNT(*) FROM wisp_comments' > "$workspace/oracle-wispcomments-$label.count"
    oracle_count "$version" 'SELECT COUNT(*) FROM wisp_dependencies' > "$workspace/oracle-wispdeps-$label.count"
    oracle_count "$version" 'SELECT COUNT(*) FROM events' > "$workspace/oracle-events-$label.count"
}

verify_wisp_survival() {
    local version="$1" listing detail w1 w2 w3 w4 w5 file
    w1=$(wisp_id 3); w2=$(wisp_id 4); w3=$(wisp_id 5); w4=$(wisp_id 6); w5=$(wisp_id 7)
    # --all: the default listing hides closed wisps, so the closed seed is only
    # visible here (probed at implementation time, v1.0.1 and v1.1.x alike).
    listing=$(run_in_workspace "$candidate" mol wisp list --all --json) ||
        die "$version: candidate could not list wisps after the upgrade"
    jq -e --arg w1 "$w1" --arg w2 "$w2" --arg w3 "$w3" --arg w4 "$w4" --arg w5 "$w5" '
        (.wisps // []) as $wisps |
        ($wisps | length) == 5 and
        ([$w1, $w2, $w3, $w4] | all(. as $id | ($wisps | any(.id == $id and .status == "open")))) and
        ($wisps | any(.id == $w5 and .status == "closed"))
    ' <<< "$listing" >/dev/null ||
        die "$version: candidate did not preserve the seeded wisp set and statuses"

    for file in wisps wispcomments wispdeps; do
        [ "$(cat "$workspace/oracle-$file-after.count")" = "$(cat "$workspace/oracle-$file-before.count")" ] ||
            die "$version: upgrade changed the $file row count on the wisp plane"
    done

    detail=$(run_in_workspace "$candidate" show "$w1" --json --include-comments) ||
        die "$version: candidate could not read the commented wisp"
    jq -e '(if type == "array" then .[0] else . end) |
        .title == "wisp open plain" and
        ((.labels // []) | index("historical-upgrade") != null) and
        ((.comments // []) | any(.author == "historical-upgrade" and .text == "Wisp comment must survive the upgrade."))
    ' <<< "$detail" >/dev/null ||
        die "$version: candidate did not preserve the wisp title, label, and comment body"
}

# updated_at is derived-state-sensitive: 0059 and its ignored/0015 twin both
# self-assign `updated_at = updated_at` precisely so an is_blocked repair cannot
# look like a user edit to staleness and TTL consumers. Their predecessors do
# not: 0046, 0047 and ignored/0007 all recompute without the guard, so replaying
# any of them restamps every row they touch.
#
# Which of them replay is a property of the source cursor, so the tolerance is
# too:
#
#   v1.0.1  main cursor 32, no ignored cursor. 0046/0047 and the whole ignored
#           series re-run, so every non-closed row in both tables legitimately
#           moves. Only the closed rows are provably untouched, because every
#           recompute is scoped `WHERE status NOT IN ('closed', 'pinned')`.
#   v1.1.x  main cursor 53, so 0046/0047 are behind it and issues must not move
#           at all. The ignored cursor says 0011, but `leases.granted_node` does
#           not exist yet, the sentinel disbelieves the cursor, and the ignored
#           series replays anyway (#5366) — dragging in ignored/0007 and
#           restamping the wisps whose is_blocked it toggles.
#
# Closed rows must be byte-stable in every regime; that is what bites when a
# migration forgets its status scope. The strongest guard on the rest is
# verify_ignored_track_idempotence, which requires a second open to move
# nothing at all.
#
# TODO(wisp-restamp): once ignored/0007 carries the same `updated_at =
# updated_at` guard as ignored/0015, drop the is_blocked tolerance and require
# the v1.1.x wisp projection to be byte-identical too.
verify_no_restamp() {
    local version="$1" table file moved tolerated stray closed_moved

    for table in issues wisps; do
        file="$workspace/oracle-$table"
        closed_moved=$(wisp_moved_rows "$file-before.csv" "$file-after.csv" \
            "$(oracle_rows "$version" "SELECT id FROM $table WHERE status IN ('closed', 'pinned')")")
        [ -z "$closed_moved" ] ||
            die "$version: upgrade restamped closed $table rows, so a recompute lost its status scope: $closed_moved"
    done

    is_wisp_plane_split_source "$version" || return 0

    cmp -s "$workspace/oracle-issues-before.csv" "$workspace/oracle-issues-after.csv" ||
        die "$version: upgrade restamped issues.updated_at with the main cursor already past 0059"

    moved=$(wisp_updated_at_moves "$workspace/oracle-wisps-before.csv" "$workspace/oracle-wisps-after.csv")
    tolerated=$(oracle_rows "$version" 'SELECT id FROM wisps WHERE is_blocked = 1 ORDER BY id' | LC_ALL=C sort)
    stray=$(LC_ALL=C comm -23 <(printf '%s\n' "$moved") <(printf '%s\n' "$tolerated") | tr -d '[:space:]')
    [ -z "$stray" ] ||
        die "$version: upgrade restamped wisps the is_blocked recompute never toggled: $stray"
}

# Sources whose cursors already cover the guarded recomputes, so only the
# ignored-track replay can move a timestamp.
is_wisp_plane_split_source() {
    [ "$1" != v1.0.1 ]
}

# ids whose updated_at differs between two `id,...,updated_at` projections.
wisp_updated_at_moves() {
    local before="$1" after="$2"
    join -t, -j 1 \
        <(tail -n +2 "$before" | awk -F, '{print $1 "," $NF}' | LC_ALL=C sort) \
        <(tail -n +2 "$after" | awk -F, '{print $1 "," $NF}' | LC_ALL=C sort) |
        awk -F, '$2 != $3 {print $1}' | LC_ALL=C sort
}

# Of the given ids, those whose updated_at moved.
wisp_moved_rows() {
    local before="$1" after="$2" candidates="$3"
    [ -n "$(tr -d '[:space:]' <<< "$candidates")" ] || return 0
    LC_ALL=C comm -12 \
        <(wisp_updated_at_moves "$before" "$after") \
        <(printf '%s\n' "$candidates" | LC_ALL=C sort) | tr '\n' ' ' | sed 's/ *$//'
}

# W2 blocks-on-issue, W3 blocks-on-wisp and W4 conditional-blocks-on-issue must
# all be blocked; the plain and closed wisps must not be. I2 carries the
# issue-blocked-by-wisp edge through dependencies.depends_on_wisp_id.
#
# `bd ready` never lists wisps, so the CLI cross-check is `bd blocked`, which
# does cover them (probed at implementation time).
verify_is_blocked() {
    local version="$1" actual expected blocked w1 w2 w3 w4 w5 i1 i2
    i1=$(wisp_id 1); i2=$(wisp_id 2)
    w1=$(wisp_id 3); w2=$(wisp_id 4); w3=$(wisp_id 5); w4=$(wisp_id 6); w5=$(wisp_id 7)

    actual=$(oracle_rows "$version" 'SELECT id, is_blocked FROM wisps ORDER BY id')
    expected=$(printf '%s,1\n%s,1\n%s,1\n%s,0\n%s,0\n' "$w2" "$w3" "$w4" "$w1" "$w5" | LC_ALL=C sort)
    [ "$(LC_ALL=C sort <<< "$actual")" = "$expected" ] ||
        die "$version: wisps.is_blocked truth table is wrong after the upgrade: $(tr '\n' ' ' <<< "$actual")"

    actual=$(oracle_rows "$version" 'SELECT id, is_blocked FROM issues ORDER BY id')
    expected=$(printf '%s,0\n%s,1\n' "$i1" "$i2" | LC_ALL=C sort)
    [ "$(LC_ALL=C sort <<< "$actual")" = "$expected" ] ||
        die "$version: issues.is_blocked is wrong after the upgrade (issue blocked by wisp): $(tr '\n' ' ' <<< "$actual")"

    blocked=$(run_in_workspace "$candidate" blocked --json) ||
        die "$version: candidate blocked query failed on the migrated wisp plane"
    jq -e --arg w3 "$w3" --arg i2 "$i2" '
        type == "array" and any(.[]; .id == $w3) and any(.[]; .id == $i2)
    ' <<< "$blocked" >/dev/null ||
        die "$version: bd blocked did not report the blocked wisp and the wisp-gated issue"
}

# 0055, 0062 and 0064 each register new ignored tables. Registration reads the
# existing dolt_ignore before writing it (#5947), so every pre-upgrade pattern
# must still be there afterwards alongside the new ones.
verify_ignore_superset_and_clean_status() {
    local version="$1" pattern missing="" dirty
    while IFS= read -r pattern; do
        [ -n "$pattern" ] || continue
        grep -Fxq "$pattern" <(tail -n +2 "$workspace/oracle-ignore-after.csv") ||
            missing="$missing $pattern"
    done < <(tail -n +2 "$workspace/oracle-ignore-before.csv")
    [ -z "$missing" ] ||
        die "$version: upgrade clobbered pre-existing dolt_ignore patterns:$missing"

    for pattern in leases events bd_events_journal bd_events_seq; do
        grep -Fxq "$pattern" <(tail -n +2 "$workspace/oracle-ignore-after.csv") ||
            die "$version: upgrade did not register '$pattern' in dolt_ignore"
    done

    dirty=$(oracle_count "$version" 'SELECT COUNT(*) FROM dolt_status')
    [ "$dirty" = 0 ] ||
        die "$version: upgrade left $dirty dirty tracked table(s): $(oracle_rows "$version" 'SELECT table_name FROM dolt_status' | tr '\n' ' ')"
    run_in_workspace "$candidate" dolt status >/dev/null ||
        die "$version: bd dolt status failed after the upgrade"
}

# 0062 drops, commits and recreates events as an ignored table. Every row has to
# survive that flip, so this runs before the lane makes any candidate mutation.
verify_events_flip() {
    local version="$1" before after
    before=$(cat "$workspace/oracle-events-before.count")
    after=$(cat "$workspace/oracle-events-after.count")
    [ "$before" -gt 0 ] ||
        die "$version: the events fixture is empty, so the flip assertion would be vacuous"
    [ "$after" = "$before" ] ||
        die "$version: the 0062 events flip changed the audit row count ($before -> $after)"
}

# The lease plane is born in the candidate chain (0054/0055 and ignored
# 0012/0016), so it is post-upgrade behavior rather than seedable history.
# granted_node is the column a contradicted-cursor replay loses (#5366).
verify_lease_plane() {
    local version="$1" i1 rows holder detail
    i1=$(wisp_id 1)
    oracle_table_exists "$version" leases ||
        die "$version: the upgrade did not create the leases table"
    oracle_column_exists "$version" leases granted_node ||
        die "$version: leases.granted_node is missing after the upgrade (contradicted-cursor replay)"
    [ "$(oracle_count "$version" 'SELECT COUNT(*) FROM leases')" = 0 ] ||
        die "$version: the upgrade invented lease rows"

    run_in_workspace "$candidate" update "$i1" --claim >/dev/null ||
        die "$version: candidate could not claim an issue on the migrated store"
    [ "$(oracle_count "$version" 'SELECT COUNT(*) FROM leases')" = 1 ] ||
        die "$version: a candidate claim did not write exactly one lease row"
    rows=$(oracle_rows "$version" 'SELECT issue_id, holder FROM leases')
    holder=$(cut -d, -f2 <<< "$rows")
    [ "$(cut -d, -f1 <<< "$rows")" = "$i1" ] ||
        die "$version: the lease row does not belong to the claimed issue"
    [ -n "$(tr -d '[:space:]"' <<< "$holder")" ] ||
        die "$version: the lease row has an empty holder"
    # TODO(lease-granted-node): `bd update --claim` currently leaves
    # granted_node empty and `bd show --json` exposes no lease_granted_node
    # field, so the column's presence is asserted above and its population is
    # not yet gated. Tighten once the claim path writes the node identity.
    detail=$(run_in_workspace "$candidate" show "$i1" --json) ||
        die "$version: candidate could not read the claimed issue"
    jq -e '(if type == "array" then .[0] else . end) |
        (.lease_expires_at // "") != "" and (.heartbeat_at // "") != ""
    ' <<< "$detail" >/dev/null ||
        die "$version: the claimed issue exposes no lease projection"
}

# Closing the blocker must release the plain-blocks and conditional-blocks
# wisps; the wisp-blocked wisp stays blocked because its blocker is still open.
verify_unblock_path() {
    local version="$1" i1 w2 w3 w4 actual expected
    i1=$(wisp_id 1); w2=$(wisp_id 4); w3=$(wisp_id 5); w4=$(wisp_id 6)
    run_in_workspace "$candidate" close "$i1" >/dev/null ||
        die "$version: candidate could not close the blocker while holding its own claim"
    actual=$(oracle_rows "$version" "SELECT id, is_blocked FROM wisps WHERE id IN ('$w2', '$w3', '$w4') ORDER BY id")
    expected=$(printf '%s,0\n%s,1\n%s,0\n' "$w2" "$w3" "$w4" | LC_ALL=C sort)
    [ "$(LC_ALL=C sort <<< "$actual")" = "$expected" ] ||
        die "$version: closing the blocker did not release the blocked wisps: $(tr '\n' ' ' <<< "$actual")"
}

# The 0058 contract, and the reason v1.0.1 is in this lane at all.
#
# v1.0.1 stores wisp dependencies in the legacy single-target shape
# (issue_id, depends_on_id) with no way to tell an issue target from a wisp
# target except by looking the id up. The candidate's ignored/0003-0005 split
# plus the 0047 delegate and the 0058 heal have to re-home those rows into
# depends_on_issue_id / depends_on_wisp_id / depends_on_external. Getting that
# wrong on populated rows is the "duplicate primary key given" hazard class in
# migrations/README.md.
#
# At v1.1.x the rows are already split, so the same assertion is a preservation
# check. Both regimes must end with identical, correctly-homed targets.
verify_wisp_dependency_split() {
    local version="$1" column actual expected i1 i2 w1 w2 w3 w4
    i1=$(wisp_id 1); i2=$(wisp_id 2)
    w1=$(wisp_id 3); w2=$(wisp_id 4); w3=$(wisp_id 5); w4=$(wisp_id 6)
    for column in depends_on_issue_id depends_on_wisp_id depends_on_external; do
        oracle_column_exists "$version" wisp_dependencies "$column" ||
            die "$version: wisp_dependencies.$column is missing after the split migrations"
    done
    if oracle_column_exists "$version" wisp_dependencies depends_on_id; then
        die "$version: wisp_dependencies still carries the legacy depends_on_id column"
    fi

    actual=$(oracle_rows "$version" 'SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, type FROM wisp_dependencies ORDER BY issue_id' | LC_ALL=C sort)
    expected=$(printf '%s,%s,,blocks\n%s,,%s,blocks\n%s,%s,,conditional-blocks\n' \
        "$w2" "$i1" "$w3" "$w1" "$w4" "$i1" | LC_ALL=C sort)
    [ "$actual" = "$expected" ] ||
        die "$version: wisp dependencies did not land on the right split targets: $(tr '\n' ' ' <<< "$actual")"

    # The issue-source edge on a wisp target lives in the main dependencies
    # table, which is the column 0059's stand-in recompute reads.
    actual=$(oracle_rows "$version" 'SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, type FROM dependencies ORDER BY issue_id' | LC_ALL=C sort)
    expected=$(printf '%s,,%s,blocks\n' "$i2" "$w1")
    [ "$actual" = "$expected" ] ||
        die "$version: the issue-blocked-by-wisp edge did not survive the dependencies split: $(tr '\n' ' ' <<< "$actual")"
}

# 0064/0066 and ignored 0022/0025 must land on an upgraded store, not just on a
# fresh clone, and wisps must stay writable through the 0060/ignored-0020
# storage_class default.
verify_events_journal_and_wisp_write() {
    local version="$1" w1 detail
    w1=$(wisp_id 3)
    oracle_table_exists "$version" bd_events_journal ||
        die "$version: the upgrade did not create bd_events_journal"
    oracle_column_exists "$version" bd_events_journal actor ||
        die "$version: bd_events_journal has no actor column after the upgrade (0066/ignored-0025)"
    oracle_table_exists "$version" bd_events_seq ||
        die "$version: the upgrade did not create bd_events_seq"

    run_in_workspace "$candidate" update "$w1" --notes 'post-upgrade wisp mutation' >/dev/null ||
        die "$version: candidate could not mutate a wisp after the upgrade"
    detail=$(run_in_workspace "$candidate" show "$w1" --json) ||
        die "$version: candidate could not reopen the mutated wisp"
    jq -e '(if type == "array" then .[0] else . end) | .notes == "post-upgrade wisp mutation"' \
        <<< "$detail" >/dev/null ||
        die "$version: the post-upgrade wisp mutation did not persist across a reopen"

    # TODO(events-journal-rows): the journal stays empty under this harness's
    # no-daemon isolation, so the shape is gated here and the row-level actor
    # assertion is not. These must still run clean on a migrated store.
    run_in_workspace "$candidate" events export >/dev/null ||
        die "$version: bd events export failed on the migrated store"
    run_in_workspace "$candidate" events tail >/dev/null ||
        die "$version: bd events tail failed on the migrated store"
}

# Push before pull: the 0062 flip leaves the local events table diverged from
# the pre-upgrade remote's tracked copy, so pulling first is refused with
# "local changes would be stomped by merge: events". Publishing the migration
# commits first is the order that works, and the wisp plane must survive both.
verify_wisp_vc_ops() {
    local version="$1" listing
    wisp_lane_has_remote "$version" || return 0
    run_in_workspace "$candidate" dolt push >/dev/null ||
        die "$version: candidate could not publish migration commits to the file:// remote"
    run_in_workspace "$candidate" dolt pull >/dev/null ||
        die "$version: candidate could not pull from the file:// remote after the upgrade"
    listing=$(run_in_workspace "$candidate" mol wisp list --all --json) ||
        die "$version: candidate could not list wisps after a pull"
    jq -e '(.wisps // []) | length == 5' <<< "$listing" >/dev/null ||
        die "$version: a pull disturbed the ignored-plane wisp rows"
}

# MigrateUp reports `applied` from the main track only and never adds
# appliedIgnored, so `✓ Schema already at vN` is blind to an ignored-track
# replay. This fingerprint is the only guard: a contradicted-cursor replay that
# restamps a row, moves the cursor, or re-runs non-idempotent DDL trips it even
# though the main-track no-op check stays green.
fingerprint_wisp_plane() {
    local version="$1"
    oracle_sql "$version" 'SELECT id, status, is_blocked, updated_at FROM wisps ORDER BY id'
    oracle_sql "$version" 'SELECT id, is_blocked, updated_at FROM issues ORDER BY id'
    oracle_sql "$version" 'SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, type FROM wisp_dependencies ORDER BY id'
    oracle_sql "$version" 'SELECT issue_id, holder FROM leases ORDER BY issue_id'
    oracle_sql "$version" 'SELECT pattern FROM dolt_ignore ORDER BY pattern'
    oracle_sql "$version" 'SELECT MAX(version) FROM ignored_schema_migrations'
    oracle_sql "$version" 'SELECT MAX(version) FROM schema_migrations'
    oracle_sql "$version" 'SELECT COUNT(*) FROM events'
    oracle_sql "$version" 'SELECT COUNT(*) FROM wisp_comments'
}

verify_ignored_track_idempotence() {
    local version="$1"
    fingerprint_wisp_plane "$version" > "$workspace/wisp-fingerprint-first"
    run_in_workspace "$candidate" list --json -n 0 --all >/dev/null ||
        die "$version: candidate could not reopen the migrated wisp workspace"
    migrate_schema_current "$version" wisp-second
    fingerprint_wisp_plane "$version" > "$workspace/wisp-fingerprint-second"
    cmp -s "$workspace/wisp-fingerprint-first" "$workspace/wisp-fingerprint-second" ||
        die "$version: a second open changed ignored-plane state that the main-track no-op check cannot see"
}

run_wisp_plane_upgrade() {
    local version="$1" source
    printf '\n● Wisp-plane upgrade: %s → candidate\n' "$version"
    source=$(download_verified_release_binary "$version") || die "$version: verified release is unavailable"
    run_in_workspace "$source" init --quiet --prefix histwisp --skip-hooks --skip-agents ||
        die "$version: wisp-plane source init failed"
    [ -d "$workspace/.beads/embeddeddolt" ] ||
        die "$version: wisp-plane source did not create embedded Dolt data"
    create_wisp_fixture "$version" "$source"
    snapshot_wisp_plane "$version" before

    # First candidate touch runs the full MigrateUp under the standard per-op
    # timeout. A dirty-table refusal or a migration error here is the headline
    # failure class for this lane.
    run_in_workspace "$candidate" list --json -n 0 --all > "$workspace/wisp-first-open.json" ||
        die "$version: candidate could not open the seeded wisp workspace"
    migrate_schema_current "$version" wisp-first
    snapshot_wisp_plane "$version" after

    # Read-only assertions first: the events count must be observed before any
    # candidate mutation, and the is_blocked truth table before the unblock.
    verify_wisp_survival "$version"
    verify_no_restamp "$version"
    verify_is_blocked "$version"
    verify_wisp_dependency_split "$version"
    verify_ignore_superset_and_clean_status "$version"
    verify_events_flip "$version"

    verify_lease_plane "$version"
    verify_unblock_path "$version"
    verify_events_journal_and_wisp_write "$version"
    verify_ignored_track_idempotence "$version"
    verify_wisp_vc_ops "$version"
    # 'bd doctor' has no embedded-mode JSON report; quick is the supported
    # health surface here (probed at implementation time).
    run_in_workspace "$candidate" doctor quick >/dev/null ||
        die "$version: bd doctor quick failed after the upgrade"
}

# #5816: an ignored-named table that is tracked at HEAD *and* dirty, with
# ignored migrations still pending, hard-locks the open.
#
# This state is not constructible from clean OSS lineage — every release since
# v0.63.3 registers the dolt_ignore patterns before creating the tables — which
# is exactly why it needs synthetic surgery here, and why it still matters:
# clones from forks and from older-behavior lineages arrive in it. The surgery
# is the only place this file runs DML through the oracle, and it runs it
# against a throwaway workspace.
#
# #5816 is still open, so this does NOT gate on whether the candidate accepts or
# refuses. It gates on the two things that must hold either way: the command
# terminates, and no wisp rows are lost. If it refuses, the refusal has to name
# the dirty tables rather than failing opaquely.
#
# TODO(#5816): once the fix lands, tighten this to "the open must succeed and
# `bd dolt commit` must clear the dirty state".
run_dirty_tracked_wisp_scenario() {
    local version="$1" source i1 w1 w2 before after output table
    printf '\n● Dirty tracked wisp tables (#5816): %s → candidate\n' "$version"
    source=$(download_verified_release_binary "$version") || die "$version: verified release is unavailable"
    run_in_workspace "$source" init --quiet --prefix histwispdirty --skip-hooks --skip-agents ||
        die "$version: dirty-table source init failed"
    i1=$(create_wisp_lane_issue "$source" 'Dirty-table blocker issue' task 1) ||
        die "$version: could not create the dirty-table blocker issue"
    w1=$(create_wisp_lane_issue "$source" 'dirty-table wisp' task 2 --ephemeral) ||
        die "$version: could not create the dirty-table wisp"
    w2=$(create_wisp_lane_issue "$source" 'dirty-table blocked wisp' task 2 --ephemeral) ||
        die "$version: could not create the dirty-table blocked wisp"
    run_in_workspace "$source" dep add "$w2" "$i1" >/dev/null ||
        die "$version: could not block the dirty-table wisp"

    # Track the ignored-named tables at HEAD, then dirty one of them again with
    # a normal old-binary write. dolt_add/dolt_commit are the only DML the
    # oracle ever issues.
    for table in wisps wisp_comments wisp_dependencies; do
        oracle_sql "$version" "CALL DOLT_ADD('-f', '$table')" >/dev/null
    done
    oracle_sql "$version" "CALL DOLT_COMMIT('-m', 'baseline: wisp tables tracked at HEAD', '--author', 'historical-upgrade <historical-upgrade@test.invalid>')" >/dev/null
    [ "$(oracle_count "$version" 'SELECT COUNT(*) FROM dolt_status')" = 0 ] ||
        die "$version: the dirty-table baseline commit did not leave a clean status"
    run_in_workspace "$source" update "$w1" --notes 'dirty the tracked wisp table' >/dev/null ||
        die "$version: could not dirty the tracked wisp table"
    [ "$(oracle_count "$version" 'SELECT COUNT(*) FROM dolt_status')" -gt 0 ] ||
        die "$version: the tracked wisp table did not become dirty, so the scenario would be vacuous"

    before=$(oracle_count "$version" 'SELECT COUNT(*) FROM wisps')
    output="$workspace/dirty-tracked-open.out"
    # run_in_workspace already wraps every call in `timeout`, so a hard-lock
    # surfaces as a timeout kill rather than hanging the job.
    if run_in_workspace "$candidate" list --json -n 0 --all > "$output" 2>&1; then
        printf '  · candidate accepted the dirty tracked wisp tables\n'
    else
        grep -Fq 'dirty tables' "$output" ||
            die "$version: candidate refused the dirty tracked wisp tables without naming them: $(head -c 300 "$output")"
        for table in wisps wisp_comments; do
            grep -Fq "$table" "$output" || continue
            printf '  · candidate refused cleanly, naming %s\n' "$table"
            break
        done
    fi
    after=$(oracle_count "$version" 'SELECT COUNT(*) FROM wisps')
    [ "$after" = "$before" ] ||
        die "$version: the dirty-table open lost wisp rows ($before -> $after)"
}

prepare_workspace() {
    workspace=$(mktemp -d "$RUN_ROOT/bd-historical-upgrade.XXXXXX") || die 'could not create isolated workspace'
    public_bridge_destination=""
    mkdir -p "$workspace/home" "$workspace/home/config" "$workspace/home/cache"
    chmod 700 "$workspace/home" "$workspace/home/config" "$workspace/home/cache"
    git -C "$workspace" init --quiet
    git -C "$workspace" config user.name historical-upgrade-test
    git -C "$workspace" config user.email historical-upgrade@test.invalid
}

for version in "${SELECTED_VERSIONS[@]}"; do
    # v0.63.3's published darwin/arm64 release binary was built without CGO
    # and can't open an embedded-Dolt store at all ("embedded Dolt requires
    # CGO") — a limitation of that specific historical release artifact, not
    # of this host or this corpus's code, and not something a local fix can
    # change (the whole point is testing the authentic pinned binary).
    # Skipping just this one version keeps ./run.sh from being permanently
    # red on macOS; linux/amd64 and every other embedded-Dolt version
    # (v1.0.0, v1.0.1, v1.1.0, v1.1.2) are unaffected and still run.
    if [ "$version" = v0.63.3 ] && [ "$OS" = darwin ] && [ "$ARCH" = arm64 ]; then
        printf '\n● Historical embedded Dolt upgrade: %s\n  ⚠ skipped on darwin/arm64: the published release binary for %s requires CGO to open an embedded-Dolt store, and its darwin/arm64 build has none — upstream release limitation, covered on linux/amd64\n' "$version" "$version"
        continue
    fi
    prepare_workspace
    case "$version" in
        "$SOURCE_TAG_SQLITE_VERSION") run_v091_upgrade ;;
        "$PRE_CANONICAL_SQLITE_VERSION") run_v017_upgrade ;;
        "$CLASSIC_SQLITE_VERSION"|"$CONFIGURED_SQLITE_VERSION") run_classic_sqlite_upgrade "$version" ;;
        *)
            # Membership rather than a second hardcoded list: the embedded
            # corpus is already declared once in lib/versions.sh, and a copy
            # here silently routes any newly added version into the server-Dolt
            # bridge instead.
            if is_embedded_dolt_version "$version"; then
                run_embedded_dolt_upgrade "$version"
            else
                run_dolt_upgrade "$version"
            fi
            ;;
    esac
    printf '  ✓ historical upgrade preserved representative data and schema migration was idempotent\n'
    cleanup
    workspace=""
    if [ "$version" = v0.55.4 ]; then
        prepare_workspace
        run_v0554_default_embedded_dolt_upgrade
        printf '  ✓ historical default embedded Dolt upgrade preserved representative data and schema migration was idempotent\n'
        cleanup
        workspace=""
    fi
    # Second lane, fresh workspace: the shared three-issue fixture never carries
    # wisps, so the ignored plane the 1.3.0 chain rewrites gets its own corpus.
    if is_wisp_plane_version "$version"; then
        prepare_workspace
        run_wisp_plane_upgrade "$version"
        printf '  ✓ wisp-plane upgrade preserved the ignored plane and the ignored track was idempotent\n'
        cleanup
        workspace=""
    fi
    # One version carries the synthetic #5816 state; it is a containment check,
    # not a regime check, so it does not need repeating across the corpus.
    if [ "$version" = v1.1.2 ]; then
        prepare_workspace
        run_dirty_tracked_wisp_scenario "$version"
        printf '  ✓ dirty tracked wisp tables terminated without losing wisp rows\n'
        cleanup
        workspace=""
    fi
done

printf '\n✓ historical upgrade corpus passed\n'
