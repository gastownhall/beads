package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsValidBranchName(t *testing.T) {
	tests := []struct {
		name     string
		branch   string
		expected bool
	}{
		{"valid simple", "main", true},
		{"valid with slash", "feature/test", true},
		{"valid with dash", "my-branch", true},
		{"valid with underscore", "my_branch", true},
		{"valid with dot", "v1.0", true},
		{"valid complex", "feature/bd-123-add-thing", true},

		{"empty", "", false},
		{"starts with dash", "-branch", false},
		{"ends with dot", "branch.", false},
		{"ends with slash", "branch/", false},
		{"contains space", "my branch", false},
		{"contains tilde", "branch~1", false},
		{"contains caret", "branch^2", false},
		{"contains colon", "branch:name", false},
		{"contains backslash", "branch\\name", false},
		{"contains question", "branch?", false},
		{"contains asterisk", "branch*", false},
		{"contains bracket", "branch[0]", false},
		{"contains double dot", "branch..name", false},
		{"ends with .lock", "branch.lock", false},
		{"contains @{", "branch@{1}", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidBranchName(tt.branch)
			if got != tt.expected {
				t.Errorf("isValidBranchName(%q) = %v, want %v", tt.branch, got, tt.expected)
			}
		})
	}
}

func TestCheckConfigValues(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Test with valid config
	t.Run("valid config", func(t *testing.T) {
		configContent := `issue-prefix: "test"
flush-debounce: "30s"
sync-branch: "beads-sync"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "ok" {
			t.Errorf("expected ok status, got %s: %s", check.Status, check.Detail)
		}
	})

	// Test with invalid flush-debounce
	t.Run("invalid flush-debounce", func(t *testing.T) {
		configContent := `issue-prefix: "test"
flush-debounce: "not-a-duration"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "warning" {
			t.Errorf("expected warning status, got %s", check.Status)
		}
		if check.Detail == "" || !contains(check.Detail, "flush-debounce") {
			t.Errorf("expected detail to mention flush-debounce, got: %s", check.Detail)
		}
	})

	// Test with invalid issue-prefix
	t.Run("invalid issue-prefix", func(t *testing.T) {
		configContent := `issue-prefix: "123-invalid"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "warning" {
			t.Errorf("expected warning status, got %s", check.Status)
		}
		if check.Detail == "" || !contains(check.Detail, "issue-prefix") {
			t.Errorf("expected detail to mention issue-prefix, got: %s", check.Detail)
		}
	})

	// Test with invalid routing.mode
	t.Run("invalid routing.mode", func(t *testing.T) {
		configContent := `routing:
  mode: "invalid-mode"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "warning" {
			t.Errorf("expected warning status, got %s", check.Status)
		}
		if check.Detail == "" || !contains(check.Detail, "routing.mode") {
			t.Errorf("expected detail to mention routing.mode, got: %s", check.Detail)
		}
	})

	// Test with invalid sync-branch
	t.Run("invalid sync-branch", func(t *testing.T) {
		configContent := `sync-branch: "branch with spaces"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "warning" {
			t.Errorf("expected warning status, got %s", check.Status)
		}
		if check.Detail == "" || !contains(check.Detail, "sync-branch") {
			t.Errorf("expected detail to mention sync-branch, got: %s", check.Detail)
		}
	})

	// Test with too long issue-prefix
	t.Run("too long issue-prefix", func(t *testing.T) {
		configContent := `issue-prefix: "thisprefiswaytooolongtobevalid"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "warning" {
			t.Errorf("expected warning status, got %s", check.Status)
		}
		if check.Detail == "" || !contains(check.Detail, "too long") {
			t.Errorf("expected detail to mention too long, got: %s", check.Detail)
		}
	})
}

func TestCheckMetadataConfigValues(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Test with valid metadata
	t.Run("valid metadata", func(t *testing.T) {
		metadataContent := `{
  "database": "beads.db",
  "jsonl_export": "issues.jsonl"
}`
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadataContent), 0644); err != nil {
			t.Fatalf("failed to write metadata.json: %v", err)
		}

		issues := checkMetadataConfigValues(tmpDir)
		if len(issues) > 0 {
			t.Errorf("expected no issues, got: %v", issues)
		}
	})

	// Test with path in database field
	t.Run("path in database field", func(t *testing.T) {
		metadataContent := `{
  "database": "/path/to/beads.db",
  "jsonl_export": "issues.jsonl"
}`
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadataContent), 0644); err != nil {
			t.Fatalf("failed to write metadata.json: %v", err)
		}

		issues := checkMetadataConfigValues(tmpDir)
		if len(issues) == 0 {
			t.Error("expected issues for path in database field")
		}
	})

	// Test with wrong extension for jsonl
	t.Run("wrong jsonl extension", func(t *testing.T) {
		metadataContent := `{
  "database": "beads.db",
  "jsonl_export": "issues.json"
}`
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadataContent), 0644); err != nil {
			t.Fatalf("failed to write metadata.json: %v", err)
		}

		issues := checkMetadataConfigValues(tmpDir)
		if len(issues) == 0 {
			t.Error("expected issues for wrong jsonl extension")
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
