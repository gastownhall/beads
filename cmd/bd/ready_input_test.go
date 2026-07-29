package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestGatherReadyInputPropagatesLabelPatternAndRegex(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("sort", "priority", "")
	cmd.Flags().String("label-pattern", "", "")
	cmd.Flags().String("label-regex", "", "")
	if err := cmd.Flags().Set("label-pattern", "tech-*"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("label-regex", "^tech-(debt|legacy)$"); err != nil {
		t.Fatal(err)
	}

	in, err := gatherReadyInput(cmd)
	if err != nil {
		t.Fatalf("gatherReadyInput() error = %v", err)
	}
	if got, want := in.filter.LabelPattern, "tech-*"; got != want {
		t.Errorf("filter.LabelPattern = %q, want %q", got, want)
	}
	if got, want := in.filter.LabelRegex, "^tech-(debt|legacy)$"; got != want {
		t.Errorf("filter.LabelRegex = %q, want %q", got, want)
	}
}
