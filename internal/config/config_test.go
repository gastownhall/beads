package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitialize(t *testing.T) {
	// Test that initialization doesn't error
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	
	if v == nil {
		t.Fatal("viper instance is nil after Initialize()")
	}
}

func TestDefaults(t *testing.T) {
	// Reset viper for test isolation
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	
	tests := []struct {
		key      string
		expected interface{}
		getter   func(string) interface{}
	}{
		{"json", false, func(k string) interface{} { return GetBool(k) }},
		{"no-daemon", false, func(k string) interface{} { return GetBool(k) }},
		{"no-auto-flush", false, func(k string) interface{} { return GetBool(k) }},
		{"no-auto-import", false, func(k string) interface{} { return GetBool(k) }},
		{"db", "", func(k string) interface{} { return GetString(k) }},
		{"actor", "", func(k string) interface{} { return GetString(k) }},
		{"flush-debounce", 30 * time.Second, func(k string) interface{} { return GetDuration(k) }},
		{"auto-start-daemon", true, func(k string) interface{} { return GetBool(k) }},
	}
	
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := tt.getter(tt.key)
			if got != tt.expected {
				t.Errorf("GetXXX(%q) = %v, want %v", tt.key, got, tt.expected)
			}
		})
	}
}

func TestEnvironmentBinding(t *testing.T) {
	// Test environment variable binding
	tests := []struct {
		envVar   string
		key      string
		value    string
		expected interface{}
		getter   func(string) interface{}
	}{
		{"BD_JSON", "json", "true", true, func(k string) interface{} { return GetBool(k) }},
		{"BD_NO_DAEMON", "no-daemon", "true", true, func(k string) interface{} { return GetBool(k) }},
		{"BD_ACTOR", "actor", "testuser", "testuser", func(k string) interface{} { return GetString(k) }},
		{"BD_DB", "db", "/tmp/test.db", "/tmp/test.db", func(k string) interface{} { return GetString(k) }},
		{"BEADS_FLUSH_DEBOUNCE", "flush-debounce", "10s", 10 * time.Second, func(k string) interface{} { return GetDuration(k) }},
		{"BEADS_AUTO_START_DAEMON", "auto-start-daemon", "false", false, func(k string) interface{} { return GetBool(k) }},
	}
	
	for _, tt := range tests {
		t.Run(tt.envVar, func(t *testing.T) {
			// Set environment variable
			oldValue := os.Getenv(tt.envVar)
			_ = os.Setenv(tt.envVar, tt.value)
			defer os.Setenv(tt.envVar, oldValue)
			
			// Re-initialize viper to pick up env var
			err := Initialize()
			if err != nil {
				t.Fatalf("Initialize() returned error: %v", err)
			}
			
			got := tt.getter(tt.key)
			if got != tt.expected {
				t.Errorf("GetXXX(%q) with %s=%s = %v, want %v", tt.key, tt.envVar, tt.value, got, tt.expected)
			}
		})
	}
}

func TestConfigFile(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir := t.TempDir()
	
	// Create a config file
	configContent := `
json: true
no-daemon: true
actor: configuser
flush-debounce: 15s
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	
	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Move config to .beads directory
	beadsConfigPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.Rename(configPath, beadsConfigPath); err != nil {
		t.Fatalf("failed to move config file: %v", err)
	}

	// Change to tmp directory so config file is discovered
	t.Chdir(tmpDir)

	// Initialize viper
	var err error
	err = Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	
	// Test that config file values are loaded
	if got := GetBool("json"); got != true {
		t.Errorf("GetBool(json) = %v, want true", got)
	}
	
	if got := GetBool("no-daemon"); got != true {
		t.Errorf("GetBool(no-daemon) = %v, want true", got)
	}
	
	if got := GetString("actor"); got != "configuser" {
		t.Errorf("GetString(actor) = %q, want \"configuser\"", got)
	}
	
	if got := GetDuration("flush-debounce"); got != 15*time.Second {
		t.Errorf("GetDuration(flush-debounce) = %v, want 15s", got)
	}
}

func TestConfigPrecedence(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir := t.TempDir()
	
	// Create a config file with json: false
	configContent := `json: false`
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	
	// Change to tmp directory
	t.Chdir(tmpDir)

	// Test 1: Config file value (json: false)
	var err error
	err = Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	
	if got := GetBool("json"); got != false {
		t.Errorf("GetBool(json) from config file = %v, want false", got)
	}
	
	// Test 2: Environment variable overrides config file
	_ = os.Setenv("BD_JSON", "true")
	defer func() { _ = os.Unsetenv("BD_JSON") }()
	
	err = Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	
	if got := GetBool("json"); got != true {
		t.Errorf("GetBool(json) with env var = %v, want true (env should override config)", got)
	}
}

func TestSetAndGet(t *testing.T) {
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}
	
	// Test Set and Get
	Set("test-key", "test-value")
	if got := GetString("test-key"); got != "test-value" {
		t.Errorf("GetString(test-key) = %q, want \"test-value\"", got)
	}
	
	Set("test-bool", true)
	if got := GetBool("test-bool"); got != true {
		t.Errorf("GetBool(test-bool) = %v, want true", got)
	}
	
	Set("test-int", 42)
	if got := GetInt("test-int"); got != 42 {
		t.Errorf("GetInt(test-int) = %d, want 42", got)
	}
}

func TestAllSettings(t *testing.T) {
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	Set("custom-key", "custom-value")

	settings := AllSettings()
	if settings == nil {
		t.Fatal("AllSettings() returned nil")
	}

	// Check that our custom key is in the settings
	if val, ok := settings["custom-key"]; !ok || val != "custom-value" {
		t.Errorf("AllSettings() missing or incorrect custom-key: got %v", val)
	}
}

func TestGetStringSlice(t *testing.T) {
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test with Set
	Set("test-slice", []string{"a", "b", "c"})
	got := GetStringSlice("test-slice")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("GetStringSlice(test-slice) = %v, want [a b c]", got)
	}

	// Test with non-existent key - should return empty/nil slice
	got = GetStringSlice("nonexistent-key")
	if len(got) != 0 {
		t.Errorf("GetStringSlice(nonexistent-key) = %v, want empty slice", got)
	}
}

func TestGetStringSliceFromConfig(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir := t.TempDir()

	// Create a config file with string slice
	configContent := `
repos:
  primary: /path/to/primary
  additional:
    - /path/to/repo1
    - /path/to/repo2
`
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Change to tmp directory
	t.Chdir(tmpDir)

	// Initialize viper
	var err error
	err = Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test that string slice is loaded correctly
	got := GetStringSlice("repos.additional")
	if len(got) != 2 || got[0] != "/path/to/repo1" || got[1] != "/path/to/repo2" {
		t.Errorf("GetStringSlice(repos.additional) = %v, want [/path/to/repo1 /path/to/repo2]", got)
	}
}

func TestGetMultiRepoConfig(t *testing.T) {
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test when repos.primary is not set (single-repo mode)
	config := GetMultiRepoConfig()
	if config != nil {
		t.Errorf("GetMultiRepoConfig() with no repos.primary = %+v, want nil", config)
	}

	// Test when repos.primary is set (multi-repo mode)
	Set("repos.primary", "/path/to/primary")
	Set("repos.additional", []string{"/path/to/repo1", "/path/to/repo2"})

	config = GetMultiRepoConfig()
	if config == nil {
		t.Fatal("GetMultiRepoConfig() returned nil when repos.primary is set")
	}

	if config.Primary != "/path/to/primary" {
		t.Errorf("GetMultiRepoConfig().Primary = %q, want \"/path/to/primary\"", config.Primary)
	}

	if len(config.Additional) != 2 || config.Additional[0] != "/path/to/repo1" || config.Additional[1] != "/path/to/repo2" {
		t.Errorf("GetMultiRepoConfig().Additional = %v, want [/path/to/repo1 /path/to/repo2]", config.Additional)
	}
}

func TestGetMultiRepoConfigFromFile(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir := t.TempDir()

	// Create a config file with multi-repo config
	configContent := `
repos:
  primary: /main/repo
  additional:
    - /extra/repo1
    - /extra/repo2
    - /extra/repo3
`
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Change to tmp directory
	t.Chdir(tmpDir)

	// Initialize viper
	var err error
	err = Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test that multi-repo config is loaded correctly
	config := GetMultiRepoConfig()
	if config == nil {
		t.Fatal("GetMultiRepoConfig() returned nil")
	}

	if config.Primary != "/main/repo" {
		t.Errorf("GetMultiRepoConfig().Primary = %q, want \"/main/repo\"", config.Primary)
	}

	if len(config.Additional) != 3 {
		t.Errorf("GetMultiRepoConfig().Additional has %d items, want 3", len(config.Additional))
	}
}

func TestNilViperBehavior(t *testing.T) {
	// Save the current viper instance
	savedV := v

	// Set viper to nil to test nil-safety
	v = nil
	defer func() { v = savedV }()

	// All getters should return zero values without panicking
	if got := GetString("any-key"); got != "" {
		t.Errorf("GetString with nil viper = %q, want \"\"", got)
	}

	if got := GetBool("any-key"); got != false {
		t.Errorf("GetBool with nil viper = %v, want false", got)
	}

	if got := GetInt("any-key"); got != 0 {
		t.Errorf("GetInt with nil viper = %d, want 0", got)
	}

	if got := GetDuration("any-key"); got != 0 {
		t.Errorf("GetDuration with nil viper = %v, want 0", got)
	}

	if got := GetStringSlice("any-key"); got == nil || len(got) != 0 {
		t.Errorf("GetStringSlice with nil viper = %v, want empty slice", got)
	}

	if got := AllSettings(); got == nil || len(got) != 0 {
		t.Errorf("AllSettings with nil viper = %v, want empty map", got)
	}

	if got := GetMultiRepoConfig(); got != nil {
		t.Errorf("GetMultiRepoConfig with nil viper = %+v, want nil", got)
	}

	// Set should not panic
	Set("any-key", "any-value") // Should be a no-op
}
