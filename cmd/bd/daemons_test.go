package main

import (
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

// TestDaemonsFormatFunctions tests the formatting helpers
// Integration tests for the actual commands are in daemon_test.go
