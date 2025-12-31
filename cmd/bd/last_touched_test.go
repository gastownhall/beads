package main

import (
	"os"
	"path/filepath"
	"testing"
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
