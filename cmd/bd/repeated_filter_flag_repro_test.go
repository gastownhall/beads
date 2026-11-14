package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRepeatedFilterFlagsNotLastValueWins reproduces bd-rtblug: a repeated
// filter flag outside bd list must either union its values or refuse the
// repeat, never silently keep only the last occurrence.
func TestRepeatedFilterFlagsNotLastValueWins(t *testing.T) {
	cases := []struct {
		cmd          *cobra.Command
		flag, v1, v2 string
	}{
		{searchCmd, "status", "open", "closed"},
		{searchCmd, "assignee", "alice", "bob"},
		{countCmd, "status", "open", "closed"},
		{readyCmd, "type", "bug", "task"},
		{staleCmd, "status", "open", "blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.cmd.Name()+"/--"+tc.flag, func(t *testing.T) {
			fl := tc.cmd.Flags().Lookup(tc.flag)
			if fl == nil {
				t.Fatalf("bd %s has no --%s flag", tc.cmd.Name(), tc.flag)
			}
			fs := tc.cmd.Flags()
			t.Cleanup(func() {
				_ = fl.Value.Set("")
				_ = fl.Value.Set(fl.DefValue)
				fl.Changed = false
			})
			if err := fs.Parse([]string{"--" + tc.flag, tc.v1, "--" + tc.flag, tc.v2}); err != nil {
				return // refusing the repeat is correct
			}
			got := fl.Value.String()
			if !strings.Contains(got, tc.v1) {
				t.Errorf("bd %s --%s %s --%s %s: value %q silently dropped %q (last value wins)",
					tc.cmd.Name(), tc.flag, tc.v1, tc.flag, tc.v2, got, tc.v1)
			}
		})
	}
}
