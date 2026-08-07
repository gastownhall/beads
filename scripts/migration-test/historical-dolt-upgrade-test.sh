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
(v0.63.3, v1.0.0, v1.0.1, v1.1.0, v1.1.2) against CANDIDATE_BIN. Every release archive is pinned and verified.
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
    resolved=$(realpath -e -- "$resolved") || die "cannot resolve Dolt runtime: $DOLT_BIN"
    output=$("$resolved" version 2>&1) ||
        die "pinned external Dolt runtime does not run: $resolved"
    bare="${DOLT_TEST_RUNTIME_VERSION#v}"
    if ! grep -Eq "(^|[^0-9])${bare//./\\.}([^0-9]|$)" <<< "$output"; then
        die "unpinned external Dolt runtime: $resolved reports '$output'; require ${DOLT_TEST_RUNTIME_VERSION} linux/amd64 (archive SHA-256 ${DOLT_TEST_RUNTIME_SHA256})"
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
[ "$(uname -s)" = Linux ] || die 'the pinned authentic-binary corpus supports Linux only'
[ "$(uname -m)" = x86_64 ] || die 'the pinned authentic-binary corpus supports linux/amd64 only'
[[ "$OP_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || die 'HISTORICAL_DOLT_E2E_TIMEOUT must be a positive number of seconds'
for version in "${SELECTED_VERSIONS[@]}"; do
    case " ${HISTORICAL_DOLT_VERSIONS[*]} " in
        *" $version "*) verify_dolt_runtime; break ;;
    esac
done

candidate="${CANDIDATE_BIN:-}"
if [ -z "$candidate" ]; then candidate=$(build_candidate); fi
candidate=$(realpath -e -- "$candidate") || die 'candidate binary cannot be resolved'
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

beads_dir_fingerprint() {
    local beads_dir="$1"
    (
        cd "$beads_dir" || return 1
        while IFS= read -r -d '' entry; do
            find "$entry" -maxdepth 0 -printf '%p\0%y\0%l\0'
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
    [ "$(find "$workspace/.beads" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)" = vc.db ] ||
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
    prepare_workspace
    case "$version" in
        "$SOURCE_TAG_SQLITE_VERSION") run_v091_upgrade ;;
        "$PRE_CANONICAL_SQLITE_VERSION") run_v017_upgrade ;;
        "$CLASSIC_SQLITE_VERSION"|"$CONFIGURED_SQLITE_VERSION") run_classic_sqlite_upgrade "$version" ;;
        v0.63.3|v1.0.0|v1.0.1|v1.1.0|v1.1.2) run_embedded_dolt_upgrade "$version" ;;
        *) run_dolt_upgrade "$version" ;;
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
done

printf '\n✓ historical upgrade corpus passed\n'
