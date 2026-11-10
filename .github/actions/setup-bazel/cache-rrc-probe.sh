#!/usr/bin/env bash
# May this fork-cache lane read Bazel's remote repo contents cache through
# rbe-west's anonymous read-only cache (rbe-cache :8443, .bazelrc's
# fork-cache) right now? Exits 0 only when the kill switch below is on,
# rbe-cache answers a REAPI GetCapabilities for instance oss with gRPC status
# 0 and a ServerCapabilities that has cache_capabilities, and then answers a
# GetActionResult for a key no action has, and a ByteStream Read for a blob
# the CAS does not hold, each with NOT_FOUND (gRPC 5): the whole read chain
# (Caddy, nativelink-anon, the action cache and the CAS) is up. Anything else
# exits nonzero: switched off, an HTTP or gRPC error, a refused connection,
# a timeout, an answer that does not parse, no curl or python3. The caller
# then writes no repo contents cache lines, and never fails on it. The
# verdict carries the GetCapabilities TCP connect time without the DNS
# lookup (about one round trip to rbe-cache), for the lane's step summary.
#
# Why a probe: with the startup flag set and the cache unreachable (closed
# by rbe-west's kill switch, or down), Bazel retries every repository
# against it before fetching it itself (engdocs/design/
# bazel-remote-repo-contents-cache.md, R6), which costs far more than the
# cache saves. Fork pull_request runs see no repository variables, so the
# opt-out is the committed line below and the server's own answer.
#
# Three gRPC calls over curl's HTTP/2, the first answer decoded by
# python3's standard library: no grpcurl, no new action. Nothing goes to
# stdout (callers write their rc from it); one verdict line goes to stderr. A copy lives in gascity
# (tools/rbe/) and beads (.github/actions/setup-bazel/); keep in sync, except
# the kill switch, which is each repository's own.
#
# Tests only: RBE_CACHE_PROBE_URL (a stand-in server) and
# RBE_CACHE_PROBE_MAX_TIME (seconds).
set -uo pipefail

# Kill switch: off stops this repository's fork-cache lanes reading the
# remote repo contents cache (they fetch external repositories themselves),
# with a one-line pull request; on lets rbe-cache's answer decide. To stop
# every reader of rbe-cache, set it off in both copies (gascity and beads).
# It reaches a lane when the lane's checkout carries it.
fork_rrc_read=on

url=${RBE_CACHE_PROBE_URL:-https://rbe-cache.ops.gascity.com:8443}
max_time=${RBE_CACHE_PROBE_MAX_TIME:-5}

rtt=
no() {
	echo "rbe-cache rrc probe: $*${rtt:+ ($rtt)}; this lane fetches external repositories itself" >&2
	exit 1
}
[ "$fork_rrc_read" = on ] || no "switched off (fork_rrc_read=$fork_rrc_read in $(basename "$0"))"
command -v curl >/dev/null 2>&1 || no "no curl"
command -v python3 >/dev/null 2>&1 || no "no python3"
tmp=$(mktemp -d) || no "no temp dir"
trap 'rm -rf "$tmp"' EXIT

# call WHAT METHOD REQ: one gRPC call (METHOD: service/method); the answer's
# messages go to $tmp/body, its grpc-status to $st, and the TCP connect time
# without the DNS lookup to $tc. Fails the probe (prefixed with WHAT) on a
# transport error or a non-200 answer.
call() {
	local what=$1 out code dns
	out=$(curl -sS --http2 --connect-timeout 3 --max-time "$max_time" \
		-H 'content-type: application/grpc' -H 'te: trailers' \
		--data-binary @"$3" -D "$tmp/head" -o "$tmp/body" -w '%{http_code} %{time_namelookup} %{time_connect}' \
		"$url/$2" 2>"$tmp/err") ||
		no "$what$(tr '\n' ' ' <"$tmp/err")"
	read -r code dns tc <<<"$out"
	tc=$(awk -v d="$dns" -v c="$tc" 'BEGIN { if (c > d) printf "%.6f", c - d }' 2>/dev/null) || tc=
	[ "$code" = 200 ] || no "${what}HTTP $code"
	# grpc-status is a trailer; curl writes trailers after the headers.
	st=$(tr -d '\r' <"$tmp/head" | sed -n 's/^grpc-status: *//Ip' | tail -1)
}

# GetCapabilitiesRequest{instance_name: "oss"} (fork-cache's
# --remote_instance_name) as one uncompressed gRPC message.
printf '\000\000\000\000\005\012\003oss' >"$tmp/req" || no "cannot write the request"
call "" build.bazel.remote.execution.v2.Capabilities/GetCapabilities "$tmp/req"
rtt=$(awk -v s="$tc" 'BEGIN { if (s > 0) printf "tcp connect %.1f ms", s * 1000 }' 2>/dev/null) || rtt=
[ "$st" = 0 ] || no "gRPC status ${st:-missing}"

why=$(python3 -I - "$tmp/body" <<'PY'
import sys


def varint(b, i):
    v = s = 0
    while True:
        if i >= len(b) or s > 63:
            raise ValueError("truncated varint")
        c = b[i]
        i += 1
        v |= (c & 0x7F) << s
        s += 7
        if c < 0x80:
            return v, i


def fields(b):
    i = 0
    while i < len(b):
        key, i = varint(b, i)
        num, wire = key >> 3, key & 7
        if wire == 0:
            v, i = varint(b, i)
        elif wire == 1:
            v, i = b[i:i + 8], i + 8
        elif wire == 2:
            n, i = varint(b, i)
            v, i = b[i:i + n], i + n
        elif wire == 5:
            v, i = b[i:i + 4], i + 4
        else:
            raise ValueError("wire type %d" % wire)
        if i > len(b):
            raise ValueError("truncated field %d" % num)
        yield num, wire, v


try:
    body = open(sys.argv[1], "rb").read()
    if len(body) < 5 or body[0] != 0:
        raise ValueError("not an uncompressed gRPC message")
    n = int.from_bytes(body[1:5], "big")
    if len(body) != 5 + n:
        raise ValueError("gRPC length %d, %d bytes" % (n, len(body) - 5))
    # ServerCapabilities.cache_capabilities
    cache = [v for num, wire, v in fields(body[5:]) if num == 1 and wire == 2]
except (OSError, ValueError) as e:
    print("unreadable answer (%s)" % e)
    sys.exit(2)
if not cache:
    print("no cache_capabilities in the answer")
    sys.exit(1)
PY
) || no "${why:-python3 failed}"

# GetActionResultRequest{instance_name: "oss", action_digest: {hash:
# sha256("cache-rrc-probe: no action has this key"), size_bytes: 1}}: a key
# no action has, so the action cache behind rbe-cache must say NOT_FOUND.
key=ad12d7a2f1dec188fc1f2c4f5535a4a65eb630fa2f309eca423695e6ec7abee7
printf '\000\000\000\000\113\012\003oss\022\104\012\100%s\020\001' "$key" >"$tmp/req" ||
	no "cannot write the request"
call "GetActionResult: " build.bazel.remote.execution.v2.ActionCache/GetActionResult "$tmp/req"
[ "$st" = 5 ] || no "GetActionResult: gRPC status ${st:-missing}, want 5 (NOT_FOUND)"

# ReadRequest{resource_name: "oss/blobs/<the same hash>/1"}: a blob the CAS
# does not hold (its content would have to hash to that key), so the CAS
# behind rbe-cache must say NOT_FOUND.
printf '\000\000\000\000\116\012\114oss/blobs/%s/1' "$key" >"$tmp/req" ||
	no "cannot write the request"
call "ByteStream Read: " google.bytestream.ByteStream/Read "$tmp/req"
[ "$st" = 5 ] || no "ByteStream Read: gRPC status ${st:-missing}, want 5 (NOT_FOUND)"
echo "rbe-cache rrc probe: rbe-cache answers GetCapabilities, GetActionResult and ByteStream Read${rtt:+ ($rtt)}; this lane reads the remote repo contents cache" >&2
