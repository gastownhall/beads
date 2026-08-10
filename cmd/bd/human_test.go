package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

func TestPrintHumanStats(t *testing.T) {
	tests := []struct {
		name   string
		issues []*types.Issue
		// We just verify no panic; output goes to stdout
	}{
		{
			name:   "empty list",
			issues: nil,
		},
		{
			name: "mixed statuses",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "open"},
				{ID: "bd-2", Status: "in_progress"},
				{ID: "bd-3", Status: "blocked"},
				{ID: "bd-4", Status: "closed", CloseReason: "Responded"},
				{ID: "bd-5", Status: "closed", CloseReason: "Dismissed: not needed"},
				{ID: "bd-6", Status: "hooked"},
			},
		},
		{
			name: "all closed responded",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "closed", CloseReason: "Responded"},
				{ID: "bd-2", Status: "closed", CloseReason: "Responded"},
			},
		},
		{
			name: "all dismissed",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "closed", CloseReason: "Dismissed"},
				{ID: "bd-2", Status: "closed", CloseReason: "Dismissed: stale"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify no panic
			printHumanStats(tt.issues)
		})
	}
}

func TestPrintHumanList(t *testing.T) {
	tests := []struct {
		name   string
		issues []*types.Issue
	}{
		{
			name:   "empty list",
			issues: nil,
		},
		{
			name: "single issue",
			issues: []*types.Issue{
				{ID: "bd-abc", Title: "Need human input", Status: "open", Priority: 1},
			},
		},
		{
			name: "multiple issues with varied status",
			issues: []*types.Issue{
				{ID: "bd-1", Title: "Review needed", Status: "open"},
				{ID: "bd-2", Title: "Approval required", Status: "blocked", Priority: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify no panic
			printHumanList(tt.issues)
		})
	}
}

func TestHumanCmdSubcommands(t *testing.T) {
	// Verify all subcommands are registered
	subCmds := humanCmd.Commands()
	names := make([]string, len(subCmds))
	for i, cmd := range subCmds {
		names[i] = cmd.Name()
	}
	joined := strings.Join(names, ",")

	for _, expected := range []string{"list", "respond", "dismiss", "stats"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing subcommand %q in human command", expected)
		}
	}
}

// TestHumanRespondDismissArgs pins the Args policy for respond and dismiss:
// an issue ID is required and trailing args are free text, not extra IDs
// (MinimumNArgs(1), not ExactArgs(1)). End-to-end coverage lives in the
// embedded tests, which are env-gated — this always-run check guards the
// declaration itself.
func TestHumanRespondDismissArgs(t *testing.T) {
	for _, cmd := range []*cobra.Command{humanRespondCmd, humanDismissCmd} {
		if err := cmd.Args(cmd, []string{"bd-123", "free", "text"}); err != nil {
			t.Errorf("%s should accept positional free text after the ID: %v", cmd.Name(), err)
		}
		if err := cmd.Args(cmd, []string{}); err == nil {
			t.Errorf("%s should still require an issue ID", cmd.Name())
		}
	}
}

func TestHumanRespondTextSourceFlags(t *testing.T) {
	for _, name := range []string{"file", "stdin"} {
		if humanRespondCmd.Flags().Lookup(name) == nil {
			t.Errorf("respond command should have --%s flag", name)
		}
	}

	// --response must not be marked required: the response can also come from
	// --file, --stdin, or positional args, and cobra rejects those invocations
	// before RunE if the flag carries the required annotation.
	flag := humanRespondCmd.Flags().Lookup("response")
	if flag == nil {
		t.Fatal("respond command should have --response flag")
	}
	if len(flag.Annotations[cobra.BashCompOneRequiredFlag]) > 0 {
		t.Error("--response must not be hard-required; --file/--stdin/positional text are valid sources")
	}
}

func TestHumanDismissHasReasonFlag(t *testing.T) {
	flag := humanDismissCmd.Flags().Lookup("reason")
	if flag == nil {
		t.Fatal("dismiss command should have --reason flag")
	}
}

func TestHumanListHasStatusFlag(t *testing.T) {
	flag := humanListCmd.Flags().Lookup("status")
	if flag == nil {
		t.Fatal("list command should have --status flag")
	}
}
