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

// TestFindJSONLInDir tests that FindJSONLInDir correctly prefers issues.jsonl
// and avoids deletions.jsonl and merge artifacts (bd-tqo fix)
func TestFindJSONLInDir(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		expected string
	}{
		{
			name:     "only issues.jsonl",
			files:    []string{"issues.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "issues.jsonl and deletions.jsonl - prefers issues",
			files:    []string{"deletions.jsonl", "issues.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "issues.jsonl with merge artifacts - prefers issues",
			files:    []string{"beads.base.jsonl", "beads.left.jsonl", "beads.right.jsonl", "issues.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "beads.jsonl as legacy fallback",
			files:    []string{"beads.jsonl"},
			expected: "beads.jsonl",
		},
		{
			name:     "issues.jsonl preferred over beads.jsonl",
			files:    []string{"beads.jsonl", "issues.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "only deletions.jsonl - returns default issues.jsonl",
			files:    []string{"deletions.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "only interactions.jsonl - returns default issues.jsonl",
			files:    []string{"interactions.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "interactions.jsonl with issues.jsonl - prefers issues",
			files:    []string{"interactions.jsonl", "issues.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "only merge artifacts - returns default issues.jsonl",
			files:    []string{"beads.base.jsonl", "beads.left.jsonl", "beads.right.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "no files - returns default issues.jsonl",
			files:    []string{},
			expected: "issues.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "bd-findjsonl-test-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)

			// Create test files
			for _, file := range tt.files {
				path := filepath.Join(tmpDir, file)
				if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			result := FindJSONLInDir(tmpDir)
			got := filepath.Base(result)

			if got != tt.expected {
				t.Errorf("FindJSONLInDir() = %q, want %q", got, tt.expected)
			}
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

func TestResolveForWrite(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		tmpDir := t.TempDir()
		file := filepath.Join(tmpDir, "regular.txt")
		if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}

		got, err := ResolveForWrite(file)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != file {
			t.Errorf("got %q, want %q", got, file)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		tmpDir := t.TempDir()
		target := filepath.Join(tmpDir, "target.txt")
		if err := os.WriteFile(target, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(tmpDir, "link.txt")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		got, err := ResolveForWrite(link)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Resolve target too - on macOS, /var is symlink to /private/var
		wantTarget, _ := filepath.EvalSymlinks(target)
		if got != wantTarget {
			t.Errorf("got %q, want %q", got, wantTarget)
		}
	})

	t.Run("non-existent", func(t *testing.T) {
		tmpDir := t.TempDir()
		newFile := filepath.Join(tmpDir, "new.txt")

		got, err := ResolveForWrite(newFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != newFile {
			t.Errorf("got %q, want %q", got, newFile)
		}
	})
}

func TestFindMoleculesJSONLInDir(t *testing.T) {
	root := t.TempDir()
	molecules := filepath.Join(root, "molecules.jsonl")
	if err := os.WriteFile(molecules, []byte("[]"), 0o644); err != nil {
		t.Fatalf("failed to create molecules.jsonl: %v", err)
	}

	if got := FindMoleculesJSONLInDir(root); got != molecules {
		t.Fatalf("expected %q, got %q", molecules, got)
	}

	otherDir := t.TempDir()
	if got := FindMoleculesJSONLInDir(otherDir); got != "" {
		t.Fatalf("expected empty path when file missing, got %q", got)
	}
}
