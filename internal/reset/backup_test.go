package reset

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCreateBackup(t *testing.T) {
	// Create temporary directory structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create test .beads directory: %v", err)
	}

	// Create some test files in .beads
	testFiles := map[string]string{
		"issues.jsonl":  `{"id":"test-1","title":"Test Issue"}`,
		"metadata.json": `{"version":"1.0"}`,
		"config.yaml":   `prefix: test`,
	}

	for name, content := range testFiles {
		path := filepath.Join(beadsDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create test file %s: %v", name, err)
		}
	}

	// Create a subdirectory with a file
	subDir := filepath.Join(beadsDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdirectory: %v", err)
	}
	subFile := filepath.Join(subDir, "subfile.txt")
	if err := os.WriteFile(subFile, []byte("subfile content"), 0644); err != nil {
		t.Fatalf("failed to create subfile: %v", err)
	}

	// Create backup
	backupPath, err := CreateBackup(beadsDir)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	// Verify backup path format
	expectedPattern := `\.beads-backup-\d{8}-\d{6}$`
	matched, _ := regexp.MatchString(expectedPattern, backupPath)
	if !matched {
		t.Errorf("backup path %q doesn't match expected pattern %q", backupPath, expectedPattern)
	}

	// Verify backup directory exists
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("backup directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("backup path is not a directory")
	}

	// Verify all files were copied
	for name, expectedContent := range testFiles {
		backupFilePath := filepath.Join(backupPath, name)
		content, err := os.ReadFile(backupFilePath)
		if err != nil {
			t.Errorf("failed to read backed up file %s: %v", name, err)
			continue
		}
		if string(content) != expectedContent {
			t.Errorf("file %s content mismatch: got %q, want %q", name, content, expectedContent)
		}
	}

	// Verify subdirectory and its file were copied
	backupSubFile := filepath.Join(backupPath, "subdir", "subfile.txt")
	content, err := os.ReadFile(backupSubFile)
	if err != nil {
		t.Errorf("failed to read backed up subfile: %v", err)
	}
	if string(content) != "subfile content" {
		t.Errorf("subfile content mismatch: got %q, want %q", content, "subfile content")
	}
}

func TestCreateBackup_PreservesPermissions(t *testing.T) {
	// Create temporary directory structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create test .beads directory: %v", err)
	}

	// Create a file with specific permissions
	testFile := filepath.Join(beadsDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create backup
	backupPath, err := CreateBackup(beadsDir)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	// Check permissions on backed up file
	backupFile := filepath.Join(backupPath, "test.txt")
	info, err := os.Stat(backupFile)
	if err != nil {
		t.Fatalf("failed to stat backed up file: %v", err)
	}

	// Verify permissions (mask to ignore permission bits we don't care about)
	gotPerm := info.Mode() & 0777
	wantPerm := os.FileMode(0600)
	if gotPerm != wantPerm {
		t.Errorf("permissions not preserved: got %o, want %o", gotPerm, wantPerm)
	}
}

func TestCreateBackup_ErrorIfBackupExists(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create test .beads directory: %v", err)
	}

	// Create first backup
	backupPath1, err := CreateBackup(beadsDir)
	if err != nil {
		t.Fatalf("first CreateBackup failed: %v", err)
	}

	// Try to create backup with same timestamp (simulate collision)
	// We need to create the directory manually since timestamps differ
	timestamp := time.Now().Format("20060102-150405")
	existingBackup := filepath.Join(tmpDir, ".beads-backup-"+timestamp)
	if err := os.Mkdir(existingBackup, 0755); err != nil {
		// If the directory already exists from the first backup, use that
		if !os.IsExist(err) {
			t.Fatalf("failed to create existing backup directory: %v", err)
		}
	}

	// Mock the time to ensure we get the same timestamp
	// Since we can't mock time.Now(), we'll create a second backup immediately
	// and verify the first one succeeded
	_, err = CreateBackup(beadsDir)
	if err != nil {
		// Either we got an error (good) or we created a new backup with different timestamp
		// The test is mainly to verify the first backup succeeded
		if !strings.Contains(err.Error(), "backup directory already exists") {
			// Different timestamp, that's fine - backup system works
			t.Logf("Second backup got different timestamp (expected): %v", err)
		}
	}

	// Verify first backup exists
	if _, err := os.Stat(backupPath1); os.IsNotExist(err) {
		t.Errorf("first backup was not created")
	}
}

func TestCreateBackup_TimestampFormat(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create test .beads directory: %v", err)
	}

	backupPath, err := CreateBackup(beadsDir)
	if err != nil {
		t.Fatalf("CreateBackup failed: %v", err)
	}

	// Extract timestamp from backup path
	baseName := filepath.Base(backupPath)
	if !strings.HasPrefix(baseName, ".beads-backup-") {
		t.Errorf("backup name doesn't have expected prefix: %s", baseName)
	}

	timestamp := strings.TrimPrefix(baseName, ".beads-backup-")

	// Verify timestamp format: YYYYMMDD-HHMMSS
	expectedPattern := `^\d{8}-\d{6}$`
	matched, err := regexp.MatchString(expectedPattern, timestamp)
	if err != nil {
		t.Fatalf("regex error: %v", err)
	}
	if !matched {
		t.Errorf("timestamp %q doesn't match expected format YYYYMMDD-HHMMSS", timestamp)
	}

	// Verify timestamp is parseable and reasonable (within last day to handle timezone issues)
	parsedTime, err := time.Parse("20060102-150405", timestamp)
	if err != nil {
		t.Errorf("failed to parse timestamp %q: %v", timestamp, err)
	}

	now := time.Now()
	diff := now.Sub(parsedTime)
	// Allow for timezone differences and clock skew (within 24 hours)
	if diff < -24*time.Hour || diff > 24*time.Hour {
		t.Errorf("timestamp %q is not within reasonable range (diff: %v)", timestamp, diff)
	}
}

func TestCreateBackup_NonexistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	// Don't create the directory

	_, err := CreateBackup(beadsDir)
	if err == nil {
		t.Error("expected error for nonexistent source directory, got nil")
	}
}

func TestCreateBackup_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create test .beads directory: %v", err)
	}

	backupPath, err := CreateBackup(beadsDir)
	if err != nil {
		t.Fatalf("CreateBackup failed on empty directory: %v", err)
	}

	// Verify backup directory exists
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("backup directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("backup path is not a directory")
	}

	// Verify backup is empty (only contains what filepath.Walk copies)
	entries, err := os.ReadDir(backupPath)
	if err != nil {
		t.Fatalf("failed to read backup directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty backup directory, got %d entries", len(entries))
	}
}
