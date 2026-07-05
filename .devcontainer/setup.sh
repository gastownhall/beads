#!/bin/bash
set -e

# Persist -tags=gms_pure_go in the go env config file (`go env -w`), so a
# bare `go build`/`go test` run later from a shell that never sourced
# .buildflags still picks up the tag (see docs/ICU-POLICY.md and
# `make doctor-build`). Read the persisted value here, before sourcing
# .buildflags below, since that export would otherwise shadow the on-disk
# go env value for the rest of this script.
echo "⚙️  Persisting -tags=gms_pure_go in go env..."
PERSISTED_GOFLAGS="$(go env GOFLAGS)"
if [[ "$PERSISTED_GOFLAGS" != *gms_pure_go* ]]; then
  go env -w GOFLAGS="${PERSISTED_GOFLAGS:+$PERSISTED_GOFLAGS }-tags=gms_pure_go"
fi

# Canonical build flags (GOFLAGS=-tags=gms_pure_go, CGO_ENABLED=1).
# shellcheck source=../.buildflags
source "$(dirname "$0")/../.buildflags"

echo "🔧 Building bd from source..."
go build -o bd ./cmd/bd

echo "📦 Installing bd globally..."
sudo mv bd /usr/local/bin/bd
sudo chmod +x /usr/local/bin/bd

echo "✅ Verifying bd installation..."
bd version

echo "🎯 Initializing bd (non-interactive)..."
if [ ! -f .beads/beads.db ]; then
  bd init --quiet
else
  echo "bd already initialized"
fi

echo "🪝 Installing git hooks..."
if [ -f examples/git-hooks/install.sh ]; then
  bash examples/git-hooks/install.sh
  echo "Git hooks installed successfully"
else
  echo "⚠️  Git hooks installer not found, skipping..."
fi

echo "📚 Installing Go dependencies..."
go mod download

echo "✨ Development environment ready!"
echo "Run 'bd ready' to see available tasks"
