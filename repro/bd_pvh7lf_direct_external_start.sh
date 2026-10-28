#!/usr/bin/env bash
# Repro for bd-pvh7lf / GH#6585 (direct-external-server cell).
#
# A workspace initialised against an external dolt sql-server at
# 127.0.0.1:<port> does not own that server. When the external server is not
# listening (stopped, restarting, or simply a port free on this host),
# `bd dolt start` must refuse rather than launch a bd-owned sql-server over the
# workspace's own .beads/dolt bound to the external endpoint's port.
#
# Expected (correct): `bd dolt start` exits non-zero and no dolt sql-server is
# running over the workspace. Exits 0 when that holds, 1 on the defect.
set -euo pipefail

root=$(pwd)
bazel build //cmd/bd:bd --experimental_convenience_symlinks=ignore >/dev/null 2>&1
bd="$(bazel info output_path 2>/dev/null)/$(bazel cquery //cmd/bd:bd --output=files 2>/dev/null | sed 's#^bazel-out/##')"
[[ -x "$bd" ]] || { echo "bd binary not found: $bd"; exit 2; }
command -v dolt >/dev/null || { echo "dolt not in PATH"; exit 2; }

t=$(mktemp -d /tmp/bd-pvh7lf-repro.XXXXXX)
ext_pid=""
cleanup() {
	[[ -n "$ext_pid" ]] && kill "$ext_pid" 2>/dev/null || true
	pkill -f "dolt sql-server --config $t/" 2>/dev/null || true
	sleep 1
	rm -rf "$t"
}
trap cleanup EXIT

mkdir -p "$t/home" "$t/ws" "$t/ext"
E=(env -i HOME="$t/home" PATH="/usr/local/bin:/usr/bin:/bin" BD_DISABLE_METRICS=1 DO_NOT_TRACK=1
	GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t)
port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')

# The external server: not bd's, in a directory outside the workspace.
(cd "$t/ext" && "${E[@]}" dolt config --global --add user.name t >/dev/null &&
	"${E[@]}" dolt config --global --add user.email t@t >/dev/null)
(cd "$t/ext" && exec "${E[@]}" dolt sql-server --host 127.0.0.1 --port "$port" >"$t/ext.log" 2>&1) &
ext_pid=$!
for _ in $(seq 1 50); do
	(echo >"/dev/tcp/127.0.0.1/$port") 2>/dev/null && break
	sleep 0.2
done

cd "$t/ws"
git init -q
"${E[@]}" "$bd" init --quiet --server --server-host 127.0.0.1 --server-port "$port" \
	--prefix dle --non-interactive --skip-agents --skip-hooks >/dev/null 2>&1

# The external server goes away; its port is now free on this host.
kill "$ext_pid"
wait "$ext_pid" 2>/dev/null || true
ext_pid=""
sleep 1

set +e
out=$("${E[@]}" "$bd" dolt start 2>&1)
rc=$?
set -e
echo "bd dolt start (exit $rc):"
echo "$out"

leaked=$(pgrep -af "dolt sql-server --config $t/ws/" || true)
cd "$root"
if [[ $rc -eq 0 || -n "$leaked" ]]; then
	echo "FAIL: bd dolt start on a direct-external-server workspace (127.0.0.1:$port) launched a bd-owned server instead of refusing"
	[[ -n "$leaked" ]] && echo "leaked: $leaked"
	exit 1
fi
echo "PASS: bd dolt start refused on the external endpoint"