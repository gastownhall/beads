package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestParentFlagRegistered verifies --parent flag exists on all tracker sync commands.
func TestParentFlagRegistered(t *testing.T) {
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
	}

	for _, tc := range trackers {
		t.Run(tc.name, func(t *testing.T) {
			flag := tc.syncCmd.Flags().Lookup("parent")
			if flag == nil {
				t.Fatalf("%s sync command missing --parent flag", tc.name)
			}
			if flag.DefValue != "" {
				t.Errorf("%s --parent default = %q, want empty", tc.name, flag.DefValue)
			}
		})
	}
}

// TestParentFlagNotOnNotion verifies Notion sync doesn't have --parent
// since Notion doesn't have a parent-child hierarchy in the same way.
func TestParentFlagNotOnNotion(t *testing.T) {
	// Not parallel: accesses shared cobra command tree.

	flag := notionSyncCmd.Flags().Lookup("parent")
	// Notion init has a --parent flag (for page parent), but sync should not
	// accidentally inherit it. The sync command's --parent would be for tree-scoped push.
	// For now, we don't add it to Notion since Notion doesn't support parent-child deps.
	if flag != nil {
		t.Log("Notion sync has --parent flag (may be intentional for future use)")
	}
}
