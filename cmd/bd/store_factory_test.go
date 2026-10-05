//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
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
// machine-local state — therefore had its only statement of shared-server mode
// silently ignored on these two paths, even though cmd/bd/main.go's own
// resolution already compensates for exactly this gap (GH#3817).
// effectiveServerMode centralizes that compensation so the paths cannot drift
// from main.go's again.
//
// The config.yaml layer is the whole of the gap. BEADS_DOLT_SHARED_SERVER was
// never ignored at these call sites: configfile.IsDoltServerMode honors it
// itself, and normalizeLoadedConfig replaces an absent metadata.json with a
// non-nil DefaultConfig() before the gate runs, so the old gate's `cfg != nil`
// conjunct never short-circuited here. This test covers the env arm because it
// is part of the helper's contract, not because it was broken.
func TestEffectiveServerMode(t *testing.T) {
	// The false cases assert the ABSENCE of a shared-server signal, and
	// doltserver.IsSharedServerMode falls through to config.GetBool on the
	// process-global config singleton, so without this they red spuriously on
	// any machine or agent rig that enables dolt.shared-server in config.yaml
	// — or after any earlier test in this package leaves the singleton
	// initialized with it. The hermetic recipe is the sibling test's below.
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Setenv("HOME", t.TempDir())
	emptyDir := t.TempDir()
	t.Setenv("BEADS_DIR", emptyDir)

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	if effectiveServerMode(emptyDir, nil) {
		t.Error("effectiveServerMode(nil) = true with no shared-server signal, want false")
	}
	if effectiveServerMode(emptyDir, &configfile.Config{}) {
		t.Error("effectiveServerMode(cfg not naming server) = true with no shared-server signal, want false")
	}

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	if !effectiveServerMode(emptyDir, nil) {
		t.Error("effectiveServerMode(nil) = false under BEADS_DOLT_SHARED_SERVER=1, want true (GH#6551)")
	}
	if !effectiveServerMode(emptyDir, &configfile.Config{}) {
		t.Error("effectiveServerMode(cfg not naming server) = false under BEADS_DOLT_SHARED_SERVER=1, want true (GH#6551)")
	}
}

// TestEffectiveServerModeResolvesYamlPerWorkspace pins which config.yaml the
// dolt.shared-server layer is resolved against. newDoltStoreFromConfig's
// contract says activation is resolved from beadsDir's own config rather than
// the launching workspace's, and it is the factory the CROSS-WORKSPACE opens
// use (routed.go, create.go, init_contributor.go all pass a foreign beadsDir).
// doltserver.IsSharedServerMode() reads process-global state, so consulting it
// directly let a launcher whose own config.yaml enables shared-server retarget
// a foreign workspace that explicitly asked for embedded onto the shared server
// — a connect failure, or a same-named database holding someone else's rows.
//
// The two halves are a matched pair and both are load-bearing: the bound
// workspace must keep winning over a stale metadata.json that still pins
// dolt_mode="embedded" (main.go:1710 / shouldUseExternalDoltStatus, GH#2946),
// so the guard cannot simply be "an explicit dolt_mode always wins".
func TestEffectiveServerModeResolvesYamlPerWorkspace(t *testing.T) {
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)

	root := t.TempDir()
	launcherDir := filepath.Join(root, "launcher", ".beads")
	foreignDir := filepath.Join(root, "foreign", ".beads")
	for _, dir := range []string{launcherDir, foreignDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The launcher enables shared-server in its OWN config.yaml. This is the
	// durable shape: `bd dolt shared-server on` and `bd init --shared-server`
	// both persist the key through config.SetYamlConfig, which resolves to a
	// .beads/config.yaml.
	if err := os.WriteFile(filepath.Join(launcherDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n  auto-start: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Both workspaces carry the same explicit metadata.json statement, so the
	// ONLY thing that differs between the two assertions below is which
	// workspace the yaml layer is read from.
	embeddedMeta := []byte(`{"dolt_mode":"embedded","dolt_database":"beads"}`)
	for _, dir := range []string{launcherDir, foreignDir} {
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), embeddedMeta, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("BEADS_DIR", launcherDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	launcherCfg, err := configfile.Load(launcherDir)
	if err != nil {
		t.Fatalf("load launcher metadata.json: %v", err)
	}
	foreignCfg, err := configfile.Load(foreignDir)
	if err != nil {
		t.Fatalf("load foreign metadata.json: %v", err)
	}
	if launcherCfg == nil || foreignCfg == nil {
		t.Fatal("test setup: metadata.json did not load")
	}

	if !effectiveServerMode(launcherDir, launcherCfg) {
		t.Error("effectiveServerMode(bound workspace) = false; shared-server in the workspace's own config.yaml must win over its stale dolt_mode=embedded (GH#2946)")
	}
	if effectiveServerMode(foreignDir, foreignCfg) {
		t.Error("effectiveServerMode(foreign workspace) = true; the LAUNCHER's config.yaml must not retarget a workspace whose own metadata.json asks for embedded")
	}
}

// TestEffectiveServerModeHonorsWorkspaceLocalYaml pins the SECOND project-level
// layer. config.Initialize merges .beads/config.local.yaml last — the documented
// place for machine-specific settings that must not be committed — so every
// config.GetBool consumer in the tree (main.go, bootstrap, doctor,
// migrate-dolt-mode) lets it override the tracked config.yaml. dolt.shared-server
// is exactly that kind of state and .beads/config.yaml is git-tracked, so "the
// repo enables it, this machine opts out" is the ordinary use of the escape
// hatch, not an exotic shape.
//
// Resolving these four factories against config.yaml alone would answer that
// shape differently from every other resolver — the same resolver-divergence
// class GH#6551 itself is an instance of. Both directions are load-bearing and
// the enabling one is the silent one: config.yaml false (what `bd dolt
// shared-server off` persists) plus a local true would fall through to
// embeddeddolt.Open and re-create the GH#6551 phantom database with no error at
// all.
func TestEffectiveServerModeHonorsWorkspaceLocalYaml(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tracked string
		local   string
		want    bool
	}{
		{name: "local opts this machine out", tracked: "true", local: "false", want: false},
		{name: "local opts this machine in", tracked: "false", local: "true", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Same hermetic recipe as the siblings: these arms are answered by
			// the workspace's own files, but the recipe is what keeps that true
			// if the layering below them ever changes.
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

			// The GH#6551 shape: config.yaml tracked, no metadata.json, so cfg
			// arrives nil and the yaml layer is the workspace's only statement.
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BEADS_DIR", beadsDir)
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
				[]byte("dolt:\n  shared-server: "+tc.tracked+"\n  auto-start: false\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.local.yaml"),
				[]byte("dolt:\n  shared-server: "+tc.local+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			// The merged reader is the oracle: whatever config.GetBool answers
			// for this workspace is what these factories must answer, so the
			// assertion cannot drift from the layer order it is pinning.
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
			if got := config.GetBool("dolt.shared-server"); got != tc.want {
				t.Fatalf("test setup: merged config.GetBool = %v, want %v (config.yaml %s + config.local.yaml %s)",
					got, tc.want, tc.tracked, tc.local)
			}

			if got := effectiveServerMode(beadsDir, nil); got != tc.want {
				t.Errorf("effectiveServerMode = %v, want %v: .beads/config.local.yaml (%s) must override the tracked config.yaml (%s), as config.Initialize merges it last",
					got, tc.want, tc.local, tc.tracked)
			}
		})
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
	t.Setenv("BEADS_DOLT_SERVER_PORT", readOnlySharedServerPort)
	t.Setenv("HOME", t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if !effectiveServerMode(beadsDir, nil) {
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
	// Positive half: only the server arm dials, so a refused connection to the
	// port this test pinned is proof the shared-server branch was taken. Both
	// discriminators are owned outside internal/storage/dolt — a syscall
	// sentinel and this test's own port — so a reworded connection error in a
	// package this PR does not own cannot turn a correct implementation red.
	if !errors.Is(err, syscall.ECONNREFUSED) && !strings.Contains(err.Error(), readOnlySharedServerPort) {
		t.Fatalf("read-only factory did not dial the shared server; got: %v", err)
	}
	// Negative half: the embedded fallback must not have run.
	if strings.Contains(err.Error(), "embeddeddolt") {
		t.Fatalf("read-only factory fell through to the embedded store; got: %v", err)
	}
}

// readOnlySharedServerPort is a port nothing listens on, pinned through
// BEADS_DOLT_SERVER_PORT (the highest-priority port source in
// internal/doltserver, so it wins over DefaultSharedServerPort) and reused by
// the assertion, so the two cannot drift apart.
//
// It must be DISTINCTIVE, not merely unused: the assertion below ORs this
// substring with errors.Is(ECONNREFUSED), so a short or common value makes that
// half unfalsifiable — "1" matched any errno, timestamp, or port like 3306, and
// the OR then held up wherever the dial error was not ECONNREFUSED (a context
// deadline from the 2s timeout, or a Windows ETIMEDOUT shape), silently
// collapsing a two-discriminator proof to a tautology. Five distinctive digits,
// like the prime sibling's sharedServerPrimePort.
//
// It must also sit BELOW 32768, outside the kernel's ephemeral range (32768-60999
// here and on the GitHub runners). Now that the substring half carries real
// weight, a process that bound :0 and happened to land on this port would answer
// the dial and turn the expected ECONNREFUSED into a handshake error, reddening
// a correct implementation.
const readOnlySharedServerPort = "19998"

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
