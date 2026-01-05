package main

import (
	"testing"
)

func TestParseGitStatusForBeadsChanges(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		expected bool
	}{
		// No changes
		{
			name:     "empty status",
			status:   "",
			expected: false,
		},
		{
			name:     "whitespace only",
			status:   "   \n",
			expected: false,
		},

		// Modified (should return true)
		{
			name:     "staged modified",
			status:   "M  .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "unstaged modified",
			status:   " M .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "staged and unstaged modified",
			status:   "MM .beads/issues.jsonl",
			expected: true,
		},

		// Added (should return true)
		{
			name:     "staged added",
			status:   "A  .beads/issues.jsonl",
			expected: true,
		},
		{
			name:     "added then modified",
			status:   "AM .beads/issues.jsonl",
			expected: true,
		},

		// Untracked (should return false)
		{
			name:     "untracked file",
			status:   "?? .beads/issues.jsonl",
			expected: false,
		},

		// Deleted (should return false)
		{
			name:     "staged deleted",
			status:   "D  .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "unstaged deleted",
			status:   " D .beads/issues.jsonl",
			expected: false,
		},

		// Edge cases
		{
			name:     "renamed file",
			status:   "R  old.jsonl -> .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "copied file",
			status:   "C  source.jsonl -> .beads/issues.jsonl",
			expected: false,
		},
		{
			name:     "status too short",
			status:   "M",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseGitStatusForBeadsChanges(tt.status)
			if result != tt.expected {
				t.Errorf("parseGitStatusForBeadsChanges(%q) = %v, want %v",
					tt.status, result, tt.expected)
			}
		})
	}
}
