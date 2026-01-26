package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsYamlOnlyKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		// Exact matches
		{"no-db", true},
		{"no-daemon", true},
		{"no-auto-flush", true},
		{"json", true},
		{"auto-start-daemon", true},
		{"flush-debounce", true},
		{"git.author", true},
		{"git.no-gpg-sign", true},

		// Prefix matches
		{"routing.mode", true},
		{"routing.custom-key", true},
		{"sync.branch", true},
		{"sync.require_confirmation_on_mass_delete", true},
		{"directory.labels", true},
		{"repos.primary", true},
		{"external_projects.beads", true},

		// Daemon settings (GH#871)
		{"daemon.auto_commit", true},
		{"daemon.auto_push", true},
		{"daemon.auto_pull", true},
		{"daemon.custom_setting", true}, // prefix match

		// Hierarchy settings (GH#995)
		{"hierarchy.max-depth", true},
		{"hierarchy.custom_setting", true}, // prefix match

		// SQLite keys (should return false)
		{"jira.url", false},
		{"jira.project", false},
		{"linear.api_key", false},
		{"github.org", false},
		{"custom.setting", false},
		{"status.custom", false},
		{"issue_prefix", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsYamlOnlyKey(tt.key)
			if got != tt.expected {
				t.Errorf("IsYamlOnlyKey(%q) = %v, want %v", tt.key, got, tt.expected)
			}
		})
	}
}

func TestUpdateYamlKey(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		key      string
		value    string
		expected string
	}{
		{
			name:     "update commented key",
			content:  "# no-db: false\nother: value",
			key:      "no-db",
			value:    "true",
			expected: "no-db: true\nother: value",
		},
		{
			name:     "update existing key",
			content:  "no-db: false\nother: value",
			key:      "no-db",
			value:    "true",
			expected: "no-db: true\nother: value",
		},
		{
			name:     "add new key",
			content:  "other: value",
			key:      "no-db",
			value:    "true",
			expected: "other: value\n\nno-db: true",
		},
		{
			name:     "preserve indentation",
			content:  "  # no-db: false\nother: value",
			key:      "no-db",
			value:    "true",
			expected: "  no-db: true\nother: value",
		},
		{
			name:     "handle string value",
			content:  "# actor: \"\"\nother: value",
			key:      "actor",
			value:    "steve",
			expected: "actor: \"steve\"\nother: value",
		},
		{
			name:     "handle duration value",
			content:  "# flush-debounce: \"5s\"",
			key:      "flush-debounce",
			value:    "30s",
			expected: "flush-debounce: 30s",
		},
		{
			name:     "quote special characters",
			content:  "other: value",
			key:      "actor",
			value:    "user: name",
			expected: "other: value\n\nactor: \"user: name\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updateYamlKey(tt.content, tt.key, tt.value)
			if err != nil {
				t.Fatalf("updateYamlKey() error = %v", err)
			}
			if got != tt.expected {
				t.Errorf("updateYamlKey() =\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

func TestFormatYamlValue(t *testing.T) {
	tests := []struct {
		value    string
		expected string
	}{
		{"true", "true"},
		{"false", "false"},
		{"TRUE", "true"},
		{"FALSE", "false"},
		{"123", "123"},
		{"3.14", "3.14"},
		{"30s", "30s"},
		{"5m", "5m"},
		{"simple", "\"simple\""},
		{"has space", "\"has space\""},
		{"has:colon", "\"has:colon\""},
		{"has#hash", "\"has#hash\""},
		{" leading", "\" leading\""},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := formatYamlValue(tt.value)
			if got != tt.expected {
				t.Errorf("formatYamlValue(%q) = %q, want %q", tt.value, got, tt.expected)
			}
		})
	}
}

func TestNormalizeYamlKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"sync.branch", "sync-branch"},   // alias should be normalized
		{"sync-branch", "sync-branch"},   // already canonical
		{"no-db", "no-db"},               // no alias, unchanged
		{"json", "json"},                 // no alias, unchanged
		{"routing.mode", "routing.mode"}, // no alias for this one
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeYamlKey(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeYamlKey(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSetYamlConfig_KeyNormalization(t *testing.T) {
	// Create a temp directory with .beads/config.yaml
	tmpDir, err := os.MkdirTemp("", "beads-yaml-key-norm-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	initialConfig := `# Beads Config
sync-branch: old-value
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Change to temp directory for the test
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Test SetYamlConfig with aliased key (sync.branch should write as sync-branch)
	if err := SetYamlConfig("sync.branch", "new-value"); err != nil {
		t.Fatalf("SetYamlConfig() error = %v", err)
	}

	// Read back and verify
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config.yaml: %v", err)
	}

	contentStr := string(content)
	// Should update the existing sync-branch line, not add sync.branch
	if !strings.Contains(contentStr, "sync-branch: \"new-value\"") {
		t.Errorf("config.yaml should contain 'sync-branch: \"new-value\"', got:\n%s", contentStr)
	}
	if strings.Contains(contentStr, "sync.branch") {
		t.Errorf("config.yaml should NOT contain 'sync.branch' (should be normalized to sync-branch), got:\n%s", contentStr)
	}
}

func TestGetYamlConfig_KeyNormalization(t *testing.T) {
	// Create a temp directory with .beads/config.yaml
	tmpDir, err := os.MkdirTemp("", "beads-yaml-get-key-norm-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Write config with canonical key name (sync-branch, not sync.branch)
	configPath := filepath.Join(beadsDir, "config.yaml")
	initialConfig := `# Beads Config
sync-branch: test-value
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Change to temp directory for the test
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Initialize viper to read the config
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// Test GetYamlConfig with aliased key (sync.branch should find sync-branch value)
	got := GetYamlConfig("sync.branch")
	if got != "test-value" {
		t.Errorf("GetYamlConfig(\"sync.branch\") = %q, want %q", got, "test-value")
	}

	// Also verify canonical key works
	got = GetYamlConfig("sync-branch")
	if got != "test-value" {
		t.Errorf("GetYamlConfig(\"sync-branch\") = %q, want %q", got, "test-value")
	}
}

func TestSetYamlConfig(t *testing.T) {
	// Create a temp directory with .beads/config.yaml
	tmpDir, err := os.MkdirTemp("", "beads-yaml-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	initialConfig := `# Beads Config
# no-db: false
other-setting: value
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Change to temp directory for the test
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	// Test SetYamlConfig
	if err := SetYamlConfig("no-db", "true"); err != nil {
		t.Fatalf("SetYamlConfig() error = %v", err)
	}

	// Read back and verify
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config.yaml: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "no-db: true") {
		t.Errorf("config.yaml should contain 'no-db: true', got:\n%s", contentStr)
	}
	if strings.Contains(contentStr, "# no-db") {
		t.Errorf("config.yaml should not have commented no-db, got:\n%s", contentStr)
	}
	if !strings.Contains(contentStr, "other-setting: value") {
		t.Errorf("config.yaml should preserve other settings, got:\n%s", contentStr)
	}
}

// TestValidateYamlConfigValue_HierarchyMaxDepth tests validation of hierarchy.max-depth (GH#995)
func TestValidateYamlConfigValue_HierarchyMaxDepth(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		expectErr bool
		errMsg    string
	}{
		{"valid positive integer", "5", false, ""},
		{"valid minimum value", "1", false, ""},
		{"valid large value", "100", false, ""},
		{"invalid zero", "0", true, "hierarchy.max-depth must be at least 1, got 0"},
		{"invalid negative", "-1", true, "hierarchy.max-depth must be at least 1, got -1"},
		{"invalid non-integer", "abc", true, "hierarchy.max-depth must be a positive integer, got \"abc\""},
		{"invalid float", "3.5", true, "hierarchy.max-depth must be a positive integer, got \"3.5\""},
		{"invalid empty", "", true, "hierarchy.max-depth must be a positive integer, got \"\""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateYamlConfigValue("hierarchy.max-depth", tt.value)
			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error for value %q, got nil", tt.value)
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for value %q: %v", tt.value, err)
				}
			}
		})
	}
}

// TestValidateYamlConfigValue_OtherKeys tests that other keys are not validated
func TestValidateYamlConfigValue_OtherKeys(t *testing.T) {
	// Other keys should pass validation regardless of value
	err := validateYamlConfigValue("no-db", "invalid")
	if err != nil {
		t.Errorf("unexpected error for no-db: %v", err)
	}

	err = validateYamlConfigValue("routing.mode", "anything")
	if err != nil {
		t.Errorf("unexpected error for routing.mode: %v", err)
	}
}

// TestValidateYamlConfigValue_SyncBranch_RejectsMain tests that main/master are rejected as sync branch (GH#1166)
func TestValidateYamlConfigValue_SyncBranch_RejectsMain(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"sync-branch main", "sync-branch", "main"},
		{"sync-branch master", "sync-branch", "master"},
		{"sync.branch main", "sync.branch", "main"},
		{"sync.branch master", "sync.branch", "master"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateYamlConfigValue(tt.key, tt.value)
			if err == nil {
				t.Errorf("expected error for %s=%s, got nil", tt.key, tt.value)
			}
			if err != nil && !strings.Contains(err.Error(), "cannot use") {
				t.Errorf("expected 'cannot use' error, got: %v", err)
			}
		})
	}
}

// TestValidateYamlConfigValue_SyncBranch_AcceptsValid tests that valid branch names are accepted (GH#1166)
func TestValidateYamlConfigValue_SyncBranch_AcceptsValid(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"sync-branch beads-sync", "sync-branch", "beads-sync"},
		{"sync-branch feature/test", "sync-branch", "feature/test"},
		{"sync.branch beads-sync", "sync.branch", "beads-sync"},
		{"sync.branch develop", "sync.branch", "develop"},
		{"sync-branch empty", "sync-branch", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateYamlConfigValue(tt.key, tt.value)
			if err != nil {
				t.Errorf("unexpected error for %s=%s: %v", tt.key, tt.value, err)
			}
		})
	}
}
