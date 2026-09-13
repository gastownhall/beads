package dolt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/ownershiphandoff"
)

func TestHandoffJournalPermitsAutoStartOnlyForCommittedBD(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beadsDir, "ownership-handoff.json")
	write := func(phase ownershiphandoff.Phase, owner ownershiphandoff.Owner, requestRoot string) {
		data, err := json.Marshal(ownershiphandoff.Journal{Request: ownershiphandoff.Request{Root: requestRoot, CityRoot: root, Database: "d", Workspace: "w", Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}, Phase: phase, Owner: owner})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ownershiphandoff.PhaseTargetConfigured, ownershiphandoff.OwnerLegacyGC, root)
	if handoffJournalPermitsAutoStart(beadsDir) {
		t.Fatal("pending handoff allowed auto-start")
	}
	write(ownershiphandoff.PhaseCommitted, ownershiphandoff.OwnerBD, root)
	if handoffJournalPermitsAutoStart(beadsDir) {
		t.Fatal("incomplete committed bd handoff allowed auto-start")
	}
	write(ownershiphandoff.PhaseCommitted, ownershiphandoff.OwnerBD, filepath.Join(root, "other"))
	if handoffJournalPermitsAutoStart(beadsDir) {
		t.Fatal("wrong-root handoff allowed auto-start")
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if handoffJournalPermitsAutoStart(beadsDir) {
		t.Fatal("malformed handoff allowed auto-start")
	}
	request := ownershiphandoff.Request{Root: root, CityRoot: root, Database: "d", Workspace: "w", Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	identity := struct {
		CityRoot  string `json:"city_root"`
		ScopeRoot string `json:"scope_root"`
		Database  string `json:"database"`
		Workspace string `json:"workspace"`
		Endpoint  struct {
			Host   string `json:"host"`
			Port   int    `json:"port"`
			Socket string `json:"socket"`
		} `json:"endpoint"`
		DataDir        string `json:"data_dir"`
		ConfigFile     string `json:"config_file"`
		PID            int    `json:"pid"`
		StartIdentity  string `json:"start_identity"`
		StartTimeTicks int64  `json:"start_time_ticks"`
		PortHolderPID  int    `json:"port_holder_pid"`
	}{CityRoot: root, ScopeRoot: root, Database: "d", Workspace: "w", DataDir: filepath.Join(beadsDir, "dolt"), ConfigFile: filepath.Join(root, ".gc", "dolt.yaml"), PID: 7, StartIdentity: "birth", StartTimeTicks: 1, PortHolderPID: 7}
	identity.Endpoint.Host, identity.Endpoint.Port = "127.0.0.1", 3307
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(identityJSON)
	token := "sha256:" + hex.EncodeToString(sum[:])
	metadata, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Operation     string `json:"operation"`
		Result        string `json:"result"`
		Owner         string `json:"owner"`
		Mutates       bool   `json:"mutates"`
		Identity      any    `json:"identity"`
		IdentityToken string `json:"identity_token"`
		ErrorCode     string `json:"error_code"`
	}{SchemaVersion: 1, Operation: "handoff-inspect", Result: "eligible", Owner: "legacy-gc", Identity: identity, IdentityToken: token})
	if err != nil {
		t.Fatal(err)
	}
	complete := ownershiphandoff.Journal{Request: request, Phase: ownershiphandoff.PhaseCommitted, Owner: ownershiphandoff.OwnerBD, SnapshotCaptured: true, MutationOccurred: true, CommitHookRan: true, Snapshot: ownershiphandoff.Snapshot{Metadata: metadata, Sentinel: token, TargetPID: 42, TargetBirth: "target-birth", TargetDataDir: filepath.Join(beadsDir, "dolt"), TargetLaunchID: "0123456789abcdef0123456789abcdef", TargetLaunchConfig: filepath.Join(beadsDir, "dolt-handoff-0123456789abcdef0123456789abcdef.yaml"), TargetLaunchExecutable: "/usr/bin/dolt"}}
	data, err := json.Marshal(complete)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if !handoffJournalPermitsAutoStart(beadsDir) {
		t.Fatal("fully authenticated committed bd handoff did not allow auto-start")
	}
}

func TestNormalStoreOpenFencesHandoffUnlessExplicitReadOnlyProbe(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := ownershiphandoff.Journal{Request: ownershiphandoff.Request{CityRoot: root, Root: root, Database: "beads", Workspace: "w", Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}, Phase: ownershiphandoff.PhaseTargetConfigured, Owner: ownershiphandoff.OwnerLegacyGC}
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "ownership-handoff.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	err = checkNormalOwnershipHandoffOpen(beadsDir, &Config{})
	var coded interface{ HandoffErrorCode() string }
	if err == nil || !errors.As(err, &coded) || coded.HandoffErrorCode() != "lifecycle_busy" {
		t.Fatalf("ordinary open error=%v, want typed lifecycle_busy", err)
	}
	if err := checkNormalOwnershipHandoffOpen(beadsDir, &Config{OwnershipHandoffProbe: true, ReadOnly: true, DisableAutoStart: true}); err != nil {
		t.Fatalf("explicit strict handoff probe blocked: %v", err)
	}
	if err := checkNormalOwnershipHandoffOpen(beadsDir, &Config{OwnershipHandoffProbe: true, ReadOnly: true}); err == nil {
		t.Fatal("handoff probe without disabled auto-start was accepted")
	}
}

func TestAutoStart_DisabledWithExternalMode(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	got := resolveAutoStart(false, "", ServerModeExternal)
	if got != false {
		t.Error("resolveAutoStart should return false when server mode is External")
	}
}

// TestAutoStart_ExternalMode_CallerOverrideIgnored verifies that even a caller
// requesting AutoStart=true is overridden when the server is externally managed.
func TestAutoStart_ExternalMode_CallerOverrideIgnored(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	got := resolveAutoStart(true, "", ServerModeExternal)
	if got != false {
		t.Error("resolveAutoStart should return false with External mode even when caller requests true")
	}
}

// TestAutoStart_ConfigFalse_OverridesCallerTrue verifies that dolt.auto-start: false
// in config.yaml takes precedence over a caller passing current=true. This is the
// core fix for the auto-start bug where ApplyCLIAutoStart and bootstrap paths
// hardcoded current=true, ignoring the user's config.yaml opt-out.
func TestAutoStart_ConfigFalse_OverridesCallerTrue(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	for _, cfgVal := range []string{"false", "False", "FALSE", "0", "off", "Off", "OFF"} {
		got := resolveAutoStart(true, cfgVal, ServerModeOwned)
		if got != false {
			t.Errorf("resolveAutoStart(true, %q, Owned) = true, want false: config.yaml opt-out must override caller", cfgVal)
		}
	}
}

// TestAutoStart_ConfigEmpty_CallerTrueWins verifies that when config.yaml
// does not set dolt.auto-start, the caller's current=true is respected.
func TestAutoStart_ConfigEmpty_CallerTrueWins(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	got := resolveAutoStart(true, "", ServerModeOwned)
	if got != true {
		t.Error("resolveAutoStart(true, \"\", Owned) should return true when config is not set")
	}
}

// TestAutoStart_EnvOverrideStillWins verifies that BEADS_DOLT_AUTO_START=0
// still takes precedence even in Owned mode (defense-in-depth).
func TestAutoStart_EnvOverrideStillWins(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	got := resolveAutoStart(true, "", ServerModeOwned) // owned mode, but env says no
	if got != false {
		t.Error("BEADS_DOLT_AUTO_START=0 should still disable auto-start in Owned mode")
	}
}

// TestAutoStartReleaseNoErrorWhenServerAlreadyStopped verifies that
// autoStartRelease does not return an error when the server is already
// gone, preventing false "failed to stop auto-started dolt server"
// warnings (GH#2670). Also verifies stale PID/port files are cleaned up.
func TestAutoStartReleaseNoErrorWhenServerAlreadyStopped(t *testing.T) {
	dir := t.TempDir()

	// Create stale PID/port files to verify cleanup
	pidFile := filepath.Join(dir, doltserver.PIDFileName)
	portFile := filepath.Join(dir, doltserver.PortFileName)
	if err := os.WriteFile(pidFile, []byte("999999999"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portFile, []byte("13307"), 0600); err != nil {
		t.Fatal(err)
	}

	// Simulate: acquire a reference, then release it when no server is running.
	autoStartAcquire(dir)
	t.Cleanup(func() {
		// Ensure global refcount is cleaned up even if test fails early.
		autoStartRefs.mu.Lock()
		delete(autoStartRefs.m, dir)
		autoStartRefs.mu.Unlock()
	})

	err := autoStartRelease(dir)
	if err != nil {
		t.Errorf("autoStartRelease should not error when server is already stopped, got: %v", err)
	}

	// Verify stale state files were cleaned up by Stop → cleanupStateFiles
	if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
		t.Error("PID file should be removed after autoStartRelease on dead server")
	}
	if _, statErr := os.Stat(portFile); !os.IsNotExist(statErr) {
		t.Error("port file should be removed after autoStartRelease on dead server")
	}
}

// TestAutoStartReleaseNilMap verifies autoStartRelease returns nil
// when the refcount map was never initialized.
func TestAutoStartReleaseNilMap(t *testing.T) {
	// Save and clear the global map.
	autoStartRefs.mu.Lock()
	saved := autoStartRefs.m
	autoStartRefs.m = nil
	autoStartRefs.mu.Unlock()
	t.Cleanup(func() {
		autoStartRefs.mu.Lock()
		autoStartRefs.m = saved
		autoStartRefs.mu.Unlock()
	})

	err := autoStartRelease("/nonexistent")
	if err != nil {
		t.Errorf("expected nil when map is nil, got: %v", err)
	}
}

// TestAutoStartReleaseRefcountAboveZero verifies autoStartRelease
// doesn't call Stop when the refcount is still above zero.
func TestAutoStartReleaseRefcountAboveZero(t *testing.T) {
	dir := t.TempDir()

	// Acquire twice so the first release leaves refcount > 0.
	autoStartAcquire(dir)
	autoStartAcquire(dir)
	t.Cleanup(func() {
		autoStartRefs.mu.Lock()
		delete(autoStartRefs.m, dir)
		autoStartRefs.mu.Unlock()
	})

	err := autoStartRelease(dir)
	if err != nil {
		t.Errorf("expected nil when refcount > 0, got: %v", err)
	}

	// Verify refcount is now 1, not 0.
	autoStartRefs.mu.Lock()
	count := autoStartRefs.m[dir]
	autoStartRefs.mu.Unlock()
	if count != 1 {
		t.Errorf("expected refcount 1, got %d", count)
	}
}
