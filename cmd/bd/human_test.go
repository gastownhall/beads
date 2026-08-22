package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
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

func TestHumanListFilter(t *testing.T) {
	cfg := workapi.ListConfig{
		CustomStatuses: []types.CustomStatus{
			{Name: "archived", Category: types.CategoryDone},
			{Name: "review", Category: types.CategoryActive},
		},
	}

	t.Run("default hides the canonical done/frozen statuses", func(t *testing.T) {
		filter, err := humanListFilter("", cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		excluded := make(map[types.Status]bool)
		for _, s := range filter.ExcludeStatus {
			excluded[s] = true
		}
		for _, want := range []types.Status{types.StatusClosed, types.StatusPinned, "archived"} {
			if !excluded[want] {
				t.Errorf("default filter should exclude %q, got ExcludeStatus=%v", want, filter.ExcludeStatus)
			}
		}
		if excluded["review"] {
			t.Errorf("active-category custom status should not be excluded, got %v", filter.ExcludeStatus)
		}
		if filter.Status != nil {
			t.Errorf("default filter should not pin a status, got %v", *filter.Status)
		}
	})

	t.Run("default hides pinned beads but no bead types", func(t *testing.T) {
		filter, err := humanListFilter("", cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filter.Pinned == nil || *filter.Pinned {
			t.Errorf("default filter should hide boolean-pinned beads, got Pinned=%v", filter.Pinned)
		}
		if filter.Limit != 0 {
			t.Errorf("human list should stay unlimited, got Limit=%d", filter.Limit)
		}
	})

	t.Run("no bead type is ever hidden", func(t *testing.T) {
		for _, status := range []string{"", "open", "all"} {
			filter, err := humanListFilter(status, cfg)
			if err != nil {
				t.Fatalf("unexpected error for status %q: %v", status, err)
			}
			if len(filter.ExcludeTypes) != 0 {
				t.Errorf("status %q: human list must not exclude bead types (gates, infra), got %v", status, filter.ExcludeTypes)
			}
			if filter.SkipWisps {
				t.Errorf("status %q: human list must show human-labeled wisps", status)
			}
			if filter.IsTemplate != nil {
				t.Errorf("status %q: human list must not hide templates, got IsTemplate=%v", status, *filter.IsTemplate)
			}
		}
	})

	t.Run("explicit status overrides default", func(t *testing.T) {
		filter, err := humanListFilter("closed", cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filter.Status == nil || *filter.Status != types.StatusClosed {
			t.Errorf("expected Status=closed, got %v", filter.Status)
		}
		if len(filter.ExcludeStatus) != 0 {
			t.Errorf("explicit status should drop the default exclusions, got %v", filter.ExcludeStatus)
		}
	})

	t.Run("all shows every status", func(t *testing.T) {
		filter, err := humanListFilter("all", cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filter.Status != nil || len(filter.Statuses) != 0 || len(filter.ExcludeStatus) != 0 {
			t.Errorf("--status=all should not constrain status, got Status=%v Statuses=%v ExcludeStatus=%v",
				filter.Status, filter.Statuses, filter.ExcludeStatus)
		}
	})

	t.Run("invalid status is refused", func(t *testing.T) {
		if _, err := humanListFilter("nonesuch", cfg); err == nil {
			t.Error("expected an error for an unknown status")
		}
	})

	t.Run("always filters on human label", func(t *testing.T) {
		for _, status := range []string{"", "open", "all"} {
			filter, err := humanListFilter(status, cfg)
			if err != nil {
				t.Fatalf("unexpected error for status %q: %v", status, err)
			}
			if len(filter.Labels) != 1 || filter.Labels[0] != "human" {
				t.Errorf("expected Labels=[human] for status %q, got %v", status, filter.Labels)
			}
		}
	})
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
