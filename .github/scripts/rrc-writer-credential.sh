#!/usr/bin/env bash
# A 30-minute rbe-west client certificate for the remote repo contents cache
# writer (bazel.yml's rrc-seed job; engdocs/design/bazel-remote-repo-contents-cache.md).
#
# The job trades its GitHub Actions OIDC token (audience rbe-rrc-writer) for a
# certificate CN=rbe-rrc-writer from rbe-west's mint. The mint signs only for
# this repository's bazel.yml on a push to main (repository, ref, event_name
# and job_workflow_ref claims); pull_request, fork and Dependabot runs cannot
# get one (no OIDC token, or a refs/pull/N/merge one). On rbe-west that
# certificate reaches only GetCapabilities, the CAS and an action cache front
# (rrc-gate) that admits repo-contents entries and refuses everything else, so
# it can never write an execution result or execute anything.
#
# The key is an EC P-256 key (PKCS#8: Bazel's Netty TLS refuses a SEC1 "EC
# PRIVATE KEY") generated here in BAZEL_CI_SECRET_DIR, outside the workspace;
# it never leaves the runner. The OIDC token is masked. Outputs (to
# $GITHUB_OUTPUT when set, else stdout): cert, key, endpoint, instance; none
# when the mint answers 503 or cannot be reached (seeding switched off on
# rbe-west: infra nativelink-cas/west/README.md "Client contract"), which
# exits 0 with a warning.
#
# Tests only: RBE_RRC_MINT (a stand-in mint) and RBE_RRC_ENDPOINT_RE.
set -euo pipefail
MINT=${RBE_RRC_MINT:-https://rbe-mint.ops.gascity.com:8444}
ENDPOINT_RE=${RBE_RRC_ENDPOINT_RE:-'^grpcs://rbe-west\.ops\.gascity\.com(:443)?$'}
AUDIENCE=rbe-rrc-writer
dir=${BAZEL_CI_SECRET_DIR:?BAZEL_CI_SECRET_DIR is required}
out=${GITHUB_OUTPUT:-/dev/stdout}

if [ -z "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ] || [ -z "${ACTIONS_ID_TOKEN_REQUEST_TOKEN:-}" ]; then
	echo "::error title=rrc writer::no OIDC token request URL; the job needs permissions: id-token: write" >&2
	exit 1
fi

install -d -m 0700 "$dir"
umask 077

oidc=$(curl -sS --fail --connect-timeout 5 --max-time 30 --retry 3 --retry-all-errors \
	-H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
	"$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$AUDIENCE" | jq -r '.value // empty')
if [ -z "$oidc" ]; then
	echo "::error title=rrc writer::GitHub returned no OIDC token" >&2
	exit 1
fi
printf '::add-mask::%s\n' "$oidc"

openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$dir/rrc-writer.key" 2>/dev/null
csr=$(openssl req -new -key "$dir/rrc-writer.key" -subj "/CN=rbe-rrc-writer-request")
body=$(jq -cn --arg csr "$csr" '{csr_pem: $csr}')

reply="$dir/rrc-mint.json"
code=000
for attempt in 1 2 3 4; do
	# --connect-timeout: a closed gate drops the SYN; fail fast, not in 60 s.
	code=$(curl -sS -o "$reply" -w '%{http_code}' --connect-timeout 5 --max-time 60 \
		-H "Authorization: Bearer $oidc" -H 'content-type: application/json' \
		--data "$body" "$MINT/v1/rrc-writer/cert") || code=000
	# Retry what may pass on its own: a rate limit (429), the mint could not
	# reach GitHub's JWKS (502), or the network (000). Everything else is an
	# answer.
	case "$code" in 429 | 502 | 000) ;; *) break ;; esac
	[ "$attempt" -eq 4 ] || sleep $((attempt * 10))
done
case "$code" in
200) ;;
503 | 000)
	# The mint's switch is off, its CA is not installed, or rbe-west's fork
	# edge is closed: seeding is off, not broken. No outputs, so the seed
	# step skips; the push to main never fails on it.
	echo "::warning title=rrc writer: seeding off (HTTP $code)::$(jq -r '.error // empty' "$reply" 2>/dev/null || true)"
	exit 0
	;;
*)
	echo "::error title=rrc writer mint refused (HTTP $code)::$(jq -r '.error // empty' "$reply" 2>/dev/null || true)"
	exit 1
	;;
esac

endpoint=$(jq -r '.endpoint // empty' "$reply")
instance=$(jq -r '.instance // empty' "$reply")
[[ $endpoint =~ $ENDPOINT_RE ]] || {
	echo "::error::rrc writer mint returned endpoint '$endpoint'" >&2
	exit 1
}
[ "$instance" = oss ] || {
	echo "::error::rrc writer mint returned instance '$instance', want oss" >&2
	exit 1
}
jq -r '.cert_pem // empty' "$reply" >"$dir/rrc-writer.crt"
subject=$(openssl x509 -in "$dir/rrc-writer.crt" -noout -subject -nameopt RFC2253) || {
	echo "::error::rrc writer mint returned no certificate" >&2
	exit 1
}
rdns=$(tr ',' '\n' <<<"${subject#subject=}")
cn=$(sed -n 's/^CN=//p' <<<"$rdns")
org=$(sed -n 's/^O=//p' <<<"$rdns")
if ! [[ $cn =~ ^rbe-rrc-writer(-[a-z0-9]+)?$ ]] || [ "$org" != gascity ]; then
	echo "::error::rrc writer mint returned a certificate for '$subject'" >&2
	exit 1
fi
# The certificate must belong to the key generated above.
if [ "$(openssl x509 -in "$dir/rrc-writer.crt" -noout -pubkey | openssl sha256)" != "$(openssl pkey -in "$dir/rrc-writer.key" -pubout | openssl sha256)" ]; then
	echo "::error::rrc writer mint returned a certificate for another key" >&2
	exit 1
fi
openssl x509 -in "$dir/rrc-writer.crt" -noout -subject -enddate
rm -f "$reply"
{
	echo "cert=$dir/rrc-writer.crt"
	echo "key=$dir/rrc-writer.key"
	echo "endpoint=$endpoint"
	echo "instance=$instance"
} >>"$out"
