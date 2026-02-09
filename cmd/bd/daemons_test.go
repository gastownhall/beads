package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFormatDaemonDuration(t *testing.T) {
	tests := []struct {
		name     string
		seconds  float64
		expected string
	}{
		{"zero", 0, "0s"},
		{"seconds", 45, "45s"},
		{"minutes", 90.5, "2m"},
		{"hours", 3661, "1.0h"},
		{"days", 86400, "1.0d"},
		{"mixed", 93784, "1.1d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDaemonDuration(tt.seconds)
			if got != tt.expected {
				t.Errorf("formatDaemonDuration(%f) = %q, want %q", tt.seconds, got, tt.expected)
			}
		})
	}
}

func TestFormatDaemonRelativeTime(t *testing.T) {
	tests := []struct {
		name     string
		ago      time.Duration
		expected string
	}{
		{"just now", 5 * time.Second, "just now"},
		{"minutes ago", 3 * time.Minute, "3m ago"},
		{"hours ago", 2 * time.Hour, "2.0h ago"},
		{"days ago", 25 * time.Hour, "1.0d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTime := time.Now().Add(-tt.ago)
			got := formatDaemonRelativeTime(testTime)
			if got != tt.expected {
				t.Errorf("formatDaemonRelativeTime(%v) = %q, want %q", testTime, got, tt.expected)
			}
		})
	}
}

func TestFileRotated_SameFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.log")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if fileRotated(f, path) {
		t.Error("fileRotated should return false for the same file")
	}
}

func TestFileRotated_DifferentFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.log")
	if err := os.WriteFile(path, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Simulate rotation: rename old, create new at same path
	rotated := filepath.Join(tmp, "test.log.1")
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if !fileRotated(f, path) {
		t.Error("fileRotated should return true after rotation")
	}
}

func TestFileRotated_PathGone(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.log")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Remove the file at path
	os.Remove(path)

	// When path is gone, fileRotated returns false (not rotated yet)
	if fileRotated(f, path) {
		t.Error("fileRotated should return false when path is gone")
	}
}

// TestDaemonsFormatFunctions tests the formatting helpers
// Integration tests for the actual commands are in daemon_test.go
