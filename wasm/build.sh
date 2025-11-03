#!/bin/bash
# Build bd for WebAssembly

set -e

echo "Building bd for WASM..."
GOOS=js GOARCH=wasm go build -o wasm/bd.wasm ./cmd/bd

echo "WASM build complete: wasm/bd.wasm"
ls -lh wasm/bd.wasm
