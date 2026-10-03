#!/usr/bin/env bash
# setup-bazel: a short-lived rbe-west client certificate for fork and
# Dependabot pull_request runs, which get no secrets and no OIDC token
# (infra nativelink-cas/west README "rbe-fork"). Two steps around the CSR
# artifact upload (action.yml):
#
#   fork-credential.sh key    an EC P-256 key in BAZEL_CI_SECRET_DIR (outside the
#                             workspace; PKCS#8: Bazel's Netty TLS refuses a
#                             SEC1 "EC PRIVATE KEY"), and a CSR for the upload.
#                             Outputs: csr (path), artifact-name.
#   fork-credential.sh cert   asks rbe-fork-mint for a certificate naming the
#                             uploaded artifact (ARTIFACT_ID). Outputs: cert,
#                             key, endpoint, instance, tier.
#
# The mint signs only for an in-progress pull_request run of this repository
# whose artifact holds this CSR (only code running in the run can upload one),
# at the head of RBE_FORK_PR. It decides the tier from the PR's author and the
# run's triggering actor; RBE_FORK_TIER is what the rbe job's status probe
# said, and a different answer fails the job (re-run all jobs). The key never
# leaves the runner; the certificate is public. Nothing here is secret enough
# to mask, but the key file is 0600 like the CI key.
set -euo pipefail
MINT=${RBE_FORK_MINT:-https://rbe-mint.ops.gascity.com:8444}
ENDPOINT_RE=${RBE_FORK_ENDPOINT_RE:-'^grpcs://rbe-fork\.ops\.gascity\.com:8444$'}
dir=${BAZEL_CI_SECRET_DIR:?BAZEL_CI_SECRET_DIR is required}
out=${GITHUB_OUTPUT:-/dev/stdout}

case "${1:-}" in
key)
	# Only fork and Dependabot runs (bazel.yml modes fork-ro/fork-rw).
	[ "${BAZEL_FORK_REMOTE:-}" = true ] || exit 0
	install -d -m 0700 "$dir"
	csr_dir="${RUNNER_TEMP:?}/rbe-csr"
	rm -rf "$csr_dir" && install -d -m 0700 "$csr_dir"
	umask 077
	openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$dir/fork.key" 2>/dev/null
	openssl req -new -key "$dir/fork.key" -subj "/CN=rbe-fork-request" -out "$csr_dir/csr.pem"
	job=${GITHUB_JOB:?}
	[[ $job =~ ^[a-z0-9][a-z0-9_-]{0,39}$ ]] || { echo "setup-bazel: job id '$job' does not fit the mint's job pattern" >&2; exit 1; }
	{
		echo "csr=$csr_dir/csr.pem"
		echo "artifact-name=rbe-csr-$job-${GITHUB_RUN_ATTEMPT:?}-$(openssl rand -hex 8)"
	} >>"$out"
	;;
cert)
	: "${ARTIFACT_ID:?}" "${RBE_FORK_PR:?}" "${RBE_FORK_TIER:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_RUN_ID:?}"
	body=$(jq -cn --arg repo "${GITHUB_REPOSITORY#*/}" --argjson run "$GITHUB_RUN_ID" \
		--argjson attempt "${GITHUB_RUN_ATTEMPT:?}" --argjson pr "$RBE_FORK_PR" --arg job "${GITHUB_JOB:?}" \
		--argjson artifact "$ARTIFACT_ID" \
		'{repo: $repo, run_id: $run, run_attempt: $attempt, pr: $pr, job: $job, artifact_id: $artifact}')
	reply="$dir/mint.json"
	code=000
	for attempt in 1 2 3 4; do
		code=$(curl -sS -o "$reply" -w '%{http_code}' --max-time 60 -H 'content-type: application/json' \
			--data "$body" "$MINT/v1/cert") || code=000
		# Retry what may pass on its own: the mint could not reach GitHub (502),
		# or the network (000). Everything else is an answer.
		case "$code" in 200) break ;; 502 | 000) sleep $((attempt * 10)) ;; *) break ;; esac
	done
	if [ "$code" != 200 ]; then
		echo "::error title=rbe-fork mint refused (HTTP $code)::$(jq -r '.error // empty' "$reply" 2>/dev/null || true)"
		exit 1
	fi
	endpoint=$(jq -r .endpoint "$reply")
	instance=$(jq -r .instance "$reply")
	tier=$(jq -r .tier "$reply")
	[[ $endpoint =~ $ENDPOINT_RE ]] || { echo "::error::rbe-fork mint returned endpoint '$endpoint'" >&2; exit 1; }
	case "$tier/$instance" in ro/oss-fork | rw/oss) ;; *) echo "::error::rbe-fork mint returned tier '$tier' instance '$instance'" >&2; exit 1 ;; esac
	if [ "$tier" != "$RBE_FORK_TIER" ]; then
		echo "::error title=rbe-fork tier changed::the mint says '$tier', this run's mode says '$RBE_FORK_TIER' (allowlist changed?); re-run all jobs"
		exit 1
	fi
	umask 077
	jq -r .cert_pem "$reply" >"$dir/fork.crt"
	openssl x509 -in "$dir/fork.crt" -noout -subject -enddate
	{
		echo "cert=$dir/fork.crt"
		echo "key=$dir/fork.key"
		echo "endpoint=$endpoint"
		echo "instance=$instance"
		echo "tier=$tier"
	} >>"$out"
	;;
*)
	echo "usage: $0 key|cert" >&2
	exit 2
	;;
esac
