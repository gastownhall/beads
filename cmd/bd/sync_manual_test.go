package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
)

func TestTruncateText(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "empty string",
			input:  "",
			maxLen: 10,
			want:   "(empty)",
		},
		{
			name:   "short string",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			input:  "0123456789",
			maxLen: 10,
			want:   "0123456789",
		},
		{
			name:   "truncated",
			input:  "this is a very long string",
			maxLen: 15,
			want:   "this is a ve...",
		},
		{
			name:   "newlines replaced",
			input:  "line1\nline2\nline3",
			maxLen: 30,
			want:   "line1 line2 line3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateText(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateText(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestValueOrNone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "(none)"},
		{"value", "value"},
		{" ", " "},
	}

	for _, tt := range tests {
		got := valueOrNone(tt.input)
		if got != tt.want {
			t.Errorf("valueOrNone(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestInteractiveConflictDisplay(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)

	// Test that displayConflictDiff doesn't panic for various inputs
	tests := []struct {
		name     string
		conflict InteractiveConflict
	}{
		{
			name: "both exist with differences",
			conflict: InteractiveConflict{
				IssueID: "test-1",
				Local: &beads.Issue{
					ID:        "test-1",
					Title:     "Local title",
					Status:    beads.StatusOpen,
					Priority:  1,
					UpdatedAt: now,
				},
				Remote: &beads.Issue{
					ID:        "test-1",
					Title:     "Remote title",
					Status:    beads.StatusInProgress,
					Priority:  2,
					UpdatedAt: earlier,
				},
			},
		},
		{
			name: "local deleted",
			conflict: InteractiveConflict{
				IssueID: "test-2",
				Local:   nil,
				Remote: &beads.Issue{
					ID:        "test-2",
					Title:     "Remote only",
					Status:    beads.StatusOpen,
					UpdatedAt: now,
				},
			},
		},
		{
			name: "remote deleted",
			conflict: InteractiveConflict{
				IssueID: "test-3",
				Local: &beads.Issue{
					ID:        "test-3",
					Title:     "Local only",
					Status:    beads.StatusOpen,
					UpdatedAt: now,
				},
				Remote: nil,
			},
		},
		{
			name: "same timestamps",
			conflict: InteractiveConflict{
				IssueID: "test-4",
				Local: &beads.Issue{
					ID:        "test-4",
					Title:     "Same time local",
					UpdatedAt: now,
				},
				Remote: &beads.Issue{
					ID:        "test-4",
					Title:     "Same time remote",
					UpdatedAt: now,
				},
			},
		},
		{
			name: "with labels",
			conflict: InteractiveConflict{
				IssueID: "test-5",
				Local: &beads.Issue{
					ID:        "test-5",
					Title:     "Local",
					Labels:    []string{"bug", "urgent"},
					UpdatedAt: now,
				},
				Remote: &beads.Issue{
					ID:        "test-5",
					Title:     "Remote",
					Labels:    []string{"feature", "low-priority"},
					UpdatedAt: earlier,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just make sure it doesn't panic
			displayConflictDiff(tt.conflict)
		})
	}
}

func TestShowDetailedDiff(t *testing.T) {
	now := time.Now()

	conflict := InteractiveConflict{
		IssueID: "test-1",
		Local: &beads.Issue{
			ID:        "test-1",
			Title:     "Local",
			UpdatedAt: now,
		},
		Remote: &beads.Issue{
			ID:        "test-1",
			Title:     "Remote",
			UpdatedAt: now,
		},
	}

	// Just make sure it doesn't panic
	showDetailedDiff(conflict)
}

func TestPrintResolutionHelp(t *testing.T) {
	// Test all combinations of hasLocal/hasRemote
	tests := []struct {
		hasLocal  bool
		hasRemote bool
	}{
		{true, true},
		{true, false},
		{false, true},
		{false, false},
	}

	for _, tt := range tests {
		// Just make sure it doesn't panic
		printResolutionHelp(tt.hasLocal, tt.hasRemote)
	}
}

func TestDisplayIssueSummary(t *testing.T) {
	issue := &beads.Issue{
		ID:       "test-1",
		Title:    "Test issue",
		Status:   beads.StatusOpen,
		Priority: 2,
		Assignee: "alice",
	}

	// Just make sure it doesn't panic
	displayIssueSummary(issue, "  ")
	displayIssueSummary(nil, "  ")
}

func TestInteractiveResolutionMerge(t *testing.T) {
	// Test that mergeFieldLevel is called correctly in resolution
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)

	local := &beads.Issue{
		ID:        "test-1",
		Title:     "Local title",
		Status:    beads.StatusOpen,
		Priority:  1,
		Labels:    []string{"bug"},
		UpdatedAt: now,
	}

	remote := &beads.Issue{
		ID:        "test-1",
		Title:     "Remote title",
		Status:    beads.StatusInProgress,
		Priority:  2,
		Labels:    []string{"feature"},
		UpdatedAt: earlier,
	}

	// mergeFieldLevel should pick local values (newer) for scalars
	// and union for labels
	merged := mergeFieldLevel(nil, local, remote)

	if merged.Title != "Local title" {
		t.Errorf("Expected title 'Local title', got %q", merged.Title)
	}
	if merged.Status != beads.StatusOpen {
		t.Errorf("Expected status 'open', got %q", merged.Status)
	}
	if merged.Priority != 1 {
		t.Errorf("Expected priority 1, got %d", merged.Priority)
	}
	// Labels should be merged (union)
	if len(merged.Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(merged.Labels))
	}
	labelsStr := strings.Join(merged.Labels, ",")
	if !strings.Contains(labelsStr, "bug") || !strings.Contains(labelsStr, "feature") {
		t.Errorf("Expected labels to contain 'bug' and 'feature', got %v", merged.Labels)
	}
}
