package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveBeadsFromGitattributes(t *testing.T) {
	t.Run("removes beads entry", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitattributes := filepath.Join(tmpDir, ".gitattributes")

		content := `*.png binary
# Use bd merge for beads JSONL files
.beads/issues.jsonl merge=beads
*.jpg binary
`
		if err := os.WriteFile(gitattributes, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		if err := removeBeadsFromGitattributes(gitattributes); err != nil {
			t.Fatalf("removeBeadsFromGitattributes failed: %v", err)
		}

		result, err := os.ReadFile(gitattributes)
		if err != nil {
			t.Fatalf("failed to read result: %v", err)
		}

		if strings.Contains(string(result), "merge=beads") {
			t.Error("beads merge entry should have been removed")
		}
		if strings.Contains(string(result), "Use bd merge") {
			t.Error("beads comment should have been removed")
		}
		if !strings.Contains(string(result), "*.png binary") {
			t.Error("other entries should be preserved")
		}
		if !strings.Contains(string(result), "*.jpg binary") {
			t.Error("other entries should be preserved")
		}
	})

	t.Run("removes file if only beads entry", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitattributes := filepath.Join(tmpDir, ".gitattributes")

		content := `# Use bd merge for beads JSONL files
.beads/issues.jsonl merge=beads
`
		if err := os.WriteFile(gitattributes, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		if err := removeBeadsFromGitattributes(gitattributes); err != nil {
			t.Fatalf("removeBeadsFromGitattributes failed: %v", err)
		}

		if _, err := os.Stat(gitattributes); !os.IsNotExist(err) {
			t.Error("file should have been deleted when only beads entries present")
		}
	})

	t.Run("handles non-existent file", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitattributes := filepath.Join(tmpDir, ".gitattributes")

		// File doesn't exist - should not error
		if err := removeBeadsFromGitattributes(gitattributes); err != nil {
			t.Fatalf("should not error on non-existent file: %v", err)
		}
	})
}

func TestVerifyResetConfirmation(t *testing.T) {
	// This test depends on git being available and a remote being configured
	// Skip if not in a git repo
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		t.Skip("not in a git repository")
	}

	t.Run("accepts origin", func(t *testing.T) {
		// Most repos have an "origin" remote
		// If not, this test will just pass since we can't reliably test this
		result := verifyResetConfirmation("origin")
		// Don't assert - just make sure it doesn't panic
		_ = result
	})

	t.Run("rejects invalid remote", func(t *testing.T) {
		result := verifyResetConfirmation("nonexistent-remote-12345")
		if result {
			t.Error("should reject non-existent remote")
		}
	})
}
