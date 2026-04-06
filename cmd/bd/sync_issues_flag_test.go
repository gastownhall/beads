package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestIssuesFlagRegistered verifies that --issues is registered on all tracker sync commands.
func TestIssuesFlagRegistered(t *testing.T) {
	// Not parallel: accesses shared cobra command tree.

	trackers := []struct {
		name    string
		syncCmd *cobra.Command
	}{
		{"ado", adoSyncCmd},
		{"jira", jiraSyncCmd},
		{"linear", linearSyncCmd},
		{"github", githubSyncCmd},
		{"gitlab", gitlabSyncCmd},
		{"notion", notionSyncCmd},
	}

	for _, tc := range trackers {
		t.Run(tc.name, func(t *testing.T) {
			flag := tc.syncCmd.Flags().Lookup("issues")
			if flag == nil {
				t.Fatalf("%s sync command missing --issues flag", tc.name)
			}
			if flag.DefValue != "" {
				t.Errorf("%s --issues default = %q, want empty", tc.name, flag.DefValue)
			}
		})
	}
}

// TestIssuesFlagParsing verifies that --issues parses comma-separated IDs correctly.
func TestIssuesFlagParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  []string
	}{
		{"bd-abc,bd-def", []string{"bd-abc", "bd-def"}},
		{"bd-abc", []string{"bd-abc"}},
		{" bd-abc , bd-def ", []string{"bd-abc", "bd-def"}},
		{"", nil},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := splitCSV(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("splitCSV(%q) = %v (len %d), want %v (len %d)",
					tc.input, got, len(got), tc.want, len(tc.want))
			}
			for i, v := range got {
				if v != tc.want[i] {
					t.Errorf("splitCSV(%q)[%d] = %q, want %q", tc.input, i, v, tc.want[i])
				}
			}
		})
	}
}
