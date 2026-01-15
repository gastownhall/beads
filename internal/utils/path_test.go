package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestNormalizePathForComparison(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		result := NormalizePathForComparison("")
		if result != "" {
			t.Errorf("expected empty string for empty input, got %q", result)
		}
	})

	t.Run("absolute path", func(t *testing.T) {
		tmpDir := t.TempDir()
		result := NormalizePathForComparison(tmpDir)
		if !filepath.IsAbs(result) {
			t.Errorf("expected absolute path, got %q", result)
		}
	})

	t.Run("relative path becomes absolute", func(t *testing.T) {
		result := NormalizePathForComparison(".")
		if !filepath.IsAbs(result) {
			t.Errorf("expected absolute path, got %q", result)
		}
	})

	t.Run("symlink resolution", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a subdirectory
		subDir := filepath.Join(tmpDir, "subdir")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a symlink to the subdirectory
		linkPath := filepath.Join(tmpDir, "link")
		if err := os.Symlink(subDir, linkPath); err != nil {
			t.Skipf("symlink creation failed: %v", err)
		}

		normalizedLink := NormalizePathForComparison(linkPath)
		normalizedSubdir := NormalizePathForComparison(subDir)

		if normalizedLink != normalizedSubdir {
			t.Errorf("symlink and target should normalize to same path: %q vs %q", normalizedLink, normalizedSubdir)
		}
	})

	t.Run("case normalization on case-insensitive systems", func(t *testing.T) {
		if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
			t.Skip("case normalization only applies to darwin/windows")
		}

		// On macOS/Windows, different case should normalize to same
		tmpDir := t.TempDir()
		lowerCase := strings.ToLower(tmpDir)
		upperCase := strings.ToUpper(tmpDir)

		normalizedLower := NormalizePathForComparison(lowerCase)
		normalizedUpper := NormalizePathForComparison(upperCase)

		if normalizedLower != normalizedUpper {
			t.Errorf("case-insensitive paths should normalize to same value: %q vs %q", normalizedLower, normalizedUpper)
		}
	})
}

func TestCanonicalizePathCase(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case canonicalization test only runs on macOS")
	}

	// Create a directory with mixed case
	tmpDir := t.TempDir()
	mixedCaseDir := filepath.Join(tmpDir, "TestCase")
	if err := os.MkdirAll(mixedCaseDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Access via wrong case (lowercase)
	wrongCasePath := filepath.Join(tmpDir, "testcase")

	// Verify the wrong case path exists (macOS case-insensitive)
	if _, err := os.Stat(wrongCasePath); err != nil {
		t.Fatalf("wrong case path should exist on macOS: %v", err)
	}

	// CanonicalizePath should return the correct case
	result := CanonicalizePath(wrongCasePath)

	// The result should have the correct case "TestCase", not "testcase"
	if !strings.HasSuffix(result, "TestCase") {
		t.Errorf("CanonicalizePath(%q) = %q, want path ending in 'TestCase'", wrongCasePath, result)
	}
}

func TestCanonicalizeIfRelative(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		validate func(t *testing.T, result string)
	}{
		{
			name:  "empty string returns empty",
			input: "",
			validate: func(t *testing.T, result string) {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
			},
		},
		{
			name:  "absolute path returns unchanged",
			input: "/tmp/test/path",
			validate: func(t *testing.T, result string) {
				if result != "/tmp/test/path" {
					t.Errorf("expected /tmp/test/path, got %q", result)
				}
			},
		},
		{
			name:  "relative path returns canonicalized absolute",
			input: ".",
			validate: func(t *testing.T, result string) {
				if !filepath.IsAbs(result) {
					t.Errorf("expected absolute path, got %q", result)
				}
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatalf("failed to get cwd: %v", err)
				}
				// Result should be related to cwd
				canonicalCwd := CanonicalizePath(cwd)
				if result != canonicalCwd {
					t.Errorf("expected %q, got %q", canonicalCwd, result)
				}
			},
		},
		{
			name:  "relative subdirectory path",
			input: "subdir/file.txt",
			validate: func(t *testing.T, result string) {
				if !filepath.IsAbs(result) {
					t.Errorf("expected absolute path, got %q", result)
				}
				if !strings.HasSuffix(result, "subdir/file.txt") && !strings.HasSuffix(result, "subdir\\file.txt") {
					t.Errorf("expected path to end with subdir/file.txt, got %q", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CanonicalizeIfRelative(tt.input)
			tt.validate(t, result)
		})
	}
}

func TestCanonicalizeIfRelativeTilde(t *testing.T) {
	// Test tilde expansion - CanonicalizeIfRelative does NOT expand tilde
	// because ~ is not detected as relative by filepath.IsAbs()
	// This documents the current behavior - tilde paths need separate handling
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("could not get home directory")
	}

	// ~/path is technically not absolute, but filepath.IsAbs treats it as relative
	tildeInput := "~/testpath"
	result := CanonicalizeIfRelative(tildeInput)

	// The function should canonicalize it since ~ is not absolute
	// But CanonicalizePath doesn't expand tilde, so we get cwd + ~/testpath
	// This documents the expected behavior - tilde expansion must happen before
	// calling CanonicalizeIfRelative
	if result == tildeInput {
		t.Logf("tilde path returned unchanged - tilde expansion happens elsewhere")
	} else if strings.Contains(result, home) {
		t.Logf("tilde was expanded to home directory")
	}
	// Either behavior is acceptable - this test documents it

	// Verify absolute tilde-expanded paths work correctly
	absoluteWithHome := filepath.Join(home, "testpath")
	result = CanonicalizeIfRelative(absoluteWithHome)
	if result != absoluteWithHome {
		// May differ on macOS due to /var -> /private/var symlink resolution
		// Just verify it's still absolute and contains the expected path
		if !filepath.IsAbs(result) {
			t.Errorf("expected absolute path, got %q", result)
		}
	}
}

func TestPathsEqual(t *testing.T) {
	t.Run("identical paths", func(t *testing.T) {
		tmpDir := t.TempDir()
		if !PathsEqual(tmpDir, tmpDir) {
			t.Error("identical paths should be equal")
		}
	})

	t.Run("empty paths", func(t *testing.T) {
		if !PathsEqual("", "") {
			t.Error("two empty paths should be equal")
		}
	})

	t.Run("one empty path", func(t *testing.T) {
		if PathsEqual("/tmp/foo", "") {
			t.Error("non-empty and empty paths should not be equal")
		}
	})

	t.Run("different paths", func(t *testing.T) {
		if PathsEqual("/tmp/foo", "/tmp/bar") {
			t.Error("different paths should not be equal")
		}
	})

	t.Run("symlink equality", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a subdirectory
		subDir := filepath.Join(tmpDir, "subdir")
		if err := os.MkdirAll(subDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a symlink to the subdirectory
		linkPath := filepath.Join(tmpDir, "link")
		if err := os.Symlink(subDir, linkPath); err != nil {
			t.Skipf("symlink creation failed: %v", err)
		}

		if !PathsEqual(linkPath, subDir) {
			t.Error("symlink and target should be equal")
		}
	})

	t.Run("case-insensitive equality on macOS/Windows (GH#869)", func(t *testing.T) {
		if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
			t.Skip("case normalization only applies to darwin/windows")
		}

		// This is the actual bug from GH#869: Desktop vs desktop
		tmpDir := t.TempDir()

		// Create a subdirectory with mixed case
		mixedCase := filepath.Join(tmpDir, "Desktop")
		if err := os.MkdirAll(mixedCase, 0755); err != nil {
			t.Fatal(err)
		}

		// The lowercase version should still refer to the same directory
		lowerCase := filepath.Join(tmpDir, "desktop")

		if !PathsEqual(mixedCase, lowerCase) {
			t.Errorf("paths with different case should be equal on case-insensitive FS: %q vs %q", mixedCase, lowerCase)
		}
	})
}
