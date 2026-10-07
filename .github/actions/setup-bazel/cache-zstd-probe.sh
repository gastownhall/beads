#!/usr/bin/env bash
# Does rbe-west's anonymous read-only cache (rbe-cache :8443, .bazelrc's
# fork-cache) advertise zstd right now? Exits 0 only when its REAPI
# GetCapabilities answer lists ZSTD in cache_capabilities.supported_compressors,
# the field Bazel checks before it accepts --remote_cache_compression (it
# refuses a remote that does not list it). Anything else exits nonzero: not
# listed, an HTTP or gRPC error, a refused connection, a timeout, an answer
# that does not parse, no curl or python3. The caller then leaves the flag
# off, and never fails on it.
#
# So the server decides, at run time: when rbe-west turns zstd on, fork-cache
# runs use it; when it turns it off (rollback), they stop. No repository
# variable (fork pull_request runs see none) and no revert.
#
# One unary gRPC call over curl's HTTP/2, decoded by python3's standard
# library: no grpcurl, no new action. Nothing goes to stdout (callers write
# their rc from it); one verdict line goes to stderr. A byte copy lives in
# gascity (tools/rbe/) and beads (.github/actions/setup-bazel/); keep in sync.
#
# Tests only: RBE_CACHE_PROBE_URL (a stand-in server) and
# RBE_CACHE_PROBE_MAX_TIME (seconds).
set -uo pipefail
url=${RBE_CACHE_PROBE_URL:-https://rbe-cache.ops.gascity.com:8443}
max_time=${RBE_CACHE_PROBE_MAX_TIME:-5}

no() {
	echo "rbe-cache zstd probe: $*; the fork cache stays identity" >&2
	exit 1
}
command -v curl >/dev/null 2>&1 || no "no curl"
command -v python3 >/dev/null 2>&1 || no "no python3"
tmp=$(mktemp -d) || no "no temp dir"
trap 'rm -rf "$tmp"' EXIT

# GetCapabilitiesRequest{instance_name: "oss"} (fork-cache's
# --remote_instance_name) as one uncompressed gRPC message.
printf '\000\000\000\000\005\012\003oss' >"$tmp/req" || no "cannot write the request"
code=$(curl -sS --http2 --connect-timeout 3 --max-time "$max_time" \
	-H 'content-type: application/grpc' -H 'te: trailers' \
	--data-binary @"$tmp/req" -D "$tmp/head" -o "$tmp/body" -w '%{http_code}' \
	"$url/build.bazel.remote.execution.v2.Capabilities/GetCapabilities" 2>"$tmp/err") ||
	no "$(tr '\n' ' ' <"$tmp/err")"
[ "$code" = 200 ] || no "HTTP $code"
# grpc-status is a trailer; curl writes trailers after the headers.
st=$(tr -d '\r' <"$tmp/head" | sed -n 's/^grpc-status: *//Ip' | tail -1)
[ "$st" = 0 ] || no "gRPC status ${st:-missing}"

why=$(python3 -I - "$tmp/body" <<'PY'
import sys

ZSTD = 1  # build.bazel.remote.execution.v2.Compressor.Value.ZSTD


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
    compressors = []
    for num, wire, v in fields(body[5:]):
        if num != 1 or wire != 2:  # ServerCapabilities.cache_capabilities
            continue
        for cnum, cwire, cv in fields(v):
            if cnum != 6:  # CacheCapabilities.supported_compressors
                continue
            if cwire == 0:
                compressors.append(cv)
            elif cwire == 2:  # packed
                j = 0
                while j < len(cv):
                    x, j = varint(cv, j)
                    compressors.append(x)
except (OSError, ValueError) as e:
    print("unreadable answer (%s)" % e)
    sys.exit(2)
if ZSTD in compressors:
    print("advertises zstd")
    sys.exit(0)
print("zstd not advertised (supported_compressors %s)" % compressors)
sys.exit(1)
PY
) || no "${why:-python3 failed}"
echo "rbe-cache zstd probe: $why; the fork cache uses zstd" >&2
