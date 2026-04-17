//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestHintsDisabledDB_UsesQuotedConfigKey(t *testing.T) {
	saveAndRestoreGlobals(t)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	store := newTestStoreIsolatedDB(t, dbPath, "test")

	ctx := context.Background()
	if err := store.SetConfig(ctx, ConfigKeyHintsDoctor, "false"); err != nil {
		t.Fatalf("SetConfig(%q): %v", ConfigKeyHintsDoctor, err)
	}

	if !hintsDisabledDB(store.DB()) {
		t.Fatalf("expected hintsDisabledDB to read %q from config table", ConfigKeyHintsDoctor)
	}
}

func TestCheckVersionMismatchDB_UsesQuotedMetadataKey(t *testing.T) {
	saveAndRestoreGlobals(t)

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")
	store := newTestStoreIsolatedDB(t, dbPath, "test")

	ctx := context.Background()
	if err := store.SetLocalMetadata(ctx, "bd_version", "0.0.0"); err != nil {
		t.Fatalf("SetLocalMetadata(bd_version): %v", err)
	}

	issue := checkVersionMismatchDB(store.DB())
	if issue == "" {
		t.Fatal("expected version mismatch to be reported")
	}
	if !strings.Contains(issue, "CLI: "+Version) {
		t.Fatalf("expected mismatch to mention CLI version %q, got %q", Version, issue)
	}
	if !strings.Contains(issue, "database: 0.0.0") {
		t.Fatalf("expected mismatch to mention database version, got %q", issue)
	}
}
