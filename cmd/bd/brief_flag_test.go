package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

// TestReadyBriefFlag verifies the --brief seam on `bd ready` (be-yvci): the flag
// exists with a safe default (off), and setting it maps onto
// WorkFilter.BriefBodies — the field that selects the body-stripped work-probe
// projection. This is the seam gascity's supervisor work_query (ga-arn) uses.
func TestReadyBriefFlag(t *testing.T) {
	t.Parallel()

	briefFlag := readyCmd.Flags().Lookup("brief")
	if briefFlag == nil {
		t.Fatal("--brief flag should exist on readyCmd")
	}
	if briefFlag.DefValue != "false" {
		t.Errorf("--brief default should be 'false', got %q", briefFlag.DefValue)
	}

	newCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "probe", Run: func(*cobra.Command, []string) {}}
		c.Flags().Bool("brief", false, "")
		return c
	}

	// --brief set ⇒ WorkFilter.BriefBodies == true
	withBrief := newCmd()
	if err := withBrief.Flags().Parse([]string{"--brief"}); err != nil {
		t.Fatalf("parse --brief: %v", err)
	}
	var f types.WorkFilter
	applyReadyBriefFlag(withBrief, &f)
	if !f.BriefBodies {
		t.Error("--brief should set WorkFilter.BriefBodies=true")
	}

	// default (no --brief) ⇒ BriefBodies stays false (no behavior change)
	def := newCmd()
	var f2 types.WorkFilter
	applyReadyBriefFlag(def, &f2)
	if f2.BriefBodies {
		t.Error("absent --brief should leave WorkFilter.BriefBodies=false")
	}
}
