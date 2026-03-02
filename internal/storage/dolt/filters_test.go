package dolt

import "testing"

func TestLooksLikeIssueID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Valid issue IDs
		{"bd-123", true},
		{"hq-319", true},
		{"bd-wisp-abc", true},
		{"bd-a3f", true},
		{"test-1", true},
		{"beads-vscode-1", true},
		{"bd-123.1", true},        // child ID
		{"bd-123.1.2", true},      // grandchild ID
		{"hq-wisp-nmxy", true},    // wisp ID
		{"si-searchbyid-xyz", true},

		// Not issue IDs
		{"authentication bug", false},   // has space
		{"login", false},                // no hyphen
		{"", false},                     // empty
		{"-abc", false},                 // starts with hyphen
		{"abc-", false},                 // ends with hyphen (suffix empty)
		{"hello world", false},          // spaces
		{"fix: something", false},       // colon
		{"search query here", false},    // multiple words
		{"bug_fix", false},              // underscore
		{"feature/branch", false},       // slash
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := looksLikeIssueID(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeIssueID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
