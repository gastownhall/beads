package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestListTypedFormatBeatsAConfigJSONDefault pins GH#6278. The root pre-run
// promotes a config-file (or env) `json: true` into jsonOutput unless the
// ROOT --format changed, but `bd list` registers its own --format, which
// shadows the root one. So `bd list --format digraph` under `json: true`
// printed the JSON listing instead of the graph. A --format the caller typed
// must win over that default; an explicit --json still wins over --format.
func TestListTypedFormatBeatsAConfigJSONDefault(t *testing.T) {
	t.Run("typed format clears a config json default", func(t *testing.T) {
		// pinJSONOutput(true) stands in for the pre-run having read json: true.
		pinJSONOutput(t, true)
		in, err := gatherListInput(newListFlagsCommand(t, "--format", "digraph"))
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		if in.jsonOutput || jsonOutput {
			t.Errorf("jsonOutput = %v (in.jsonOutput = %v), want false: the typed --format must beat the config default", jsonOutput, in.jsonOutput)
		}
		if in.formatStr != "digraph" {
			t.Errorf("formatStr = %q, want %q", in.formatStr, "digraph")
		}
	})

	t.Run("explicit --json still beats --format", func(t *testing.T) {
		pinJSONOutput(t, true)
		cmd := newListFlagsCommand(t, "--format", "digraph")
		root := &cobra.Command{Use: "bd"}
		root.PersistentFlags().Bool("json", false, "")
		root.AddCommand(cmd)
		if err := root.PersistentFlags().Set("json", "true"); err != nil {
			t.Fatalf("set --json: %v", err)
		}
		in, err := gatherListInput(cmd)
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		if !in.jsonOutput || !jsonOutput {
			t.Errorf("jsonOutput = %v, want true: an explicit --json outranks --format", jsonOutput)
		}
	})

	t.Run("no typed format leaves the config default alone", func(t *testing.T) {
		pinJSONOutput(t, true)
		in, err := gatherListInput(newListFlagsCommand(t))
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		if !in.jsonOutput {
			t.Error("in.jsonOutput = false, want true: without --format the config json default applies")
		}
	})

	t.Run("--format json still turns json on", func(t *testing.T) {
		pinJSONOutput(t, false)
		in, err := gatherListInput(newListFlagsCommand(t, "--format", "json"))
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		if !in.jsonOutput || in.formatStr != "" {
			t.Errorf("jsonOutput = %v, formatStr = %q, want true and empty", in.jsonOutput, in.formatStr)
		}
	})
}
