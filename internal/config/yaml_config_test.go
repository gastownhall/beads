package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
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

		// Import settings
		{"import.path", true},
		{"import.orphan_handling", false},

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

func TestUpdateYamlKeyNested(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		key      string
		value    string
		wantBool *bool
		wantStr  string
	}{
		{
			name:     "update existing nested leaf",
			content:  "metrics:\n  disabled: false\n  endpoint: https://example.com\n",
			key:      "metrics.disabled",
			value:    "true",
			wantBool: boolPtr(true),
		},
		{
			name:    "update string leaf under existing block",
			content: "metrics:\n  disabled: false\n  endpoint: https://example.com\n",
			key:     "metrics.endpoint",
			value:   "https://updated.example.com",
			wantStr: "https://updated.example.com",
		},
		{
			name:     "create missing leaf under existing block",
			content:  "metrics:\n  endpoint: https://example.com\n",
			key:      "metrics.disabled",
			value:    "true",
			wantBool: boolPtr(true),
		},
		{
			name:     "create entire block when parent missing",
			content:  "other: value\n",
			key:      "metrics.disabled",
			value:    "true",
			wantBool: boolPtr(true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updateYamlKey(tt.content, tt.key, tt.value)
			if err != nil {
				t.Fatalf("updateYamlKey() error = %v", err)
			}

			var parsed map[string]interface{}
			if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
				t.Fatalf("result is not valid YAML: %v\n%s", err, got)
			}

			parts := strings.Split(tt.key, ".")
			leaf := walkMap(parsed, parts)
			if leaf == nil {
				t.Fatalf("key %q not found after update\nresult:\n%s", tt.key, got)
			}
			if tt.wantBool != nil {
				b, ok := leaf.(bool)
				if !ok {
					t.Fatalf("leaf is not bool: %T %v\nresult:\n%s", leaf, leaf, got)
				}
				if b != *tt.wantBool {
					t.Errorf("leaf = %v, want %v\nresult:\n%s", b, *tt.wantBool, got)
				}
			}
			if tt.wantStr != "" {
				s, ok := leaf.(string)
				if !ok {
					t.Fatalf("leaf is not string: %T %v\nresult:\n%s", leaf, leaf, got)
				}
				if s != tt.wantStr {
					t.Errorf("leaf = %q, want %q\nresult:\n%s", s, tt.wantStr, got)
				}
			}

			if strings.Contains(got, tt.key+":") && !strings.Contains(tt.content, tt.key+":") {
				t.Errorf("result contains flat dotted key %q (should have updated nested form instead)\n%s",
					tt.key, got)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

func walkMap(m map[string]interface{}, parts []string) interface{} {
	var cur interface{} = m
	for _, p := range parts {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur, ok = mm[p]
		if !ok {
			return nil
		}
	}
	return cur
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

func TestSetYamlConfigInDir_WritesTargetConfigDespiteLocalStub(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	previousV := v
	previousOverrides := overriddenKeys
	v = nil
	overriddenKeys = map[string]bool{}
	defer func() {
		v = previousV
		overriddenKeys = previousOverrides
	}()

	tmpDir := t.TempDir()
	targetBeadsDir := filepath.Join(tmpDir, "shared", ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0o755); err != nil {
		t.Fatalf("failed to create target beads dir: %v", err)
	}
	targetConfigPath := filepath.Join(targetBeadsDir, "config.yaml")
	if err := os.WriteFile(targetConfigPath, []byte("json: false\n"), 0o644); err != nil {
		t.Fatalf("failed to write target config.yaml: %v", err)
	}

	worktreeDir := filepath.Join(tmpDir, "worktree")
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create worktree stub beads dir: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(worktreeDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	const remoteURL = "git+ssh://git@example.com/acme/repo.git"
	if err := SetYamlConfigInDir(targetBeadsDir, "sync.remote", remoteURL); err != nil {
		t.Fatalf("SetYamlConfigInDir() error = %v", err)
	}

	targetContent, err := os.ReadFile(targetConfigPath)
	if err != nil {
		t.Fatalf("failed to read target config.yaml: %v", err)
	}
	if !strings.Contains(string(targetContent), remoteURL) {
		t.Fatalf("expected target config.yaml to contain %q, got:\n%s", remoteURL, string(targetContent))
	}

	if _, err := os.Stat(filepath.Join(worktreeDir, ".beads", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected worktree stub to remain untouched, got err=%v", err)
	}
}

func TestSetYamlConfigInDir_ValidatesBeforeOpeningConfig(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	err := SetYamlConfigInDir(beadsDir, "hierarchy.max-depth", "0")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "hierarchy.max-depth must be at least 1") {
		t.Fatalf("expected hierarchy validation error, got: %v", err)
	}
}

func TestSetYamlConfigInDir_MissingConfig(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	err := SetYamlConfigInDir(beadsDir, "json", "true")
	if err == nil {
		t.Fatal("expected missing config error")
	}
	if !strings.Contains(err.Error(), "no config.yaml found in") {
		t.Fatalf("expected missing config.yaml error, got: %v", err)
	}
}

func TestFindProjectConfigYamlWithFinder_BEADS_DIRMissingConfig(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	got, err := findProjectConfigYamlWithFinder(nil)
	if err == nil {
		t.Fatal("expected error when BEADS_DIR has no config.yaml")
	}
	if got != "" {
		t.Fatalf("findProjectConfigYamlWithFinder() = %q, want empty path on error", got)
	}
	if !strings.Contains(err.Error(), "no config.yaml found in BEADS_DIR") {
		t.Fatalf("expected BEADS_DIR-specific error, got: %v", err)
	}
}

func TestFindProjectConfigYamlWithFinder_UsesFinderResult(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	previousV := v
	previousOverrides := overriddenKeys
	v = nil
	overriddenKeys = map[string]bool{}
	defer func() {
		v = previousV
		overriddenKeys = previousOverrides
	}()

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("json: true\n"), 0o644); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	got, err := findProjectConfigYamlWithFinder(func() string { return beadsDir })
	if err != nil {
		t.Fatalf("findProjectConfigYamlWithFinder() error = %v", err)
	}
	if got != configPath {
		t.Fatalf("findProjectConfigYamlWithFinder() = %q, want %q", got, configPath)
	}
}

func TestFindProjectConfigYamlWithFinder_NoFinderNoConfig(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	previousV := v
	v = nil
	defer func() {
		v = previousV
	}()

	got, err := findProjectConfigYamlWithFinder(nil)
	if err == nil {
		t.Fatal("expected missing config error")
	}
	if got != "" {
		t.Fatalf("findProjectConfigYamlWithFinder() = %q, want empty path on error", got)
	}
	if !strings.Contains(err.Error(), "no .beads/config.yaml found") {
		t.Fatalf("expected generic missing config error, got: %v", err)
	}
}

func TestProjectConfigPathFromLoadedState(t *testing.T) {
	previousV := v
	defer func() {
		v = previousV
	}()

	t.Run("rejects nil loaded state", func(t *testing.T) {
		v = nil
		if got := projectConfigPathFromLoadedState(); got != "" {
			t.Fatalf("projectConfigPathFromLoadedState() = %q, want empty for nil state", got)
		}
	})

	t.Run("rejects non config yaml", func(t *testing.T) {
		v = viper.New()
		v.SetConfigFile(filepath.Join(t.TempDir(), ".beads", "config.local.yaml"))
		if got := projectConfigPathFromLoadedState(); got != "" {
			t.Fatalf("projectConfigPathFromLoadedState() = %q, want empty for config.local.yaml", got)
		}
	})

	t.Run("rejects config outside beads dir", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(configPath, []byte("json: true\n"), 0o644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		v = viper.New()
		v.SetConfigFile(configPath)

		if got := projectConfigPathFromLoadedState(); got != "" {
			t.Fatalf("projectConfigPathFromLoadedState() = %q, want empty for non-.beads config", got)
		}
	})

	t.Run("rejects missing file", func(t *testing.T) {
		v = viper.New()
		v.SetConfigFile(filepath.Join(t.TempDir(), ".beads", "config.yaml"))
		if got := projectConfigPathFromLoadedState(); got != "" {
			t.Fatalf("projectConfigPathFromLoadedState() = %q, want empty for missing config", got)
		}
	})

	t.Run("accepts valid config yaml", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("failed to create beads dir: %v", err)
		}
		configPath := filepath.Join(beadsDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte("json: true\n"), 0o644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		v = viper.New()
		v.SetConfigFile(configPath)

		if got := projectConfigPathFromLoadedState(); got != configPath {
			t.Fatalf("projectConfigPathFromLoadedState() = %q, want %q", got, configPath)
		}
	})
}

func TestFindProjectBeadsDir_NonGitTreeWithoutConfig(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	t.Chdir(t.TempDir())

	if got := findProjectBeadsDir(); got != "" {
		t.Fatalf("findProjectBeadsDir() = %q, want empty", got)
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

func TestValidateYamlConfigValue_DoltDebug(t *testing.T) {
	if err := validateYamlConfigValue("dolt.debug", "true"); err != nil {
		t.Errorf("expected 'true' to be valid: %v", err)
	}
	if err := validateYamlConfigValue("dolt.debug", "false"); err != nil {
		t.Errorf("expected 'false' to be valid: %v", err)
	}
	if err := validateYamlConfigValue("dolt.debug", "TRUE"); err != nil {
		t.Errorf("expected 'TRUE' to be valid (case-insensitive): %v", err)
	}
	if err := validateYamlConfigValue("dolt.debug", "maybe"); err == nil {
		t.Error("expected 'maybe' to be invalid")
	}
	if err := validateYamlConfigValue("dolt.debug", "1"); err == nil {
		t.Error("expected '1' to be invalid (not a boolean string)")
	}
}

func TestValidateYamlConfigValue_DoltMode(t *testing.T) {
	if err := validateYamlConfigValue("dolt.mode", "server"); err != nil {
		t.Errorf("expected 'server' to be valid: %v", err)
	}
	if err := validateYamlConfigValue("dolt.mode", "embedded"); err != nil {
		t.Errorf("expected 'embedded' to be valid: %v", err)
	}
	if err := validateYamlConfigValue("dolt.mode", "SERVER"); err != nil {
		t.Errorf("expected 'SERVER' to be valid (case-insensitive): %v", err)
	}
	if err := validateYamlConfigValue("dolt.mode", "Embedded"); err != nil {
		t.Errorf("expected 'Embedded' to be valid (case-insensitive): %v", err)
	}
	if err := validateYamlConfigValue("dolt.mode", "invalid"); err == nil {
		t.Error("expected 'invalid' to be invalid")
	}
	if err := validateYamlConfigValue("dolt.mode", ""); err == nil {
		t.Error("expected empty string to be invalid")
	}
	if err := validateYamlConfigValue("dolt.mode", "1"); err == nil {
		t.Error("expected '1' to be invalid")
	}
	if err := validateYamlConfigValue("dolt.mode", "local"); err == nil {
		t.Error("expected 'local' to be invalid")
	}
	if err := validateYamlConfigValue("dolt.mode", "remote"); err == nil {
		t.Error("expected 'remote' to be invalid")
	}
}

func TestIsSecretKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"linear.api_key", true},
		{"github.token", true},
		{"some.password", true},
		{"some.secret", true},
		{"some.api-key", true},

		{"no-db", false},
		{"json", false},
		{"routing.mode", false},
		{"sync.remote", false},
		{"linear.team_id", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := IsSecretKey(tt.key)
			if got != tt.expected {
				t.Errorf("IsSecretKey(%q) = %v, want %v", tt.key, got, tt.expected)
			}
		})
	}
}

func TestSecretKeyEnvVarHint(t *testing.T) {
	tests := []struct {
		key      string
		expected string
	}{
		{"linear.api_key", "LINEAR_API_KEY"},
		{"github.token", "GITHUB_TOKEN"},
		{"ai.api_key", "ANTHROPIC_API_KEY"},
		{"custom.secret-token", "BD_CUSTOM_SECRET_TOKEN"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := secretKeyEnvVarHint(tt.key)
			if got != tt.expected {
				t.Errorf("secretKeyEnvVarHint(%q) = %q, want %q", tt.key, got, tt.expected)
			}
		})
	}
}

func TestCheckSecretKeyGitSafety_RefusesGitTrackedSecret(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize a git repo
	gitInit := exec.Command("git", "init")
	gitInit.Dir = tmpDir
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Create .beads/config.yaml and git-add it
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("json: false\n"), 0644); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}
	gitAdd := exec.Command("git", "add", configPath)
	gitAdd.Dir = tmpDir
	if out, err := gitAdd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}

	// checkSecretGitTracked should refuse a secret key
	err := checkSecretGitTracked(configPath, "linear.api_key")
	if err == nil {
		t.Fatal("expected error for secret key on git-tracked config, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to write secret key") {
		t.Fatalf("expected 'refusing to write' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "LINEAR_API_KEY") {
		t.Fatalf("expected env var hint in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force-git-tracked") {
		t.Fatalf("expected --force-git-tracked hint in error, got: %v", err)
	}
}

func TestCheckSecretKeyGitSafety_AllowsDatabaseBackedSecretKey(t *testing.T) {
	tmpDir := t.TempDir()

	gitInit := exec.Command("git", "init")
	gitInit.Dir = tmpDir
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("json: false\n"), 0644); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}
	gitAdd := exec.Command("git", "add", configPath)
	gitAdd.Dir = tmpDir
	if out, err := gitAdd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}

	// Secret-looking keys that are not YAML-backed do not write to config.yaml.
	err := checkSecretGitTracked(configPath, "notion.token")
	if err != nil {
		t.Fatalf("expected no error for database-backed secret key, got: %v", err)
	}
}

func TestCheckSecretKeyGitSafety_AllowsUntrackedConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize a git repo but do NOT add config.yaml
	gitInit := exec.Command("git", "init")
	gitInit.Dir = tmpDir
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("json: false\n"), 0644); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	// checkSecretGitTracked should allow untracked config
	err := checkSecretGitTracked(configPath, "linear.api_key")
	if err != nil {
		t.Fatalf("expected no error for untracked config, got: %v", err)
	}
}

func TestCheckSecretKeyGitSafety_AllowsNonSecretKeyOnTrackedConfig(t *testing.T) {
	tmpDir := t.TempDir()

	gitInit := exec.Command("git", "init")
	gitInit.Dir = tmpDir
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("json: false\n"), 0644); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}
	gitAdd := exec.Command("git", "add", configPath)
	gitAdd.Dir = tmpDir
	if out, err := gitAdd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}

	// Non-secret keys should always be allowed, even on tracked files
	err := checkSecretGitTracked(configPath, "no-db")
	if err != nil {
		t.Fatalf("expected no error for non-secret key, got: %v", err)
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

// TestMetricsConsentResolvesUserGlobalOnly is the regression guard for the
// metrics opt-out authority blocker on PR #4419: a user who runs `bd metrics
// off` (or pins their own endpoint) in the user-global config must not have
// that choice re-enabled or redirected by a repository's project/BEADS_DIR
// config, which has the highest viper precedence in the merged config.
func TestMetricsConsentResolvesUserGlobalOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	const userEndpoint = "https://user-global.example/collect"
	const projectEndpoint = "https://project-override.example/collect"

	// User opted out globally and pinned their own endpoint.
	userCfgDir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir user config dir: %v", err)
	}
	userCfg := "metrics:\n  disabled: true\n  endpoint: " + userEndpoint + "\n"
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte(userCfg), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	// A repository tries to re-enable metrics and redirect the endpoint through
	// the highest-precedence BEADS_DIR config.
	projectBeadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(projectBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir project .beads: %v", err)
	}
	projectCfg := "metrics.disabled: false\nmetrics.endpoint: " + projectEndpoint + "\n"
	if err := os.WriteFile(filepath.Join(projectBeadsDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Setenv("BEADS_DIR", projectBeadsDir)

	ResetForTesting()
	t.Cleanup(ResetForTesting)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Precondition: the project override really is live in the merged config, so
	// the assertions below prove the consent readers bypass it rather than just
	// agreeing with an absent override.
	if GetBool("metrics.disabled") {
		t.Fatalf("precondition: merged metrics.disabled should be false (project override), got true")
	}
	if got := GetString("metrics.endpoint"); got != projectEndpoint {
		t.Fatalf("precondition: merged metrics.endpoint = %q, want project override %q", got, projectEndpoint)
	}

	// Contract: consent + endpoint honor the user-global config only.
	if !MetricsDisabledByUserConfig() {
		t.Errorf("MetricsDisabledByUserConfig() = false; project config must not re-enable a user who opted out")
	}
	if got := UserMetricsEndpoint(); got != userEndpoint {
		t.Errorf("UserMetricsEndpoint() = %q, want %q; project config must not redirect the endpoint", got, userEndpoint)
	}
}

// TestGetUserYamlConfigIgnoresProjectOverride is the regression guard for the
// `bd config get metrics.*` / effective-config-display finding on PR #4419: a
// read of a user-global key must report the user-global value that actually
// governs runtime metrics behavior, not the merged value a repository's
// project/BEADS_DIR config (highest viper precedence) can shadow.
// GetUserYamlConfig backs the config get / config show user-global path.
func TestGetUserYamlConfigIgnoresProjectOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	const userEndpoint = "https://user-global.example/collect"
	const projectEndpoint = "https://project-override.example/collect"

	userCfgDir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir user config dir: %v", err)
	}
	userCfg := "metrics:\n  disabled: true\n  endpoint: " + userEndpoint + "\n"
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte(userCfg), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	// A repository tries to flip metrics and redirect the endpoint through the
	// highest-precedence BEADS_DIR config.
	projectBeadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(projectBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir project .beads: %v", err)
	}
	projectCfg := "metrics.disabled: false\nmetrics.endpoint: " + projectEndpoint + "\n"
	if err := os.WriteFile(filepath.Join(projectBeadsDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Setenv("BEADS_DIR", projectBeadsDir)

	ResetForTesting()
	t.Cleanup(ResetForTesting)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Precondition: the project override is live in the merged config, so the
	// assertions below prove GetUserYamlConfig bypasses it rather than just
	// agreeing with an absent override.
	if GetBool("metrics.disabled") {
		t.Fatalf("precondition: merged metrics.disabled should be false (project override), got true")
	}

	// Contract: a user-global key read reports the user-global value, matching
	// what `bd metrics` actually honors — not the project override.
	if got := GetUserYamlConfig("metrics.disabled"); got != "true" {
		t.Errorf("GetUserYamlConfig(metrics.disabled) = %q, want %q; project config must not shadow the user-global value", got, "true")
	}
	if got := GetUserYamlConfig("metrics.endpoint"); got != userEndpoint {
		t.Errorf("GetUserYamlConfig(metrics.endpoint) = %q, want %q; project config must not shadow the user-global value", got, userEndpoint)
	}
	// An unset user-global key reads as empty, never the project value.
	if got := GetUserYamlConfig("metrics.notice_shown"); got != "" {
		t.Errorf("GetUserYamlConfig(metrics.notice_shown) = %q, want empty (unset in user-global)", got)
	}
}

// TestMetricsNoticeShownResolvesUserGlobalOnly guards the first-run disclosure
// finding on PR #4419: metrics.notice_shown must be resolved from the user-global
// config only, so a repository cannot set it true to suppress the one-time
// disclosure for a user who has never seen it, and a repository setting it false
// cannot force the notice to re-appear once the user has dismissed it globally.
func TestMetricsNoticeShownResolvesUserGlobalOnly(t *testing.T) {
	writeUserNotice := func(t *testing.T, home string, body string) {
		t.Helper()
		userCfgDir := filepath.Join(home, ".config", "bd")
		if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
			t.Fatalf("mkdir user config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte(body), 0o600); err != nil {
			t.Fatalf("write user config: %v", err)
		}
	}
	writeProjectNotice := func(t *testing.T, value string) {
		t.Helper()
		projectBeadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(projectBeadsDir, 0o755); err != nil {
			t.Fatalf("mkdir project .beads: %v", err)
		}
		if err := os.WriteFile(filepath.Join(projectBeadsDir, "config.yaml"),
			[]byte("metrics.notice_shown: "+value+"\n"), 0o644); err != nil {
			t.Fatalf("write project config: %v", err)
		}
		t.Setenv("BEADS_DIR", projectBeadsDir)
	}

	t.Run("project true cannot suppress an unseen notice", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		// User-global has no notice marker; the repository tries to claim it was
		// already shown via the highest-precedence project config.
		writeProjectNotice(t, "true")

		ResetForTesting()
		t.Cleanup(ResetForTesting)
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if !GetBool("metrics.notice_shown") {
			t.Fatalf("precondition: merged metrics.notice_shown should be true (project override)")
		}
		if MetricsNoticeShownByUserConfig() {
			t.Errorf("MetricsNoticeShownByUserConfig() = true; a project must not suppress an unseen disclosure")
		}
	})

	t.Run("user-global true is authoritative over project false", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		writeUserNotice(t, home, "metrics:\n  notice_shown: true\n")
		writeProjectNotice(t, "false")

		ResetForTesting()
		t.Cleanup(ResetForTesting)
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if GetBool("metrics.notice_shown") {
			t.Fatalf("precondition: merged metrics.notice_shown should be false (project override)")
		}
		if !MetricsNoticeShownByUserConfig() {
			t.Errorf("MetricsNoticeShownByUserConfig() = false; user-global true must win over project false")
		}
	})
}
