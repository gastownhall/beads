package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envSnapshot saves and clears BD_/BEADS_ environment variables.
// Returns a restore function that should be deferred.
func envSnapshot(t *testing.T) func() {
	t.Helper()
	saved := make(map[string]string)
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "BD_") || strings.HasPrefix(env, "BEADS_") {
			parts := strings.SplitN(env, "=", 2)
			key := parts[0]
			saved[key] = os.Getenv(key)
			os.Unsetenv(key)
		}
	}
	return func() {
		// Clear any test-set variables first
		for _, env := range os.Environ() {
			if strings.HasPrefix(env, "BD_") || strings.HasPrefix(env, "BEADS_") {
				parts := strings.SplitN(env, "=", 2)
				os.Unsetenv(parts[0])
			}
		}
		// Restore original values
		for key, val := range saved {
			os.Setenv(key, val)
		}
	}
}

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
	// Isolate from environment variables
	restore := envSnapshot(t)
	defer restore()

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
	// Isolate from environment variables
	restore := envSnapshot(t)
	defer restore()

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

func TestGetIdentity(t *testing.T) {
	// Initialize viper
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test 1: Flag value takes precedence over everything
	got := GetIdentity("flag-identity")
	if got != "flag-identity" {
		t.Errorf("GetIdentity(flag-identity) = %q, want \"flag-identity\"", got)
	}

	// Test 2: Empty flag falls back to BEADS_IDENTITY env
	oldEnv := os.Getenv("BEADS_IDENTITY")
	_ = os.Setenv("BEADS_IDENTITY", "env-identity")
	defer func() {
		if oldEnv == "" {
			_ = os.Unsetenv("BEADS_IDENTITY")
		} else {
			_ = os.Setenv("BEADS_IDENTITY", oldEnv)
		}
	}()

	// Re-initialize to pick up env var
	_ = Initialize()
	got = GetIdentity("")
	if got != "env-identity" {
		t.Errorf("GetIdentity(\"\") with BEADS_IDENTITY = %q, want \"env-identity\"", got)
	}

	// Test 3: Without flag or env, should fall back to git user.name or hostname
	_ = os.Unsetenv("BEADS_IDENTITY")
	_ = Initialize()
	got = GetIdentity("")
	// We can't predict the exact value (depends on git config and hostname)
	// but it should not be empty or "unknown" on most systems
	if got == "" {
		t.Error("GetIdentity(\"\") without flag or env returned empty string")
	}
}

func TestGetExternalProjects(t *testing.T) {
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test default (empty map)
	got := GetExternalProjects()
	if got == nil {
		t.Error("GetExternalProjects() returned nil, want empty map")
	}
	if len(got) != 0 {
		t.Errorf("GetExternalProjects() = %v, want empty map", got)
	}

	// Test with Set
	Set("external_projects", map[string]string{
		"beads":   "../beads",
		"gastown": "/absolute/path/to/gastown",
	})

	got = GetExternalProjects()
	if len(got) != 2 {
		t.Errorf("GetExternalProjects() has %d items, want 2", len(got))
	}
	if got["beads"] != "../beads" {
		t.Errorf("GetExternalProjects()[beads] = %q, want \"../beads\"", got["beads"])
	}
	if got["gastown"] != "/absolute/path/to/gastown" {
		t.Errorf("GetExternalProjects()[gastown] = %q, want \"/absolute/path/to/gastown\"", got["gastown"])
	}
}

func TestGetExternalProjectsFromConfig(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir := t.TempDir()

	// Create a config file with external_projects
	configContent := `
external_projects:
  beads: ../beads
  gastown: /path/to/gastown
  other: ./relative/path
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
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test that external_projects is loaded correctly
	got := GetExternalProjects()
	if len(got) != 3 {
		t.Errorf("GetExternalProjects() has %d items, want 3", len(got))
	}
	if got["beads"] != "../beads" {
		t.Errorf("GetExternalProjects()[beads] = %q, want \"../beads\"", got["beads"])
	}
	if got["gastown"] != "/path/to/gastown" {
		t.Errorf("GetExternalProjects()[gastown] = %q, want \"/path/to/gastown\"", got["gastown"])
	}
	if got["other"] != "./relative/path" {
		t.Errorf("GetExternalProjects()[other] = %q, want \"./relative/path\"", got["other"])
	}
}

func TestResolveExternalProjectPath(t *testing.T) {
	// Create a temporary directory structure
	tmpDir := t.TempDir()

	// Create a project directory to resolve to
	projectDir := filepath.Join(tmpDir, "beads-project")
	if err := os.MkdirAll(projectDir, 0750); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	// Create config file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	configContent := `
external_projects:
  beads: beads-project
  missing: nonexistent-path
  absolute: ` + projectDir + `
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Change to tmp directory
	t.Chdir(tmpDir)

	// Initialize viper
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test resolving a relative path that exists
	got := ResolveExternalProjectPath("beads")
	if got != projectDir {
		t.Errorf("ResolveExternalProjectPath(beads) = %q, want %q", got, projectDir)
	}

	// Test resolving a path that doesn't exist
	got = ResolveExternalProjectPath("missing")
	if got != "" {
		t.Errorf("ResolveExternalProjectPath(missing) = %q, want empty string", got)
	}

	// Test resolving a project that isn't configured
	got = ResolveExternalProjectPath("unknown")
	if got != "" {
		t.Errorf("ResolveExternalProjectPath(unknown) = %q, want empty string", got)
	}

	// Test resolving an absolute path
	got = ResolveExternalProjectPath("absolute")
	if got != projectDir {
		t.Errorf("ResolveExternalProjectPath(absolute) = %q, want %q", got, projectDir)
	}
}

func TestGetIdentityFromConfig(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir := t.TempDir()

	// Create a config file with identity
	configContent := `identity: config-identity`
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Clear BEADS_IDENTITY env var
	oldEnv := os.Getenv("BEADS_IDENTITY")
	_ = os.Unsetenv("BEADS_IDENTITY")
	defer func() {
		if oldEnv != "" {
			_ = os.Setenv("BEADS_IDENTITY", oldEnv)
		}
	}()

	// Change to tmp directory
	t.Chdir(tmpDir)

	// Initialize viper
	err := Initialize()
	if err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test that identity from config file is used
	got := GetIdentity("")
	if got != "config-identity" {
		t.Errorf("GetIdentity(\"\") with config file = %q, want \"config-identity\"", got)
	}

	// Test that flag still takes precedence
	got = GetIdentity("flag-override")
	if got != "flag-override" {
		t.Errorf("GetIdentity(flag-override) = %q, want \"flag-override\"", got)
	}
}

func TestGetValueSource(t *testing.T) {
	// Initialize config
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	tests := []struct {
		name     string
		key      string
		setup    func()
		cleanup  func()
		expected ConfigSource
	}{
		{
			name:     "default value returns SourceDefault",
			key:      "json",
			setup:    func() {},
			cleanup:  func() {},
			expected: SourceDefault,
		},
		{
			name: "env var returns SourceEnvVar",
			key:  "json",
			setup: func() {
				os.Setenv("BD_JSON", "true")
			},
			cleanup: func() {
				os.Unsetenv("BD_JSON")
			},
			expected: SourceEnvVar,
		},
		{
			name: "BEADS_ prefixed env var returns SourceEnvVar",
			key:  "identity",
			setup: func() {
				os.Setenv("BEADS_IDENTITY", "test-identity")
			},
			cleanup: func() {
				os.Unsetenv("BEADS_IDENTITY")
			},
			expected: SourceEnvVar,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reinitialize to clear state
			if err := Initialize(); err != nil {
				t.Fatalf("Initialize() returned error: %v", err)
			}

			tt.setup()
			defer tt.cleanup()

			got := GetValueSource(tt.key)
			if got != tt.expected {
				t.Errorf("GetValueSource(%q) = %v, want %v", tt.key, got, tt.expected)
			}
		})
	}
}

func TestCheckOverrides_FlagOverridesEnvVar(t *testing.T) {
	// Initialize config
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Set an env var
	os.Setenv("BD_JSON", "true")
	defer os.Unsetenv("BD_JSON")

	// Simulate flag override
	flagOverrides := map[string]struct {
		Value  interface{}
		WasSet bool
	}{
		"json": {Value: false, WasSet: true},
	}

	overrides := CheckOverrides(flagOverrides)

	// Should detect that flag overrides env var
	found := false
	for _, o := range overrides {
		if o.Key == "json" && o.OverriddenBy == SourceFlag {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected to find flag override for 'json' key")
	}
}

func TestConfigSourceConstants(t *testing.T) {
	// Verify source constants have expected string values
	if SourceDefault != "default" {
		t.Errorf("SourceDefault = %q, want \"default\"", SourceDefault)
	}
	if SourceConfigFile != "config_file" {
		t.Errorf("SourceConfigFile = %q, want \"config_file\"", SourceConfigFile)
	}
	if SourceEnvVar != "env_var" {
		t.Errorf("SourceEnvVar = %q, want \"env_var\"", SourceEnvVar)
	}
	if SourceFlag != "flag" {
		t.Errorf("SourceFlag = %q, want \"flag\"", SourceFlag)
	}
}

// TestResolveExternalProjectPathFromRepoRoot tests that external_projects paths
// are resolved from repo root (parent of .beads/), NOT from CWD.
// This is the fix for oss-lbp (related to Bug 3 in the spec).
func TestResolveExternalProjectPathFromRepoRoot(t *testing.T) {
	// Helper to canonicalize paths for comparison (handles macOS /var -> /private/var symlink)
	canonicalize := func(path string) string {
		if path == "" {
			return ""
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return path
		}
		return resolved
	}

	t.Run("relative path resolved from repo root not CWD", func(t *testing.T) {
		// Create a repo structure:
		// tmpDir/
		//   .beads/
		//     config.yaml
		//   beads-project/     <- relative path should resolve here
		tmpDir := t.TempDir()

		// Create .beads directory with config file
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("failed to create .beads dir: %v", err)
		}

		// Create the target project directory
		projectDir := filepath.Join(tmpDir, "beads-project")
		if err := os.MkdirAll(projectDir, 0750); err != nil {
			t.Fatalf("failed to create project dir: %v", err)
		}

		// Create config file with relative path
		configContent := `
external_projects:
  beads: beads-project
`
		configPath := filepath.Join(beadsDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		// Change to a DIFFERENT directory (to test that CWD doesn't affect resolution)
		// This simulates daemon context where CWD is .beads/
		origDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		if err := os.Chdir(beadsDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer os.Chdir(origDir)

		// Reload config from the new location
		if err := Initialize(); err != nil {
			t.Fatalf("failed to initialize config: %v", err)
		}

		// Verify ConfigFileUsed() returns the config path
		usedConfig := ConfigFileUsed()
		if usedConfig == "" {
			t.Skip("config file not loaded - skipping test")
		}

		// Resolve the external project path
		got := ResolveExternalProjectPath("beads")

		// The path should resolve to tmpDir/beads-project (repo root + relative path)
		// NOT to .beads/beads-project (CWD + relative path)
		// Use canonicalize to handle macOS /var -> /private/var symlink
		if canonicalize(got) != canonicalize(projectDir) {
			t.Errorf("ResolveExternalProjectPath(beads) = %q, want %q", got, projectDir)
		}

		// Verify the wrong path doesn't exist (CWD-based resolution)
		wrongPath := filepath.Join(beadsDir, "beads-project")
		if canonicalize(got) == canonicalize(wrongPath) {
			t.Errorf("path was incorrectly resolved from CWD: %s", wrongPath)
		}
	})

	t.Run("CWD should not affect resolution", func(t *testing.T) {
		// Create two different directory structures
		tmpDir := t.TempDir()

		// Create main repo with .beads and target project
		mainRepoDir := filepath.Join(tmpDir, "main-repo")
		beadsDir := filepath.Join(mainRepoDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("failed to create .beads dir: %v", err)
		}

		// Create the target project as a sibling directory
		siblingProject := filepath.Join(tmpDir, "sibling-project")
		if err := os.MkdirAll(siblingProject, 0750); err != nil {
			t.Fatalf("failed to create sibling project: %v", err)
		}

		// Create config file with parent-relative path
		configContent := `
external_projects:
  sibling: ../sibling-project
`
		configPath := filepath.Join(beadsDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		// Test from multiple different CWDs
		// Note: We only test from mainRepoDir and beadsDir, not from tmpDir
		// because when CWD is tmpDir, the config file at mainRepoDir/.beads/config.yaml
		// won't be discovered (viper searches from CWD upward)
		testDirs := []string{
			mainRepoDir, // From repo root
			beadsDir,    // From .beads/ (daemon context)
		}

		for _, testDir := range testDirs {
			// Change to test directory
			origDir, err := os.Getwd()
			if err != nil {
				t.Fatalf("failed to get cwd: %v", err)
			}
			if err := os.Chdir(testDir); err != nil {
				t.Fatalf("failed to chdir to %s: %v", testDir, err)
			}

			// Reload config
			if err := Initialize(); err != nil {
				os.Chdir(origDir)
				t.Fatalf("failed to initialize config: %v", err)
			}

			// Resolve the external project path
			got := ResolveExternalProjectPath("sibling")

			// Restore CWD before checking result
			os.Chdir(origDir)

			// Path should always resolve to the sibling project,
			// regardless of which directory we were in
			// Use canonicalize to handle macOS /var -> /private/var symlink
			if canonicalize(got) != canonicalize(siblingProject) {
				t.Errorf("from CWD=%s: ResolveExternalProjectPath(sibling) = %q, want %q",
					testDir, got, siblingProject)
			}
		}
	})
}

func TestValidationConfigDefaults(t *testing.T) {
	// Isolate from environment variables
	restore := envSnapshot(t)
	defer restore()

	// Initialize config
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test validation.on-create default is "none"
	if got := GetString("validation.on-create"); got != "none" {
		t.Errorf("GetString(validation.on-create) = %q, want \"none\"", got)
	}

	// Test validation.on-sync default is "none"
	if got := GetString("validation.on-sync"); got != "none" {
		t.Errorf("GetString(validation.on-sync) = %q, want \"none\"", got)
	}
}

func TestValidationConfigFromFile(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir := t.TempDir()

	// Create a config file with validation settings
	configContent := `
validation:
  on-create: error
  on-sync: warn
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
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	// Test that validation settings are loaded correctly
	if got := GetString("validation.on-create"); got != "error" {
		t.Errorf("GetString(validation.on-create) = %q, want \"error\"", got)
	}
	if got := GetString("validation.on-sync"); got != "warn" {
		t.Errorf("GetString(validation.on-sync) = %q, want \"warn\"", got)
	}
}
