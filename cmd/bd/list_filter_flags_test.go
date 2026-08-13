package main

import (
	"strings"
	"testing"
)

// resetListFilterFlagState restores listCmd's filter flags to their unparsed
// state so these tests leave nothing behind for the rest of the package.
func resetListFilterFlagState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		resetListFilterFlags()
		for _, name := range []string{"status", "state", "type", "assignee", "id"} {
			if fl := listCmd.Flags().Lookup(name); fl != nil {
				fl.Changed = false
			}
		}
	})
}

func TestListRepeatedStatusUnions(t *testing.T) {
	resetListFilterFlagState(t)

	if err := listCmd.ParseFlags([]string{"--status", "open", "--status", "closed", "-s", "pinned"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	got, err := listCmd.Flags().GetString("status")
	if err != nil {
		t.Fatalf("GetString(status): %v", err)
	}
	if got != "open,closed,pinned" {
		t.Fatalf("repeated --status = %q, want union %q", got, "open,closed,pinned")
	}
}

func TestListSingleAndCommaStatusUnchanged(t *testing.T) {
	resetListFilterFlagState(t)

	if err := listCmd.ParseFlags([]string{"--status", "open"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got, _ := listCmd.Flags().GetString("status"); got != "open" {
		t.Fatalf("single --status = %q, want %q", got, "open")
	}

	resetListFilterFlags()
	if err := listCmd.ParseFlags([]string{"--status", "open,in_progress"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got, _ := listCmd.Flags().GetString("status"); got != "open,in_progress" {
		t.Fatalf("comma --status = %q, want %q", got, "open,in_progress")
	}
}

func TestListRepeatedIDUnions(t *testing.T) {
	resetListFilterFlagState(t)

	if err := listCmd.ParseFlags([]string{"--id", "bd-1", "--id", "bd-2,bd-3"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got, _ := listCmd.Flags().GetString("id"); got != "bd-1,bd-2,bd-3" {
		t.Fatalf("repeated --id = %q, want %q", got, "bd-1,bd-2,bd-3")
	}
}

func TestListRepeatedTypeRefused(t *testing.T) {
	resetListFilterFlagState(t)

	for _, args := range [][]string{
		{"--type", "bug", "--type", "epic"},
		{"-t", "bug", "-t", "epic"},
	} {
		resetListFilterFlags()
		err := listCmd.ParseFlags(args)
		if err == nil {
			t.Fatalf("ParseFlags(%v) accepted a repeated --type", args)
		}
		if !strings.Contains(err.Error(), "type") || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("ParseFlags(%v) error %q does not name the repeated flag", args, err)
		}
	}
}

func TestListRepeatedAssigneeRefused(t *testing.T) {
	resetListFilterFlagState(t)

	err := listCmd.ParseFlags([]string{"--assignee", "alice", "--assignee", "bob"})
	if err == nil {
		t.Fatal("ParseFlags accepted a repeated --assignee")
	}
	if !strings.Contains(err.Error(), "assignee") || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("error %q does not name the repeated flag", err)
	}
}

func TestListSingleTypeAndAssigneeUnchanged(t *testing.T) {
	resetListFilterFlagState(t)

	if err := listCmd.ParseFlags([]string{"--type", "bug", "--assignee", "alice"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got, _ := listCmd.Flags().GetString("type"); got != "bug" {
		t.Fatalf("single --type = %q, want %q", got, "bug")
	}
	if got, _ := listCmd.Flags().GetString("assignee"); got != "alice" {
		t.Fatalf("single --assignee = %q, want %q", got, "alice")
	}
}

// bd children borrows listCmd's status flag with Set("all") and a deferred
// Set(""); an empty Set must fully reset both value kinds.
func TestEmptySetResetsFilterFlags(t *testing.T) {
	resetListFilterFlagState(t)

	fl := listCmd.Flags()
	_ = fl.Set("status", "all")
	_ = fl.Set("status", "")
	_ = fl.Set("status", "open")
	if got, _ := fl.GetString("status"); got != "open" {
		t.Fatalf("status after reset = %q, want %q", got, "open")
	}

	_ = fl.Set("type", "bug")
	_ = fl.Set("type", "")
	if err := fl.Set("type", "epic"); err != nil {
		t.Fatalf("Set(type) after reset refused: %v", err)
	}
	if got, _ := fl.GetString("type"); got != "epic" {
		t.Fatalf("type after reset = %q, want %q", got, "epic")
	}
}

// Two parses in one process must not bleed into each other once
// resetListFilterFlags has run between them, the way gatherListInput does.
func TestFilterFlagsDoNotLeakAcrossParses(t *testing.T) {
	resetListFilterFlagState(t)

	if err := listCmd.ParseFlags([]string{"--status", "open", "--status", "closed"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	resetListFilterFlags()
	if err := listCmd.ParseFlags([]string{"--status", "pinned"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got, _ := listCmd.Flags().GetString("status"); got != "pinned" {
		t.Fatalf("status after second parse = %q, want %q (leaked prior parse)", got, "pinned")
	}
}
