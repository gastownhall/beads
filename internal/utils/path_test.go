package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalizePath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(t *testing.T, result string)
	}{
		{
			name:  "absolute path",
			input: "/tmp/test",
			validate: func(t *testing.T, result string) {
				if !filepath.IsAbs(result) {
					t.Errorf("expected absolute path, got %q", result)
				}
			},
		},
		{
			name:  "relative path",
			input: ".",
			validate: func(t *testing.T, result string) {
				if !filepath.IsAbs(result) {
					t.Errorf("expected absolute path, got %q", result)
				}
			},
		},
		{
			name:  "current directory",
			input: ".",
			validate: func(t *testing.T, result string) {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatalf("failed to get cwd: %v", err)
				}
				// Result should be canonical form of current directory
				if !filepath.IsAbs(result) {
					t.Errorf("expected absolute path, got %q", result)
				}
				// The result should be related to cwd (could be same or canonical version)
				if result != cwd {
					// Try to canonicalize cwd to compare
					canonicalCwd, err := filepath.EvalSymlinks(cwd)
					if err == nil && result != canonicalCwd {
						t.Errorf("expected %q or %q, got %q", cwd, canonicalCwd, result)
					}
				}
			},
		},
		{
			name:  "empty path",
			input: "",
			validate: func(t *testing.T, result string) {
				// Empty path should be handled (likely becomes "." then current dir)
				if result == "" {
					t.Error("expected non-empty result for empty input")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CanonicalizePath(tt.input)
			tt.validate(t, result)
		})
	}
}

func TestCanonicalizePathSymlink(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create a symlink to the temp directory
	symlinkPath := filepath.Join(tmpDir, "link")
	if err := os.Symlink(tmpDir, symlinkPath); err != nil {
		t.Skipf("failed to create symlink (may not be supported): %v", err)
	}

	// Canonicalize the symlink path
	result := CanonicalizePath(symlinkPath)

	// The result should be the resolved path (tmpDir), not the symlink
	if result != tmpDir {
		// Try to get canonical form of tmpDir for comparison
		canonicalTmpDir, err := filepath.EvalSymlinks(tmpDir)
		if err != nil {
			t.Fatalf("failed to canonicalize tmpDir: %v", err)
		}
		if result != canonicalTmpDir {
			t.Errorf("expected %q or %q, got %q", tmpDir, canonicalTmpDir, result)
		}
	}
}
