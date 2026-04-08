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
		{"json", true},
		{"git.author", true},
		{"git.no-gpg-sign", true},

		// Prefix matches
		{"routing.mode", true},
		{"routing.custom-key", true},
		{"sync.require_confirmation_on_mass_delete", true},
		{"directory.labels", true},
		{"repos.primary", true},
		{"external_projects.beads", true},

		// Hierarchy settings (GH#995)
		{"hierarchy.max-depth", true},
		{"hierarchy.custom_setting", true}, // prefix match

		// Backup settings (GH#2358)
		{"backup.enabled", true},
		{"backup.interval", true},
		{"backup.git-push", true},
		{"backup.git-repo", true},
		{"backup.future-key", true}, // prefix match

		// Secret keys (stored in yaml to avoid leaking via Dolt push)
		{"github.token", true},
		{"linear.api_key", true},

		// Non-yaml keys (should return false)
		{"jira.url", false},
		{"jira.project", false},
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
			name:     "handle string value",
			content:  "# actor: \"\"",
			key:      "actor",
			value:    "testuser",
			expected: `actor: "testuser"`,
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

func TestSetYamlConfig(t *testing.T) {
	oldBeadsDir := os.Getenv("BEADS_DIR")
	if err := os.Unsetenv("BEADS_DIR"); err != nil {
		t.Fatalf("Failed to unset BEADS_DIR: %v", err)
	}
	defer func() {
		if oldBeadsDir == "" {
			_ = os.Unsetenv("BEADS_DIR")
		} else {
			_ = os.Setenv("BEADS_DIR", oldBeadsDir)
		}
	}()

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

func TestValidateYamlConfigValue_SharedServer(t *testing.T) {
	if err := validateYamlConfigValue("dolt.shared-server", "true"); err != nil {
		t.Errorf("expected 'true' to be valid: %v", err)
	}
	if err := validateYamlConfigValue("dolt.shared-server", "false"); err != nil {
		t.Errorf("expected 'false' to be valid: %v", err)
	}
	if err := validateYamlConfigValue("dolt.shared-server", "TRUE"); err != nil {
		t.Errorf("expected 'TRUE' to be valid (case-insensitive): %v", err)
	}
	if err := validateYamlConfigValue("dolt.shared-server", "maybe"); err == nil {
		t.Error("expected 'maybe' to be invalid")
	}
	if err := validateYamlConfigValue("dolt.shared-server", "1"); err == nil {
		t.Error("expected '1' to be invalid (not a boolean string)")
	}
}

func TestCommentOutYamlKey(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		key      string
		expected string
	}{
		{
			name:     "comment out existing key",
			content:  "backup.enabled: false\nother: value",
			key:      "backup.enabled",
			expected: "# backup.enabled: false\nother: value",
		},
		{
			name:     "already commented - no change",
			content:  "# backup.enabled: false\nother: value",
			key:      "backup.enabled",
			expected: "# backup.enabled: false\nother: value",
		},
		{
			name:     "key not found - no change",
			content:  "other: value",
			key:      "backup.enabled",
			expected: "other: value",
		},
		{
			name:     "preserve indentation",
			content:  "  backup.enabled: true\nother: value",
			key:      "backup.enabled",
			expected: "  # backup.enabled: true\nother: value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commentOutYamlKey(tt.content, tt.key)
			if got != tt.expected {
				t.Errorf("commentOutYamlKey() =\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

func TestUnsetYamlConfig(t *testing.T) {
	oldBeadsDir := os.Getenv("BEADS_DIR")
	if err := os.Unsetenv("BEADS_DIR"); err != nil {
		t.Fatalf("Failed to unset BEADS_DIR: %v", err)
	}
	defer func() {
		if oldBeadsDir == "" {
			_ = os.Unsetenv("BEADS_DIR")
		} else {
			_ = os.Setenv("BEADS_DIR", oldBeadsDir)
		}
	}()

	// Create a temp directory with .beads/config.yaml
	tmpDir, err := os.MkdirTemp("", "beads-yaml-unset-test-*")
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
backup.enabled: false
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

	// Test UnsetYamlConfig
	if err := UnsetYamlConfig("backup.enabled"); err != nil {
		t.Fatalf("UnsetYamlConfig() error = %v", err)
	}

	// Read back and verify
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config.yaml: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "# backup.enabled: false") {
		t.Errorf("config.yaml should contain commented-out backup.enabled, got:\n%s", contentStr)
	}
	if !strings.Contains(contentStr, "other-setting: value") {
		t.Errorf("config.yaml should preserve other settings, got:\n%s", contentStr)
	}
}

func TestFindProjectConfigYaml_UsesBEADS_DIRFirst(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-yaml-beadsdir-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Runtime config pointed to by BEADS_DIR
	runtimeBeadsDir := filepath.Join(tmpDir, "runtime", ".beads")
	if err := os.MkdirAll(runtimeBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create runtime .beads dir: %v", err)
	}
	runtimeConfigPath := filepath.Join(runtimeBeadsDir, "config.yaml")
	if err := os.WriteFile(runtimeConfigPath, []byte("no-git-ops: false\n"), 0644); err != nil {
		t.Fatalf("Failed to write runtime config.yaml: %v", err)
	}

	// Also create a local .beads/config.yaml under CWD that should be ignored
	cwdRepoDir := filepath.Join(tmpDir, "cwd-repo")
	if err := os.MkdirAll(filepath.Join(cwdRepoDir, ".beads"), 0755); err != nil {
		t.Fatalf("Failed to create cwd repo .beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwdRepoDir, ".beads", "config.yaml"), []byte("no-git-ops: true\n"), 0644); err != nil {
		t.Fatalf("Failed to write cwd config.yaml: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(cwdRepoDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	oldBeadsDir := os.Getenv("BEADS_DIR")
	if err := os.Setenv("BEADS_DIR", runtimeBeadsDir); err != nil {
		t.Fatalf("Failed to set BEADS_DIR: %v", err)
	}
	defer func() {
		if oldBeadsDir == "" {
			_ = os.Unsetenv("BEADS_DIR")
		} else {
			_ = os.Setenv("BEADS_DIR", oldBeadsDir)
		}
	}()

	got, err := findProjectConfigYaml()
	if err != nil {
		t.Fatalf("findProjectConfigYaml() error = %v", err)
	}
	if got != runtimeConfigPath {
		t.Fatalf("findProjectConfigYaml() = %q, want %q", got, runtimeConfigPath)
	}
}

func TestSetAndUnsetYamlConfig_WithBEADS_DIR_FromOutsideRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-yaml-beadsdir-outside-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	runtimeBeadsDir := filepath.Join(tmpDir, "runtime", ".beads")
	if err := os.MkdirAll(runtimeBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create runtime .beads dir: %v", err)
	}
	configPath := filepath.Join(runtimeBeadsDir, "config.yaml")
	initialConfig := "no-git-ops: false\nother-setting: value\n"
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write runtime config.yaml: %v", err)
	}

	// CWD intentionally has no .beads/config.yaml
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("Failed to create outside dir: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(outsideDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	oldBeadsDir := os.Getenv("BEADS_DIR")
	if err := os.Setenv("BEADS_DIR", runtimeBeadsDir); err != nil {
		t.Fatalf("Failed to set BEADS_DIR: %v", err)
	}
	defer func() {
		if oldBeadsDir == "" {
			_ = os.Unsetenv("BEADS_DIR")
		} else {
			_ = os.Setenv("BEADS_DIR", oldBeadsDir)
		}
	}()

	if err := SetYamlConfig("no-git-ops", "true"); err != nil {
		t.Fatalf("SetYamlConfig() error = %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read runtime config.yaml: %v", err)
	}
	contentStr := string(content)
	if !strings.Contains(contentStr, "no-git-ops: true") {
		t.Fatalf("expected runtime config to contain no-git-ops: true, got:\n%s", contentStr)
	}

	if err := UnsetYamlConfig("no-git-ops"); err != nil {
		t.Fatalf("UnsetYamlConfig() error = %v", err)
	}
	content, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read runtime config.yaml after unset: %v", err)
	}
	contentStr = string(content)
	if !strings.Contains(contentStr, "# no-git-ops: true") {
		t.Fatalf("expected runtime config to comment out no-git-ops, got:\n%s", contentStr)
	}
	if !strings.Contains(contentStr, "other-setting: value") {
		t.Fatalf("expected runtime config to preserve other settings, got:\n%s", contentStr)
	}
}
