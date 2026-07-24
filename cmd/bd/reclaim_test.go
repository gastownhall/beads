package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// newReclaimTestCmd builds a command with the real reclaim scope flag set so the
// parser under test is exactly the one production uses (registerReclaimScopeFlags
// is shared with init()).
func newReclaimTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "reclaim", RunE: func(*cobra.Command, []string) error { return nil }}
	registerReclaimScopeFlags(cmd.Flags())
	return cmd
}

func TestReclaimFilterFromFlagsUnset(t *testing.T) {
	cmd := newReclaimTestCmd()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	filter, err := reclaimFilterFromFlags(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filter.IsEmpty() {
		t.Fatalf("no flags => filter %+v, want empty (global sweep)", filter)
	}
}

func TestReclaimFilterFromFlagsPopulated(t *testing.T) {
	cmd := newReclaimTestCmd()
	args := []string{
		"--id", "wy-a", "--id", "wy-b",
		"--assignee", "zelda",
		"--label", "lane-a",
		"--label-any", "x,y",
		"--exclude-label", "pinned",
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	filter, err := reclaimFilterFromFlags(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filter.IsEmpty() {
		t.Fatal("populated flags => empty filter")
	}
	if len(filter.IDs) != 2 || filter.IDs[0] != "wy-a" || filter.IDs[1] != "wy-b" {
		t.Errorf("IDs = %v", filter.IDs)
	}
	if len(filter.Assignees) != 1 || filter.Assignees[0] != "zelda" {
		t.Errorf("Assignees = %v", filter.Assignees)
	}
	if len(filter.Labels) != 1 || filter.Labels[0] != "lane-a" {
		t.Errorf("Labels = %v", filter.Labels)
	}
	if len(filter.LabelsAny) != 2 {
		t.Errorf("LabelsAny = %v", filter.LabelsAny)
	}
	if len(filter.ExcludeLabels) != 1 || filter.ExcludeLabels[0] != "pinned" {
		t.Errorf("ExcludeLabels = %v", filter.ExcludeLabels)
	}
}

// A scope flag that parses to no usable value is operator error: it must be a
// hard error, never a silent degrade into a global sweep.
func TestReclaimFilterFromFlagsRejectsEmptyValue(t *testing.T) {
	for _, flag := range []string{"--label", "--label-any", "--assignee", "--id", "--exclude-label"} {
		t.Run(flag, func(t *testing.T) {
			cmd := newReclaimTestCmd()
			if err := cmd.ParseFlags([]string{flag, ""}); err != nil {
				t.Fatal(err)
			}
			if _, err := reclaimFilterFromFlags(cmd); err == nil {
				t.Fatalf("%s '' => nil error, want a hard error", flag)
			}
		})
	}
}
