//go:build !windows

package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBeadsDirPermConstants(t *testing.T) {
	if BeadsDirPerm != 0700 {
		t.Errorf("BeadsDirPerm = %04o, want 0700", BeadsDirPerm)
	}
	if BeadsFilePerm != 0600 {
		t.Errorf("BeadsFilePerm = %04o, want 0600", BeadsFilePerm)
	}
}

func TestEnsureBeadsDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".beads")
	if err := EnsureBeadsDir(dir); err != nil {
		t.Fatalf("EnsureBeadsDir(%q) = %v", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q) = %v", dir, err)
	}
	perm := info.Mode().Perm()
	if perm != BeadsDirPerm {
		t.Errorf("directory permissions = %04o, want %04o", perm, BeadsDirPerm)
	}
}

func TestEnsureBeadsDirNested(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".beads", "dolt")
	if err := EnsureBeadsDir(dir); err != nil {
		t.Fatalf("EnsureBeadsDir(%q) = %v", dir, err)
	}
	// Both parent and child should exist
	for _, d := range []string{filepath.Dir(dir), dir} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("Stat(%q) = %v", d, err)
		}
		perm := info.Mode().Perm()
		if perm != BeadsDirPerm {
			t.Errorf("%s permissions = %04o, want %04o", d, perm, BeadsDirPerm)
		}
	}
}

func TestCheckBeadsDirPermissions_Secure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	CheckBeadsDirPermissions(dir)

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if buf.Len() != 0 {
		t.Errorf("expected no warning for 0700 dir, got: %s", buf.String())
	}
}

func TestCheckBeadsDirPermissions_Permissive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	CheckBeadsDirPermissions(dir)

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	want := fmt.Sprintf("Warning: %s has permissions 0755 (recommended: 0700). Run: chmod 700 %s\n", dir, dir)
	if buf.String() != want {
		t.Errorf("warning = %q, want %q", buf.String(), want)
	}
}

func TestCheckBeadsDirPermissions_Nonexistent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-such-dir")
	// Capture stderr — should produce no output
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	CheckBeadsDirPermissions(dir)

	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if buf.Len() != 0 {
		t.Errorf("expected no output for nonexistent dir, got: %s", buf.String())
	}
}

func TestFixBeadsDirPermissions(t *testing.T) {
	tests := []struct {
		name      string
		startPerm os.FileMode
		wantFixed bool
		wantPerm  os.FileMode
	}{
		{"world_readable_0755", 0755, true, 0700},
		{"world_writable_0777", 0777, true, 0700},
		{"world_only_0707", 0707, true, 0700},
		{"group_only_0770", 0770, true, 0700},
		{"already_secure_0700", 0700, false, 0700},
		{"owner_only_0600", 0600, false, 0600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), ".beads")
			if err := os.Mkdir(dir, tt.startPerm); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tt.startPerm); err != nil {
				t.Fatal(err)
			}

			fixed, err := FixBeadsDirPermissions(dir)
			if err != nil {
				t.Fatalf("FixBeadsDirPermissions() error = %v", err)
			}
			if fixed != tt.wantFixed {
				t.Errorf("fixed = %v, want %v", fixed, tt.wantFixed)
			}

			info, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("Stat() error = %v", err)
			}
			got := info.Mode().Perm()
			if got != tt.wantPerm {
				t.Errorf("permissions after fix = %04o, want %04o", got, tt.wantPerm)
			}
		})
	}
}

func TestFixBeadsDirPermissions_Nonexistent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-such-dir")
	fixed, err := FixBeadsDirPermissions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fixed {
		t.Error("expected fixed=false for nonexistent directory")
	}
}
