package main

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "less than a minute",
			duration: 45 * time.Second,
			want:     "45 seconds",
		},
		{
			name:     "exactly one minute",
			duration: 60 * time.Second,
			want:     "1 minutes",
		},
		{
			name:     "several minutes",
			duration: 5 * time.Minute,
			want:     "5 minutes",
		},
		{
			name:     "one hour",
			duration: 60 * time.Minute,
			want:     "1.0 hours",
		},
		{
			name:     "several hours",
			duration: 3*time.Hour + 30*time.Minute,
			want:     "3.5 hours",
		},
		{
			name:     "one day",
			duration: 24 * time.Hour,
			want:     "1.0 days",
		},
		{
			name:     "multiple days",
			duration: 3*24*time.Hour + 12*time.Hour,
			want:     "3.5 days",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestStaleIssueInfo(t *testing.T) {
	// Test that StaleIssueInfo struct can be created and serialized
	info := &StaleIssueInfo{
		IssueID:            "bd-42",
		IssueTitle:         "Test Issue",
		IssuePriority:      1,
		ExecutorInstanceID: "exec-123",
		ExecutorStatus:     "stopped",
		ExecutorHostname:   "localhost",
		ExecutorPID:        12345,
		LastHeartbeat:      time.Now().Add(-10 * time.Minute),
		ClaimedAt:          time.Now().Add(-30 * time.Minute),
		ClaimedDuration:    "30 minutes",
	}

	if info.IssueID != "bd-42" {
		t.Errorf("Expected IssueID bd-42, got %s", info.IssueID)
	}
	if info.ExecutorStatus != "stopped" {
		t.Errorf("Expected ExecutorStatus stopped, got %s", info.ExecutorStatus)
	}
}
