package main

import (
	"io"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/types"
)

// TestPushPullSubcommandsRegistered verifies push/pull subcommands exist on all tracker commands.
func TestPushPullSubcommandsRegistered(t *testing.T) {
	// Not parallel: accesses shared cobra command tree (races with InheritedFlags/Find).

	trackers := []struct {
		name string
		cmd  *cobra.Command
	}{
		{"ado", adoCmd},
		{"jira", jiraCmd},
		{"linear", linearCmd},
		{"github", githubCmd},
		{"gitlab", gitlabCmd},
		{"notion", notionCmd},
	}

	for _, tc := range trackers {
		t.Run(tc.name+"/push", func(t *testing.T) {
			sub, _, err := tc.cmd.Find([]string{"push"})
			if err != nil {
				t.Fatalf("%s missing push subcommand: %v", tc.name, err)
			}
			if sub.Name() != "push" {
				t.Fatalf("%s push resolved to %q", tc.name, sub.Name())
			}
			// Verify --dry-run flag exists
			if sub.Flags().Lookup("dry-run") == nil {
				t.Fatalf("%s push missing --dry-run flag", tc.name)
			}
		})

		t.Run(tc.name+"/pull", func(t *testing.T) {
			sub, _, err := tc.cmd.Find([]string{"pull"})
			if err != nil {
				t.Fatalf("%s missing pull subcommand: %v", tc.name, err)
			}
			if sub.Name() != "pull" {
				t.Fatalf("%s pull resolved to %q", tc.name, sub.Name())
			}
			// Verify --dry-run flag exists
			if sub.Flags().Lookup("dry-run") == nil {
				t.Fatalf("%s pull missing --dry-run flag", tc.name)
			}
		})
	}
}

// TestPushSubcommandRequiresArgs verifies push commands require at least one argument.
func TestPushSubcommandRequiresArgs(t *testing.T) {
	// Not parallel: accesses shared cobra command tree.

	// Test ADO push (uses RunE, so we can test it directly)
	err := adoPushCmd.RunE(adoPushCmd, []string{})
	if err == nil {
		t.Error("ado push with no args should return error")
	}

	// Test GitHub push (uses RunE)
	err = githubPushCmd.RunE(githubPushCmd, []string{})
	if err == nil {
		t.Error("github push with no args should return error")
	}

	// Test GitLab push (uses RunE)
	err = gitlabPushCmd.RunE(gitlabPushCmd, []string{})
	if err == nil {
		t.Error("gitlab push with no args should return error")
	}
}

// TestPullSubcommandRequiresArgs verifies pull commands require at least one argument.
func TestPullSubcommandRequiresArgs(t *testing.T) {
	// Not parallel: accesses shared cobra command tree.

	// Test ADO pull (uses RunE)
	err := adoPullCmd.RunE(adoPullCmd, []string{})
	if err == nil {
		t.Error("ado pull with no args should return error")
	}

	// Test GitHub pull (uses RunE)
	err = githubPullCmd.RunE(githubPullCmd, []string{})
	if err == nil {
		t.Error("github pull with no args should return error")
	}

	// Test GitLab pull (uses RunE)
	err = gitlabPullCmd.RunE(gitlabPullCmd, []string{})
	if err == nil {
		t.Error("gitlab pull with no args should return error")
	}
}

// TestNewGitHubEngineCarriesThePushHash pins the engine every GitHub command
// builds, `bd github pull` included, to GitHub's push hash. A pull engine
// without it clears the stored hash instead of refreshing it, and the next
// push PATCHes bd's normalized labels over GitHub labels bd cannot map.
func TestNewGitHubEngineCarriesThePushHash(t *testing.T) {
	engine := newGitHubEngine(&github.Tracker{}, io.Discard)
	if engine.PushHooks == nil || engine.PushHooks.ContentHash == nil {
		t.Fatal("GitHub engine has no push ContentHash, so a pull clears the push hash")
	}
	ref := "https://github.com/o/r/issues/1"
	issue := &types.Issue{
		ID: "bd-1", Title: "Fix it", Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeTask, ExternalRef: &ref,
	}
	want := github.PushContentHash(issue, github.DefaultMappingConfig())
	if got := engine.PushHooks.ContentHash(issue); got == "" || got != want {
		t.Fatalf("ContentHash = %q, want GitHub's push hash %q", got, want)
	}
}
