package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/term"
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

// TestAllowLastTouchedFallback_EnvPrecedence covers the env-driven branches
// of the guard (bd-m00pb). The trailing stdin-TTY branch is exercised by the
// default-deny case below and end-to-end in last_touched_guard_test.go.
func TestAllowLastTouchedFallback_EnvPrecedence(t *testing.T) {
	cases := []struct {
		name           string
		fallbackEnv    string
		nonInteractive string
		ci             string
		want           bool
	}{
		{"explicit 1 wins over non-interactive", "1", "1", "true", true},
		{"explicit true wins over CI", "true", "", "true", true},
		{"explicit 0 denies", "0", "", "", false},
		{"explicit false denies", "false", "", "", false},
		{"garbage value denies", "yes", "", "", false},
		{"BD_NON_INTERACTIVE=1 denies", "", "1", "", false},
		{"BD_NON_INTERACTIVE=true denies", "", "true", "", false},
		{"CI=true denies", "", "", "true", false},
		{"CI=1 denies", "", "", "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(lastTouchedFallbackEnv, tc.fallbackEnv)
			t.Setenv("BD_NON_INTERACTIVE", tc.nonInteractive)
			t.Setenv("CI", tc.ci)
			if got := AllowLastTouchedFallback(); got != tc.want {
				t.Errorf("AllowLastTouchedFallback() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAllowLastTouchedFallback_DefaultDeniesNonTTY verifies the default
// path denies when stdin is not a terminal and no env override is set.
func TestAllowLastTouchedFallback_DefaultDeniesNonTTY(t *testing.T) {
	t.Setenv(lastTouchedFallbackEnv, "")
	t.Setenv("BD_NON_INTERACTIVE", "")
	t.Setenv("CI", "")
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal; default-deny branch not observable")
	}
	if AllowLastTouchedFallback() {
		t.Error("AllowLastTouchedFallback() = true with non-TTY stdin and no override, want false")
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
