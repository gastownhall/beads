//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestNewDoltStoreFromConfig_NoMetadata verifies that newDoltStoreFromConfig
// succeeds when the beads directory has no metadata.json (fresh project).
// Regression test for GH#2988: "no database selected" error.
func TestNewDoltStoreFromConfig_NoMetadata(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	beadsDir := t.TempDir()

	// Confirm no config exists.
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config for empty dir")
	}

	// This should succeed using the default database name, not fail with
	// "no database selected".
	store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("newDoltStoreFromConfig failed: %v", err)
	}
	defer store.Close()
}

// TestEffectiveServerMode is a regression test for GH#6551: newDoltStoreFromConfig
// and its read-only sibling openNonMutatingStoreFromConfig checked only
// cfg.IsDoltServerMode(), which does not read dolt.shared-server from
// config.yaml (deliberately, to avoid a circular import with doltserver).
// A workspace with config.yaml but no metadata.json — the common shape of a
// linked git worktree, since metadata.json is commonly gitignored as
// machine-local state — left configfile.Load returning (nil, nil), so
// BEADS_DOLT_SHARED_SERVER (or dolt.shared-server) was silently ignored on
// these two paths even though cmd/bd/main.go's own resolution already
// compensates for exactly this gap (GH#3817). effectiveServerMode centralizes
// that compensation so the two paths cannot drift from main.go's again.
func TestEffectiveServerMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	if effectiveServerMode(nil) {
		t.Error("effectiveServerMode(nil) = true with no shared-server signal, want false")
	}
	if effectiveServerMode(&configfile.Config{}) {
		t.Error("effectiveServerMode(cfg not naming server) = true with no shared-server signal, want false")
	}

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	if !effectiveServerMode(nil) {
		t.Error("effectiveServerMode(nil) = false under BEADS_DOLT_SHARED_SERVER=1, want true (GH#6551)")
	}
	if !effectiveServerMode(&configfile.Config{}) {
		t.Error("effectiveServerMode(cfg not naming server) = false under BEADS_DOLT_SHARED_SERVER=1, want true (GH#6551)")
	}
}

// TestOpenNonMutatingStoreHonorsSharedServerConfig pins the read-only factory
// call site, not just effectiveServerMode in isolation. A linked worktree can
// have config.yaml tracked while metadata.json is absent; in that shape the
// active shared server must win over the embedded read-only fallback.
func TestOpenNonMutatingStoreHonorsSharedServerConfig(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n  auto-start: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("HOME", t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if !effectiveServerMode(nil) {
		t.Fatal("test setup: config.yaml did not enable shared-server mode")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	store, err := openNonMutatingStoreFromConfig(ctx, beadsDir, false)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("openNonMutatingStoreFromConfig unexpectedly succeeded without a server")
	}
	if !strings.Contains(err.Error(), "Dolt server") || strings.Contains(err.Error(), "embeddeddolt") {
		t.Fatalf("read-only factory did not select shared-server mode; got: %v", err)
	}
}

// TestEmbeddedOpen_EmptyDatabaseRejected verifies that embeddeddolt.Open fails
// with a clear error when called with an empty database name, rather than
// deferring to a confusing "no database selected" SQL error.
// Belt-and-suspenders defense for be-sy8 / GH#2988.
func TestEmbeddedOpen_EmptyDatabaseRejected(t *testing.T) {
	_, err := embeddeddolt.Open(t.Context(), t.TempDir(), "", "main")
	if err == nil {
		t.Fatal("expected error for empty database name")
	}
	if !strings.Contains(err.Error(), "database name must not be empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestNewDoltStoreFromConfig_HyphenatedDBName verifies that
// newDoltStoreFromConfig auto-sanitizes hyphenated database names for embedded
// mode and persists the fix to metadata.json.
// Regression test for GH#3231: pre-#2142 projects break on embedded upgrade.
func TestNewDoltStoreFromConfig_HyphenatedDBName(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	beadsDir := t.TempDir()

	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "my-cool-project",
		DoltMode:     configfile.DoltModeEmbedded,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("newDoltStoreFromConfig failed (should have auto-sanitized): %v", err)
	}
	defer store.Close()

	reloaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if reloaded.DoltDatabase != "my_cool_project" {
		t.Errorf("expected dolt_database to be sanitized to %q, got %q", "my_cool_project", reloaded.DoltDatabase)
	}
}

// TestMigrateHyphenatedDB_PersistsToMetadata verifies that migrateHyphenatedDB
// updates metadata.json with the sanitized database name.
func TestMigrateHyphenatedDB_PersistsToMetadata(t *testing.T) {
	beadsDir := t.TempDir()

	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "my-project",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := migrateHyphenatedDB(beadsDir, cfg, "my-project", "my_project"); err != nil {
		t.Fatalf("migrateHyphenatedDB failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}

	var saved configfile.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("failed to parse metadata.json: %v", err)
	}
	if saved.DoltDatabase != "my_project" {
		t.Errorf("expected dolt_database %q in metadata.json, got %q", "my_project", saved.DoltDatabase)
	}
}

// TestMigrateHyphenatedDB_RenamesDirectory verifies that migrateHyphenatedDB
// renames the old hyphenated database directory to the sanitized name.
func TestMigrateHyphenatedDB_RenamesDirectory(t *testing.T) {
	beadsDir := t.TempDir()

	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	oldDir := filepath.Join(dataDir, "my-project")
	newDir := filepath.Join(dataDir, "my_project")

	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("failed to create old dir: %v", err)
	}
	sentinel := filepath.Join(oldDir, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write sentinel: %v", err)
	}

	cfg := &configfile.Config{DoltDatabase: "my-project"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := migrateHyphenatedDB(beadsDir, cfg, "my-project", "my_project"); err != nil {
		t.Fatalf("migrateHyphenatedDB failed: %v", err)
	}

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old directory should no longer exist after rename")
	}
	if _, err := os.Stat(filepath.Join(newDir, "sentinel.txt")); err != nil {
		t.Error("sentinel file should exist in renamed directory")
	}
}

// TestMigrateHyphenatedDB_CollisionError verifies that migrateHyphenatedDB
// returns an error when both old and new directories exist (GH#3231).
func TestMigrateHyphenatedDB_CollisionError(t *testing.T) {
	beadsDir := t.TempDir()

	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	oldDir := filepath.Join(dataDir, "my-project")
	newDir := filepath.Join(dataDir, "my_project")

	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("failed to create old dir: %v", err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("failed to create new dir: %v", err)
	}

	cfg := &configfile.Config{DoltDatabase: "my-project"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	err := migrateHyphenatedDB(beadsDir, cfg, "my-project", "my_project")
	if err == nil {
		t.Fatal("expected error when both directories exist, got nil")
	}
	if !strings.Contains(err.Error(), "both") {
		t.Errorf("expected collision error message, got: %v", err)
	}
}

// TestMigrateHyphenatedDB_NoOldDir verifies that migrateHyphenatedDB still
// updates metadata.json even when the old directory doesn't exist (e.g., fresh
// project where only metadata.json has the bad name).
func TestMigrateHyphenatedDB_NoOldDir(t *testing.T) {
	beadsDir := t.TempDir()

	cfg := &configfile.Config{DoltDatabase: "my-project"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	if err := migrateHyphenatedDB(beadsDir, cfg, "my-project", "my_project"); err != nil {
		t.Fatalf("migrateHyphenatedDB failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}
	var saved configfile.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("failed to parse metadata.json: %v", err)
	}
	if saved.DoltDatabase != "my_project" {
		t.Errorf("expected %q, got %q", "my_project", saved.DoltDatabase)
	}
}

// TestNewDoltStoreFromConfig_DottedDBName verifies that dots are also
// auto-sanitized, not just hyphens (GH#3231).
func TestNewDoltStoreFromConfig_DottedDBName(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	beadsDir := t.TempDir()

	cfg := &configfile.Config{
		Database:     "dolt",
		DoltDatabase: "my.project",
		DoltMode:     configfile.DoltModeEmbedded,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	store, err := newDoltStoreFromConfig(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("newDoltStoreFromConfig failed (should have auto-sanitized dots): %v", err)
	}
	defer store.Close()

	reloaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if reloaded.DoltDatabase != "my_project" {
		t.Errorf("expected dolt_database %q, got %q", "my_project", reloaded.DoltDatabase)
	}
}

// TestNewDoltStore_StrictReadOnlyRefusesWritesOnFreshDatabase covers Blocker 2
// of the 2026-07-23 maintainer review on gastownhall/beads#4930: cfg.ReadOnly
// alone (an ordinary classified-read command) must route through
// OpenForReadOnlyCommand, which creates the embedded data directory on first
// use — but cfg.ReadOnly combined with cfg.DisableAutoStart (the strict
// --readonly signal) must route through the genuinely write-refusing
// OpenReadOnly instead, which fails rather than create anything for a fresh
// database.
func TestNewDoltStore_StrictReadOnlyRefusesWritesOnFreshDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	// Strict --readonly: must fail on a fresh database and must not create
	// the embeddeddolt data directory.
	strictBeadsDir := t.TempDir()
	strictDataDir := filepath.Join(strictBeadsDir, "embeddeddolt")
	_, err := newDoltStore(t.Context(), &dolt.Config{
		ReadOnly:         true,
		DisableAutoStart: true,
		BeadsDir:         strictBeadsDir,
		Database:         "testdb",
	})
	if err == nil {
		t.Fatal("newDoltStore(ReadOnly, DisableAutoStart) on a fresh database = nil error, want refusal")
	}
	if _, statErr := os.Stat(strictDataDir); !os.IsNotExist(statErr) {
		t.Fatalf("strict read-only open created %s (stat error: %v)", strictDataDir, statErr)
	}

	// Ordinary classified read (ReadOnly without DisableAutoStart): must
	// still succeed and initialize the embedded database on first use, per
	// the #4259 remote-migrate-gate exemption this backend relies on.
	classifiedBeadsDir := t.TempDir()
	classifiedDataDir := filepath.Join(classifiedBeadsDir, "embeddeddolt")
	store, err := newDoltStore(t.Context(), &dolt.Config{
		ReadOnly: true,
		BeadsDir: classifiedBeadsDir,
		Database: "testdb",
	})
	if err != nil {
		t.Fatalf("newDoltStore(ReadOnly) on a fresh database: %v", err)
	}
	defer store.Close()
	if _, statErr := os.Stat(classifiedDataDir); statErr != nil {
		t.Fatalf("classified-read open did not initialize %s: %v", classifiedDataDir, statErr)
	}
}
