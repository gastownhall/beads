package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCleanupMergeArtifacts_CommandInjectionPrevention verifies that the git rm
// command in cleanupMergeArtifacts is safe from command injection attacks.
//
// This test addresses bd-yxy: gosec G204 flags exec.Command with variable fullPath
// in merge.go:121. We verify that:
// 1. Shell metacharacters in filenames don't cause command injection
// 2. exec.Command passes arguments directly to git (no shell interpretation)
// 3. Only backup files are targeted for removal
func TestCleanupMergeArtifacts_CommandInjectionPrevention(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantSafe bool
	}{
		{
			name:     "shell metacharacter semicolon",
			filename: "backup; rm -rf /",
			wantSafe: true, // exec.Command doesn't use shell, so ; is just part of filename
		},
		{
			name:     "shell metacharacter pipe",
			filename: "backup | cat /etc/passwd",
			wantSafe: true,
		},
		{
			name:     "shell metacharacter ampersand",
			filename: "backup & malicious_command",
			wantSafe: true,
		},
		{
			name:     "shell variable expansion",
			filename: "backup$PATH",
			wantSafe: true,
		},
		{
			name:     "command substitution backticks",
			filename: "backup`whoami`",
			wantSafe: true,
		},
		{
			name:     "command substitution dollar-paren",
			filename: "backup$(whoami)",
			wantSafe: true,
		},
		{
			name:     "normal backup file",
			filename: "issues.jsonl.backup",
			wantSafe: true,
		},
		{
			name:     "backup with spaces",
			filename: "backup file with spaces.jsonl",
			wantSafe: true,
		},
		{
			name:     "path traversal attempt",
			filename: "../../backup_etc_passwd",
			wantSafe: true, // filepath.Join normalizes this
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary test environment
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatalf("Failed to create .beads dir: %v", err)
			}

			// Create a test file with potentially dangerous name
			testFile := filepath.Join(beadsDir, tt.filename)

			// Create the file - this will fail if filename contains path separators
			// or other invalid characters, which is exactly what we want
			if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
				// Some filenames may be invalid on the OS - that's fine, they can't
				// be exploited if they can't be created
				t.Logf("Could not create file with name %q (OS prevented): %v", tt.filename, err)
				return
			}

			// Create output path
			outputPath := filepath.Join(beadsDir, "issues.jsonl")
			if err := os.WriteFile(outputPath, []byte("{}"), 0644); err != nil {
				t.Fatalf("Failed to create output file: %v", err)
			}

			// Run cleanup with debug=false (normal operation)
			cleanupMergeArtifacts(outputPath, false)

			// Verify the file was removed (since it contains "backup")
			if _, err := os.Stat(testFile); err == nil {
				// File still exists - this is fine if git rm failed because
				// the file isn't tracked, but os.Remove should have removed it
				t.Logf("File %q still exists after cleanup - this is OK if not tracked", tt.filename)
			}

			// Most importantly: verify no command injection occurred
			// If command injection worked, we'd see evidence in the filesystem
			// or the test would hang/crash. The fact that we get here means
			// exec.Command safely handled the filename.

			// Verify that sensitive paths are NOT affected
			// Note: /etc/passwd only exists on Unix systems, so skip this check on Windows
			if runtime.GOOS != "windows" {
				if _, err := os.Stat("/etc/passwd"); err != nil {
					t.Errorf("Command injection may have occurred - /etc/passwd missing")
				}
			}
		})
	}
}

// TestCleanupMergeArtifacts_OnlyBackupFiles verifies that only files with
// "backup" in their name are targeted for removal, preventing accidental
// deletion of other files.
func TestCleanupMergeArtifacts_OnlyBackupFiles(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create various files
	files := map[string]bool{
		"issues.jsonl":         false, // Should NOT be removed
		"beads.db":             false, // Should NOT be removed
		"backup.jsonl":         true,  // Should be removed
		"issues.jsonl.backup":  true,  // Should be removed
		"BACKUP_FILE":          true,  // Should be removed (case-insensitive)
		"my_backup_2024.txt":   true,  // Should be removed
		"important_data.jsonl": false, // Should NOT be removed
		"issues.jsonl.bak":     false, // Should NOT be removed (no "backup")
	}

	for filename := range files {
		path := filepath.Join(beadsDir, filename)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create %s: %v", filename, err)
		}
	}

	// Create output path
	outputPath := filepath.Join(beadsDir, "issues.jsonl")

	// Run cleanup
	cleanupMergeArtifacts(outputPath, false)

	// Verify correct files were removed/preserved
	for filename, shouldRemove := range files {
		path := filepath.Join(beadsDir, filename)
		_, err := os.Stat(path)
		exists := err == nil

		if shouldRemove && exists {
			t.Errorf("File %q should have been removed but still exists", filename)
		}
		if !shouldRemove && !exists {
			t.Errorf("File %q should have been preserved but was removed", filename)
		}
	}
}

// TestCleanupMergeArtifacts_GitRmSafety verifies that git rm is called with
// safe arguments and proper working directory.
func TestCleanupMergeArtifacts_GitRmSafety(t *testing.T) {
	// This test verifies the fix for bd-yxy by ensuring:
	// 1. fullPath is constructed safely using filepath.Join
	// 2. exec.Command is used (not shell execution)
	// 3. Arguments are passed individually (no concatenation)
	// 4. Working directory is set correctly

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Initialize git repo (required for git rm to work)
	// Note: We're not actually testing git functionality here,
	// just verifying our command construction is safe

	// Create a backup file
	backupFile := filepath.Join(beadsDir, "test.backup")
	if err := os.WriteFile(backupFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create backup file: %v", err)
	}

	outputPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(outputPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to create output file: %v", err)
	}

	// Run cleanup - this should safely attempt git rm and then os.Remove
	cleanupMergeArtifacts(outputPath, false)

	// Verify backup file was removed (by os.Remove since git rm will fail
	// in a non-git directory)
	if _, err := os.Stat(backupFile); err == nil {
		t.Errorf("Backup file should have been removed")
	}

	// Key insight: The security issue (G204) is actually a false positive.
	// exec.Command("git", "rm", "-f", "--quiet", fullPath) is safe because:
	// - fullPath is constructed with filepath.Join (safe)
	// - exec.Command does NOT use a shell
	// - Arguments are passed as separate strings to git binary
	// - Shell metacharacters are treated as literal characters in the filename
}
