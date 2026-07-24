//go:build !cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The nocgo factories must also fail loud on a present-but-unloadable
// metadata.json — naming the real cause instead of the misleading
// "embedded requires CGO" fallback message.
func TestNocgoStoreFactoriesCorruptMetadataFailLoud(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_mode":"serv`), 0o600); err != nil {
		t.Fatalf("write corrupt metadata.json: %v", err)
	}

	for name, open := range map[string]func() (interface{ Close() error }, error){
		"newDoltStoreFromConfig": func() (interface{ Close() error }, error) {
			return newDoltStoreFromConfig(context.Background(), beadsDir)
		},
		"newReadOnlyStoreFromConfig": func() (interface{ Close() error }, error) {
			return newReadOnlyStoreFromConfig(context.Background(), beadsDir)
		},
	} {
		store, err := open()
		if err == nil {
			if store != nil {
				_ = store.Close()
			}
			t.Fatalf("%s: want error for corrupt metadata.json, got nil", name)
		}
		if !strings.Contains(err.Error(), "metadata.json") {
			t.Fatalf("%s: error should name metadata.json, got: %v", name, err)
		}
	}
}
