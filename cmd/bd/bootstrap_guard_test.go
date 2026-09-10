//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestIsGitCodeRepoURL verifies the URL classifier that guards sync.remote
// against accidental git code-repository URLs.
func TestIsGitCodeRepoURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		// .git suffix — always a code-repo signal regardless of host
		{"git+ssh://github.com/org/repo.git", true},
		{"https://github.com/org/repo.git", true},
		{"ssh://git@github.com/org/repo.git", true},
		{"git@github.com:org/repo.git", true},
		{"https://gitlab.com/org/repo.git", true},
		{"git+https://bitbucket.org/org/repo.git", true},
		{"https://codeberg.org/org/repo.git", true},
		// .git on unknown host: still blocked (Dolt DBs never use .git suffix)
		{"git+ssh://my-dolt.example.com/org/repo.git", true},

		// Well-known forges without .git suffix
		{"https://github.com/org/repo", true},
		{"git+ssh://github.com/org/db", true},
		{"https://gitlab.com/org/group/repo", true},
		{"https://bitbucket.org/org/repo", true},
		{"https://codeberg.org/org/repo", true},

		// github.com / gitlab.com subdomains
		{"https://raw.github.com/org/repo", true},
		{"https://api.github.com/repos/org/repo", true},
		{"https://example.gitlab.com/org/repo", true},

		// Valid Dolt-native schemes — never blocked
		{"dolthub://myorg/mydb", false},
		{"s3://bucket/path", false},
		{"gs://bucket/path", false},
		{"az://container/path", false},
		{"file:///tmp/doltdb", false},

		// git+ssh to a self-hosted Dolt remote — NOT a code repo
		{"git+ssh://my-self-hosted-dolt.example.com/org/db", false},
		{"https://doltremoteapi.dolthub.com/org/db", false},
		{"https://doltremoteapi.example.com/mydb", false},

		// SCP-style to non-forge — allowed
		{"git@my-dolt.example.com:org/db", false},

		// Edge: empty string
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := isGitCodeRepoURL(tt.url)
			if got != tt.want {
				t.Errorf("isGitCodeRepoURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// setupSyncRemoteConfig writes sync.remote to .beads/config.yaml and
// initializes the config subsystem so resolveSyncRemote() can read it.
// Returns a cleanup function that resets config state.
func setupSyncRemoteConfig(t *testing.T, beadsDir, remote string) func() {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(beadsDir, "config.yaml"),
		[]byte("sync.remote: "+remote+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	config.ResetForTesting()
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	return func() { config.ResetForTesting() }
}

// TestDetectBootstrapAction_GitCodeRepoSyncRemoteBlocked verifies that a
// sync.remote pointing at a git code-repository host is rejected (action=none)
// instead of triggering DOLT_CLONE.
func TestDetectBootstrapAction_GitCodeRepoSyncRemoteBlocked(t *testing.T) {
	for _, remote := range []string{
		"git+ssh://github.com/org/repo.git",
		"https://github.com/org/repo.git",
		"git@github.com:org/repo.git",
		"https://gitlab.com/group/repo.git",
	} {
		t.Run(remote, func(t *testing.T) {
			snapshotBootstrapEnv(t)

			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			cleanup := setupSyncRemoteConfig(t, beadsDir, remote)
			defer cleanup()

			oldWd, _ := os.Getwd()
			defer func() { _ = os.Chdir(oldWd) }()
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatal(err)
			}

			cfg := configfile.DefaultConfig()
			plan := detectBootstrapAction(beadsDir, cfg)

			if plan.Action != "none" {
				t.Errorf("remote=%q: action=%q, want %q (git code-repo URL must be blocked)", remote, plan.Action, "none")
			}
			if plan.SyncRemote != "" {
				t.Errorf("remote=%q: SyncRemote=%q, want empty (blocked URL must not propagate)", remote, plan.SyncRemote)
			}
		})
	}
}

// TestDetectBootstrapAction_ValidDoltSyncRemoteUnchanged verifies that a valid
// Dolt remote URL is still accepted as-is (no regression from Layer 1 guard).
func TestDetectBootstrapAction_ValidDoltSyncRemoteUnchanged(t *testing.T) {
	for _, remote := range []string{
		"https://doltremoteapi.dolthub.com/org/db",
		"dolthub://myorg/mydb",
		"git+ssh://my-self-hosted-dolt.example.com/org/db",
	} {
		t.Run(remote, func(t *testing.T) {
			snapshotBootstrapEnv(t)

			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			cleanup := setupSyncRemoteConfig(t, beadsDir, remote)
			defer cleanup()

			oldWd, _ := os.Getwd()
			defer func() { _ = os.Chdir(oldWd) }()
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatal(err)
			}

			cfg := configfile.DefaultConfig()
			plan := detectBootstrapAction(beadsDir, cfg)

			if plan.Action != "sync" {
				t.Errorf("remote=%q: action=%q, want %q (valid Dolt remote must not be blocked)", remote, plan.Action, "sync")
			}
			if plan.SyncRemote != remote {
				t.Errorf("remote=%q: SyncRemote=%q, want same URL", remote, plan.SyncRemote)
			}
		})
	}
}

// TestDetectBootstrapAction_ServerTransientRestart_DBFoundAfterRetry verifies
// that when the server is reachable but the DB appears absent on the first probe
// (e.g. during a managed Dolt restart), the retry loop waits and eventually finds
// the DB — returning action=none instead of falling through to sync/init
// .
func TestDetectBootstrapAction_ServerTransientRestart_DBFoundAfterRetry(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	doltDataDir := filepath.Join(tmpDir, "dolt-data")
	if err := os.MkdirAll(filepath.Join(doltDataDir, "mydb"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_DATA_DIR", doltDataDir)

	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.DoltDataDir = doltDataDir

	// Simulate a restart: DB absent for first 2 probes, then found.
	callCount := 0
	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(_ bootstrapServerProbeConfig) bootstrapServerDBCheck {
		callCount++
		if callCount <= 2 {
			return bootstrapServerDBCheck{Exists: false, Reachable: true}
		}
		return bootstrapServerDBCheck{Exists: true, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "none" {
		t.Errorf("action=%q, want %q — DB found after retry must suppress clone", plan.Action, "none")
	}
	if !plan.HasExisting {
		t.Error("HasExisting=false, want true — DB was found on the retry probe")
	}
	if callCount < 3 {
		t.Errorf("checkBootstrapServerDB called %d times, want >=3 (retry must fire)", callCount)
	}
}

// TestDetectBootstrapAction_ServerGenuinelyAbsent_FallsThrough verifies that
// when the server is reachable but the DB is genuinely absent after all retries,
// detection falls through to init (no change from pre-retry behavior).
func TestDetectBootstrapAction_ServerGenuinelyAbsent_FallsThrough(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	doltDataDir := filepath.Join(tmpDir, "dolt-data")
	if err := os.MkdirAll(filepath.Join(doltDataDir, "mydb"), 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_DATA_DIR", doltDataDir)

	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "mydb"
	cfg.DoltDataDir = doltDataDir

	// DB is always absent — simulates a genuinely missing database.
	origCheck := checkBootstrapServerDB
	checkBootstrapServerDB = func(_ bootstrapServerProbeConfig) bootstrapServerDBCheck {
		return bootstrapServerDBCheck{Exists: false, Reachable: true}
	}
	defer func() { checkBootstrapServerDB = origCheck }()

	origDelay := bootstrapRetryDelay
	bootstrapRetryDelay = func(time.Duration) {}
	defer func() { bootstrapRetryDelay = origDelay }()

	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action == "none" {
		t.Errorf("action=none, want non-none — genuinely absent DB must not block recovery")
	}
	// No backup or JSONL → should fall through to init.
	if plan.Action != "init" {
		t.Errorf("action=%q, want %q — no backup/jsonl available, fresh init expected", plan.Action, "init")
	}
}
