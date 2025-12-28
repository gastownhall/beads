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

	t.Run("jsonl_export cannot be system file", func(t *testing.T) {
		metadataContent := `{
  "database": "beads.db",
  "jsonl_export": "interactions.jsonl"
}`
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadataContent), 0644); err != nil {
			t.Fatalf("failed to write metadata.json: %v", err)
		}

		issues := checkMetadataConfigValues(tmpDir)
		if len(issues) == 0 {
			t.Error("expected issues for system jsonl_export")
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

func TestIsValidBoolString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"true", "true", true},
		{"false", "false", true},
		{"True uppercase", "True", true},
		{"FALSE uppercase", "FALSE", true},
		{"yes", "yes", true},
		{"no", "no", true},
		{"1", "1", true},
		{"0", "0", true},
		{"on", "on", true},
		{"off", "off", true},
		{"t", "t", true},
		{"f", "f", true},
		{"y", "y", true},
		{"n", "n", true},

		{"invalid string", "invalid", false},
		{"maybe", "maybe", false},
		{"2", "2", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidBoolString(tt.input)
			if got != tt.expected {
				t.Errorf("isValidBoolString(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExpandPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skip("Cannot get home directory")
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"tilde only", "~", homeDir},
		{"tilde path", "~/foo/bar", filepath.Join(homeDir, "foo/bar")},
		{"no tilde", "/absolute/path", "/absolute/path"},
		{"relative", "relative/path", "relative/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandPath(tt.input)
			if got != tt.expected {
				t.Errorf("expandPath(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestValidActorRegex(t *testing.T) {
	tests := []struct {
		name     string
		actor    string
		expected bool
	}{
		{"simple name", "alice", true},
		{"with numbers", "user123", true},
		{"with dash", "alice-bob", true},
		{"with underscore", "alice_bob", true},
		{"with dot", "alice.bob", true},
		{"email", "alice@example.com", true},
		{"starts with number", "123user", true},

		{"empty", "", false},
		{"starts with dash", "-user", false},
		{"starts with dot", ".user", false},
		{"starts with at", "@user", false},
		{"contains space", "alice bob", false},
		{"contains special", "alice$bob", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validActorRegex.MatchString(tt.actor)
			if got != tt.expected {
				t.Errorf("validActorRegex.MatchString(%q) = %v, want %v", tt.actor, got, tt.expected)
			}
		})
	}
}

func TestValidCustomStatusRegex(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		expected bool
	}{
		{"simple", "awaiting_review", true},
		{"with numbers", "stage1", true},
		{"lowercase only", "testing", true},
		{"underscore prefix", "a_test", true},

		{"uppercase", "Awaiting_Review", false},
		{"starts with number", "1stage", false},
		{"starts with underscore", "_test", false},
		{"contains dash", "awaiting-review", false},
		{"empty", "", false},
		{"space", "awaiting review", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validCustomStatusRegex.MatchString(tt.status)
			if got != tt.expected {
				t.Errorf("validCustomStatusRegex.MatchString(%q) = %v, want %v", tt.status, got, tt.expected)
			}
		})
	}
}

func TestCheckConfigValuesActor(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	t.Run("invalid actor", func(t *testing.T) {
		configContent := `actor: "@invalid-actor"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "warning" {
			t.Errorf("expected warning status, got %s", check.Status)
		}
		if check.Detail == "" || !contains(check.Detail, "actor") {
			t.Errorf("expected detail to mention actor, got: %s", check.Detail)
		}
	})

	t.Run("valid actor", func(t *testing.T) {
		configContent := `actor: "alice@example.com"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "ok" {
			t.Errorf("expected ok status, got %s: %s", check.Status, check.Detail)
		}
	})
}

func TestCheckConfigValuesDbPath(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	t.Run("unusual db extension", func(t *testing.T) {
		configContent := `db: "/path/to/database.txt"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "warning" {
			t.Errorf("expected warning status, got %s", check.Status)
		}
		if check.Detail == "" || !contains(check.Detail, "db") {
			t.Errorf("expected detail to mention db, got: %s", check.Detail)
		}
	})

	t.Run("valid db path", func(t *testing.T) {
		configContent := `db: "/path/to/database.db"
`
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config.yaml: %v", err)
		}

		check := CheckConfigValues(tmpDir)
		if check.Status != "ok" {
			t.Errorf("expected ok status, got %s: %s", check.Status, check.Detail)
		}
	})
}
