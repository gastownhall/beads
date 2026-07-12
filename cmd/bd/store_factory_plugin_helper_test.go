package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeStoreFactoryPluginHelper(t *testing.T, capture string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "plugin-helper")
	body := fmt.Sprintf(`#!/bin/sh
capture=%q
printf '{"ok":true,"result":{"protocol":"beads.backend.v1alpha1","backend":"doltlite"}}\n'
while IFS= read -r line; do
  printf '%%s\n' "$line" >> "$capture"
  case "$line" in
    *'"method":"open"'*) printf '{"id":"1","ok":true,"result":{"session_id":"s"}}\n' ;;
    *'"method":"close"'*) printf '{"id":"2","ok":true,"result":{}}\n'; exit 0 ;;
    *) printf '{"ok":false,"error":{"code":"unknown_method","message":"unexpected"}}\n' ;;
  esac
done
`, capture)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write plugin helper: %v", err)
	}
	return script
}
