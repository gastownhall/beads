#!/usr/bin/env bash
# Run compile, test, or lint inside a resource-capped Docker container.
# Usage: scripts/dev-docker.sh <command...>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=../.buildflags
source "$ROOT/.buildflags"
GO_VERSION="$(awk '/^go / { print $2; exit }' "$ROOT/go.mod")"
if [[ -z "$GO_VERSION" ]]; then
	echo "dev-docker: no go directive in go.mod" >&2
	exit 1
fi
IMAGE="golang:${GO_VERSION}-bookworm"

if [[ $# -eq 0 ]]; then
	echo "usage: scripts/dev-docker.sh <command...>" >&2
	echo "example: scripts/dev-docker.sh go test ./test/constitution" >&2
	exit 2
fi

exec docker run --rm \
	--name beads-dev \
	--cpus=2 \
	--memory=4g \
	--memory-swap=4g \
	--pids-limit=512 \
	-e GOMAXPROCS=2 \
	-e "CGO_ENABLED=${CGO_ENABLED}" \
	-e "GOFLAGS=${GOFLAGS}" \
	-e GOTOOLCHAIN=local \
	-v "$ROOT":/src \
	-v beads-gomod:/go/pkg/mod \
	-v beads-gobuild:/root/.cache/go-build \
	-w /src \
	"$IMAGE" \
	"$@"
