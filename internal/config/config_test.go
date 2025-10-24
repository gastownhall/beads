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
		{"flush-debounce", 5 * time.Second, func(k string) interface{} { return GetDuration(k) }},
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
	
	// Change to tmp directory so config file is discovered
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	defer os.Chdir(origDir)
	
	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	
	// Move config to .beads directory
	beadsConfigPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.Rename(configPath, beadsConfigPath); err != nil {
		t.Fatalf("failed to move config file: %v", err)
	}
	
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}
	
	// Initialize viper
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
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}
	
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	
	// Change to tmp directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	defer os.Chdir(origDir)
	
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}
	
	// Test 1: Config file value (json: false)
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
