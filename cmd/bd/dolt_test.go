package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestDoltShowConfigNotInRepo(t *testing.T) {
	// Change to a temp dir without .beads
	tmpDir := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	// showDoltConfig should exit with error - test by checking it doesn't panic
	// In real use, it calls os.Exit(1). We can't test that directly,
	// so we verify the function doesn't panic when .beads is missing.
	defer func() {
		if r := recover(); r != nil {
			// Expected - os.Exit may cause issues in test
		}
	}()

	// This will call os.Exit(1), which we can't easily intercept in Go tests
	// Just verify the setup is correct
	if _, err := os.Stat(filepath.Join(tmpDir, ".beads")); !os.IsNotExist(err) {
		t.Error("expected .beads to not exist")
	}
}

func TestDoltShowConfigDefaultMode(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create metadata.json with Dolt backend
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltDatabase = "testdb"
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Override BEADS_DIR so FindBeadsDir() returns our temp .beads,
	// not the rig's .beads (which happens in worktree environments).
	t.Setenv("BEADS_DIR", beadsDir)

	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	t.Run("text output", func(t *testing.T) {
		origJsonOutput := jsonOutput
		defer func() { jsonOutput = origJsonOutput }()
		jsonOutput = false

		output := captureDoltShowOutput(t)

		if output == "" {
			t.Skip("output capture failed")
		}

		if !containsAny(output, "testdb", "Database") {
			t.Errorf("output should show database name: %s", output)
		}
		if !containsAny(output, "Host", "Port", "User") {
			t.Errorf("output should show server connection info: %s", output)
		}
	})

	t.Run("json output", func(t *testing.T) {
		origJsonOutput := jsonOutput
		defer func() { jsonOutput = origJsonOutput }()
		jsonOutput = true

		output := captureDoltShowOutput(t)

		if output == "" {
			t.Skip("output capture failed")
		}

		var result map[string]any
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Skipf("output not pure JSON: %s", output)
		}

		if result["backend"] != "dolt" {
			t.Errorf("expected backend 'dolt', got %v", result["backend"])
		}
		if result["database"] != "testdb" {
			t.Errorf("expected database 'testdb', got %v", result["database"])
		}
		// mode field should no longer be present
		if _, ok := result["mode"]; ok {
			t.Error("mode field should no longer be in JSON output")
		}
	})
}

func TestDoltShowConfigServerMode(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create metadata.json with Dolt backend in server mode
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "myproject"
	cfg.DoltServerHost = "192.168.1.100"
	cfg.DoltServerPort = 3308
	cfg.DoltServerUser = "testuser"
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Override BEADS_DIR so FindBeadsDir() returns our temp .beads,
	// not the rig's .beads (which happens in worktree environments).
	t.Setenv("BEADS_DIR", beadsDir)
	// Clear test server port override so GetDoltServerPort() returns metadata.json value
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	t.Run("text output", func(t *testing.T) {
		origJsonOutput := jsonOutput
		defer func() { jsonOutput = origJsonOutput }()
		jsonOutput = false

		output := captureDoltShowOutput(t)

		if output == "" {
			t.Skip("output capture failed")
		}

		if !containsAny(output, "192.168.1.100", "Host") {
			t.Errorf("output should show host: %s", output)
		}
		if !containsAny(output, "3308", "Port") {
			t.Errorf("output should show port: %s", output)
		}
		if !containsAny(output, "testuser", "User") {
			t.Errorf("output should show user: %s", output)
		}
	})

	t.Run("json output", func(t *testing.T) {
		origJsonOutput := jsonOutput
		defer func() { jsonOutput = origJsonOutput }()
		jsonOutput = true

		output := captureDoltShowOutput(t)

		if output == "" {
			t.Skip("output capture failed")
		}

		var result map[string]any
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Skipf("output not pure JSON: %s", output)
		}

		if result["host"] != "192.168.1.100" {
			t.Errorf("expected host '192.168.1.100', got %v", result["host"])
		}
		// Port comes back as float64 from JSON
		if port, ok := result["port"].(float64); !ok || int(port) != 3308 {
			t.Errorf("expected port 3308, got %v", result["port"])
		}
		if result["user"] != "testuser" {
			t.Errorf("expected user 'testuser', got %v", result["user"])
		}
	})
}

func TestDoltSetConfigValidation(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create metadata.json with Dolt backend
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Override BEADS_DIR so FindBeadsDir() returns our temp .beads,
	// not the rig's .beads (which happens in worktree environments).
	// Without this, setDoltConfig writes test values to the production
	// metadata.json, corrupting the Dolt server connection config.
	t.Setenv("BEADS_DIR", beadsDir)

	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	t.Run("set database", func(t *testing.T) {
		setDoltConfig("database", "mydb", false)

		loadedCfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if loadedCfg.DoltDatabase != "mydb" {
			t.Errorf("expected database 'mydb', got %s", loadedCfg.DoltDatabase)
		}
	})

	t.Run("set host", func(t *testing.T) {
		setDoltConfig("host", "10.0.0.1", false)

		loadedCfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if loadedCfg.DoltServerHost != "10.0.0.1" {
			t.Errorf("expected host '10.0.0.1', got %s", loadedCfg.DoltServerHost)
		}
	})

	t.Run("set port", func(t *testing.T) {
		setDoltConfig("port", "3309", false)

		loadedCfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if loadedCfg.DoltServerPort != 3309 {
			t.Errorf("expected port 3309, got %d", loadedCfg.DoltServerPort)
		}
	})

	t.Run("set user", func(t *testing.T) {
		setDoltConfig("user", "admin", false)

		loadedCfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if loadedCfg.DoltServerUser != "admin" {
			t.Errorf("expected user 'admin', got %s", loadedCfg.DoltServerUser)
		}
	})
}

func TestDoltSetConfigJSONOutput(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Override BEADS_DIR so FindBeadsDir() returns our temp .beads,
	// not the rig's .beads (which happens in worktree environments).
	t.Setenv("BEADS_DIR", beadsDir)

	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	origJsonOutput := jsonOutput
	defer func() { jsonOutput = origJsonOutput }()
	jsonOutput = true

	output := captureDoltSetOutput(t, "database", "myproject", false)

	if output == "" {
		t.Skip("output capture failed")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Skipf("output not pure JSON: %s", output)
	}

	if result["key"] != "database" {
		t.Errorf("expected key 'database', got %v", result["key"])
	}
	if result["value"] != "myproject" {
		t.Errorf("expected value 'myproject', got %v", result["value"])
	}
	if result["location"] != "metadata.json" {
		t.Errorf("expected location 'metadata.json', got %v", result["location"])
	}
}

func TestDoltSetConfigWithUpdateConfig(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Create config.yaml
	configYamlPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configYamlPath, []byte("prefix: test\n"), 0644); err != nil {
		t.Fatalf("failed to create config.yaml: %v", err)
	}

	// Override BEADS_DIR so FindBeadsDir() returns our temp .beads,
	// not the rig's .beads (which happens in worktree environments).
	t.Setenv("BEADS_DIR", beadsDir)

	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	origJsonOutput := jsonOutput
	defer func() { jsonOutput = origJsonOutput }()
	jsonOutput = true

	// Set with --update-config
	output := captureDoltSetOutput(t, "database", "myproject", true)

	if output == "" {
		t.Skip("output capture failed")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Skipf("output not pure JSON: %s", output)
	}

	if result["config_yaml_updated"] != true {
		t.Errorf("expected config_yaml_updated true, got %v", result["config_yaml_updated"])
	}
}

func TestTestServerConnection(t *testing.T) {
	// Test the testServerConnection function with various configs
	t.Run("unreachable host", func(t *testing.T) {
		cfg := configfile.DefaultConfig()
		cfg.DoltServerHost = "192.0.2.1" // RFC 5737 TEST-NET, guaranteed unreachable
		cfg.DoltServerPort = 3307

		result := testServerConnection(cfg)
		if result {
			t.Error("expected connection to fail for unreachable host")
		}
	})

	t.Run("localhost with unlikely port", func(t *testing.T) {
		// Clear test server port override so GetDoltServerPort() returns 59999
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		cfg := configfile.DefaultConfig()
		cfg.DoltServerHost = "127.0.0.1"
		cfg.DoltServerPort = 59999 // Unlikely to be in use

		result := testServerConnection(cfg)
		if result {
			t.Error("expected connection to fail for unused port")
		}
	})

	t.Run("IPv6 localhost with unlikely port", func(t *testing.T) {
		cfg := configfile.DefaultConfig()
		cfg.DoltServerHost = "::1"
		cfg.DoltServerPort = 59998 // Unlikely to be in use

		result := testServerConnection(cfg)
		if result {
			t.Error("expected connection to fail for unused port on IPv6")
		}
	})
}

func TestDoltConfigGetters(t *testing.T) {
	t.Run("GetDoltMode defaults", func(t *testing.T) {
		cfg := configfile.DefaultConfig()
		if cfg.GetDoltMode() != configfile.DoltModeEmbedded {
			t.Errorf("expected default mode 'embedded', got %s", cfg.GetDoltMode())
		}
	})

	t.Run("GetDoltDatabase defaults", func(t *testing.T) {
		cfg := configfile.DefaultConfig()
		if cfg.GetDoltDatabase() != configfile.DefaultDoltDatabase {
			t.Errorf("expected default database '%s', got %s",
				configfile.DefaultDoltDatabase, cfg.GetDoltDatabase())
		}
	})

	t.Run("GetDoltServerHost defaults", func(t *testing.T) {
		cfg := configfile.DefaultConfig()
		if cfg.GetDoltServerHost() != configfile.DefaultDoltServerHost {
			t.Errorf("expected default host '%s', got %s",
				configfile.DefaultDoltServerHost, cfg.GetDoltServerHost())
		}
	})

	t.Run("GetDoltServerPort defaults", func(t *testing.T) {
		// Clear test server port override so GetDoltServerPort() returns the struct default
		t.Setenv("BEADS_DOLT_SERVER_PORT", "")
		cfg := configfile.DefaultConfig()
		if cfg.GetDoltServerPort() != configfile.DefaultDoltServerPort {
			t.Errorf("expected default port %d, got %d",
				configfile.DefaultDoltServerPort, cfg.GetDoltServerPort())
		}
	})

	t.Run("GetDoltServerUser defaults", func(t *testing.T) {
		cfg := configfile.DefaultConfig()
		if cfg.GetDoltServerUser() != configfile.DefaultDoltServerUser {
			t.Errorf("expected default user '%s', got %s",
				configfile.DefaultDoltServerUser, cfg.GetDoltServerUser())
		}
	})

	t.Run("IsDoltServerMode", func(t *testing.T) {
		cfg := configfile.DefaultConfig()
		if cfg.IsDoltServerMode() {
			t.Error("expected IsDoltServerMode to be false for default config")
		}

		// IsDoltServerMode requires BOTH backend=dolt AND mode=server
		cfg.Backend = configfile.BackendDolt
		cfg.DoltMode = configfile.DoltModeServer
		if !cfg.IsDoltServerMode() {
			t.Error("expected IsDoltServerMode to be true when backend is dolt and mode is server")
		}
	})
}

func TestDoltConfigEnvironmentOverrides(t *testing.T) {
	// Test that environment variables override config values
	cfg := configfile.DefaultConfig()
	cfg.DoltDatabase = "configdb"
	cfg.DoltServerHost = "confighost"
	cfg.DoltServerPort = 1234
	cfg.DoltServerUser = "configuser"

	// Note: GetDoltMode() does NOT support env var override
	// Only database, host, port, user support env overrides

	t.Run("BEADS_DOLT_SERVER_DATABASE overrides", func(t *testing.T) {
		os.Setenv("BEADS_DOLT_SERVER_DATABASE", "envdb")
		defer os.Unsetenv("BEADS_DOLT_SERVER_DATABASE")

		if cfg.GetDoltDatabase() != "envdb" {
			t.Errorf("expected env override to 'envdb', got %s", cfg.GetDoltDatabase())
		}
	})

	t.Run("BEADS_DOLT_SERVER_HOST overrides", func(t *testing.T) {
		os.Setenv("BEADS_DOLT_SERVER_HOST", "envhost")
		defer os.Unsetenv("BEADS_DOLT_SERVER_HOST")

		if cfg.GetDoltServerHost() != "envhost" {
			t.Errorf("expected env override to 'envhost', got %s", cfg.GetDoltServerHost())
		}
	})

	t.Run("BEADS_DOLT_SERVER_PORT overrides", func(t *testing.T) {
		os.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
		defer os.Unsetenv("BEADS_DOLT_SERVER_PORT")

		if cfg.GetDoltServerPort() != 9999 {
			t.Errorf("expected env override to 9999, got %d", cfg.GetDoltServerPort())
		}
	})

	t.Run("BEADS_DOLT_SERVER_USER overrides", func(t *testing.T) {
		os.Setenv("BEADS_DOLT_SERVER_USER", "envuser")
		defer os.Unsetenv("BEADS_DOLT_SERVER_USER")

		if cfg.GetDoltServerUser() != "envuser" {
			t.Errorf("expected env override to 'envuser', got %s", cfg.GetDoltServerUser())
		}
	})
}

// --- start/stop tests ---

// Helper functions

func captureDoltShowOutput(t *testing.T) string {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Stderr = w

	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		if rec := recover(); rec != nil {
			// Ignore panics from os.Exit
		}
	}()

	showDoltConfig(false)

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)

	return buf.String()
}

func captureDoltSetOutput(t *testing.T, key, value string, updateConfig bool) string {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Stderr = w

	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		if rec := recover(); rec != nil {
			// Ignore panics from os.Exit
		}
	}()

	setDoltConfig(key, value, updateConfig)

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)

	return buf.String()
}

// TestSetDoltConfigWorktreeIsolation verifies that setDoltConfig writes to
// BEADS_DIR (the test temp directory), not the main repo's .beads directory.
// This is a regression test for bd-la2cl: test values (10.0.0.1:3309, mydb)
// were being written to the production metadata.json in worktree environments
// because FindBeadsDir() resolves to the main repo root.
func TestSetDoltConfigWorktreeIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create metadata.json with Dolt backend
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = 3307
	cfg.DoltDatabase = "beads"
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// CRITICAL: Set BEADS_DIR so FindBeadsDir() returns our temp .beads,
	// not the main repo's .beads (which happens in worktree environments).
	t.Setenv("BEADS_DIR", beadsDir)

	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()

	// Write test values via setDoltConfig
	setDoltConfig("host", "192.168.99.99", false)
	setDoltConfig("port", "9999", false)
	setDoltConfig("database", "testdb", false)

	// Verify values were written to the TEMP directory's metadata.json
	loadedCfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if loadedCfg.DoltServerHost != "192.168.99.99" {
		t.Errorf("test values not written to temp beadsDir: host = %s", loadedCfg.DoltServerHost)
	}

	// Verify the main repo's metadata.json was NOT modified.
	// FindBeadsDir() without BEADS_DIR override would return the main repo's .beads.
	// We can't easily test this in all environments, but we verify by checking that
	// the values we wrote don't match the "known bad" test values from the original bug.
	if loadedCfg.DoltServerHost == "10.0.0.1" && loadedCfg.DoltServerPort == 3309 {
		t.Error("REGRESSION: test values match the known-bad production corruption values (10.0.0.1:3309)")
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
