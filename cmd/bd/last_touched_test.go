package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLastTouchedBasic(t *testing.T) {
	// Create a temp directory to simulate .beads
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a marker file so FindBeadsDir recognizes this as a valid beads directory
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Save the original working directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(origDir)
	}()

	// Change to temp directory so FindBeadsDir finds our .beads
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Test that no last touched returns empty
	got := GetLastTouchedID()
	if got != "" {
		t.Errorf("GetLastTouchedID() = %q, want empty", got)
	}

	// Set and retrieve
	testID := "bd-test123"
	SetLastTouchedID(testID)
	got = GetLastTouchedID()
	if got != testID {
		t.Errorf("GetLastTouchedID() = %q, want %q", got, testID)
	}

	// Update with new ID
	testID2 := "bd-test456"
	SetLastTouchedID(testID2)
	got = GetLastTouchedID()
	if got != testID2 {
		t.Errorf("GetLastTouchedID() = %q, want %q", got, testID2)
	}

	// Clear and verify
	ClearLastTouched()
	got = GetLastTouchedID()
	if got != "" {
		t.Errorf("After ClearLastTouched(), GetLastTouchedID() = %q, want empty", got)
	}
}

// TestSetLastTouchedIDAdvancesMtime verifies the write marker's mtime advances
// even when the SAME ID is rewritten, so mtime-keyed consumers (file-watch
// fingerprints, cache validators) never see an "identical" marker (GH#3965).
func TestSetLastTouchedIDAdvancesMtime(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(beadsDir, lastTouchedFile)

	SetLastTouchedID("bd-same")
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after first write: %v", err)
	}

	// Sleep past filesystem mtime resolution so an advanced mtime is observable.
	time.Sleep(20 * time.Millisecond)

	// Rewrite the SAME ID — mtime must still advance.
	SetLastTouchedID("bd-same")
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after rewrite: %v", err)
	}

	if !info2.ModTime().After(info1.ModTime()) {
		t.Errorf("mtime should advance on rewrite of same ID: first=%v second=%v",
			info1.ModTime(), info2.ModTime())
	}
}

func TestSetLastTouchedIDIgnoresEmpty(t *testing.T) {
	// Create a temp directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a marker file so FindBeadsDir recognizes this as a valid beads directory
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Save the original working directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(origDir)
	}()

	// Change to temp directory
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// First set a value
	testID := "bd-original"
	SetLastTouchedID(testID)

	// Try to set empty - should be ignored
	SetLastTouchedID("")

	// Should still have original value
	got := GetLastTouchedID()
	if got != testID {
		t.Errorf("After SetLastTouchedID(\"\"), GetLastTouchedID() = %q, want %q", got, testID)
	}
}
