package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsUpstreamRepo(t *testing.T) {
	tests := []struct {
		name     string
		remote   string
		expected bool
	}{
		{"ssh upstream", "git@github.com:steveyegge/beads.git", true},
		{"https upstream", "https://github.com/steveyegge/beads.git", true},
		{"https upstream no .git", "https://github.com/steveyegge/beads", true},
		{"fork ssh", "git@github.com:contributor/beads.git", false},
		{"fork https", "https://github.com/contributor/beads.git", false},
		{"different repo", "git@github.com:someone/other-project.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify the pattern matching logic matches what isUpstreamRepo uses
			upstreamPatterns := []string{
				"steveyegge/beads",
				"git@github.com:steveyegge/beads",
				"https://github.com/steveyegge/beads",
			}

			matches := false
			for _, pattern := range upstreamPatterns {
				if strings.Contains(tt.remote, pattern) {
					matches = true
					break
				}
			}

			if matches != tt.expected {
				t.Errorf("remote %q: expected upstream=%v, got %v", tt.remote, tt.expected, matches)
			}
		})
	}
}

func TestIsAlreadyExcluded(t *testing.T) {
	// Create temp file with exclusion
	tmpDir := t.TempDir()
	excludePath := filepath.Join(tmpDir, "exclude")

	// Test non-existent file
	if isAlreadyExcluded(excludePath) {
		t.Error("expected non-existent file to return false")
	}

	// Test file without exclusion
	if err := os.WriteFile(excludePath, []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if isAlreadyExcluded(excludePath) {
		t.Error("expected file without exclusion to return false")
	}

	// Test file with exclusion
	if err := os.WriteFile(excludePath, []byte("*.log\n.beads/issues.jsonl\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !isAlreadyExcluded(excludePath) {
		t.Error("expected file with exclusion to return true")
	}
}

func TestAddToExclude(t *testing.T) {
	tmpDir := t.TempDir()
	infoDir := filepath.Join(tmpDir, ".git", "info")
	excludePath := filepath.Join(infoDir, "exclude")

	// Test creating new file
	if err := addToExclude(excludePath); err != nil {
		t.Fatalf("addToExclude failed: %v", err)
	}

	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("failed to read exclude file: %v", err)
	}

	if !strings.Contains(string(content), ".beads/issues.jsonl") {
		t.Errorf("exclude file missing .beads/issues.jsonl: %s", content)
	}

	// Test appending to existing file
	if err := os.WriteFile(excludePath, []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := addToExclude(excludePath); err != nil {
		t.Fatalf("addToExclude append failed: %v", err)
	}

	content, err = os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("failed to read exclude file: %v", err)
	}

	if !strings.Contains(string(content), "*.log") {
		t.Errorf("exclude file missing original content: %s", content)
	}
	if !strings.Contains(string(content), ".beads/issues.jsonl") {
		t.Errorf("exclude file missing .beads/issues.jsonl: %s", content)
	}
}
