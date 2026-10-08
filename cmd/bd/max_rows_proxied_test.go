package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestGraphAndFindDuplicatesHonorMaxRowsUnderProxiedServer is the proxied-route
// pin for the two other commands that used to refuse a positive row cap there.
// Both proxied routes read through the unit-of-work SearchIssues seam, which
// sizes its window from IssueFilter.MaxRows and runs EnforceMaxRowsCap, so the
// cap is threaded the same way `bd list` threads it and the refusal was the
// only thing between BEADS_MAX_ROWS and a working command.
//
// As in TestReadyHonorsMaxRowsUnderProxiedServer the assertion is two-sided:
// no refusal, AND the run reached the proxied route, which with a nil
// uowProvider fails opening its unit of work rather than dialing a server.
func TestGraphAndFindDuplicatesHonorMaxRowsUnderProxiedServer(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)

	if uowProvider != nil {
		t.Fatal("precondition: uowProvider must be nil so the route cannot open a real proxied connection")
	}

	type command struct {
		name string
		run  func(*cobra.Command) error
		cmd  func() *cobra.Command
	}
	commands := []command{
		{
			name: "graph",
			run:  func(c *cobra.Command) error { return graphCmd.RunE(c, []string{"bd-1"}) },
			cmd: func() *cobra.Command {
				c := &cobra.Command{Use: "graph"}
				addMaxRowsFlag(c)
				return c
			},
		},
		{
			name: "find-duplicates",
			run:  func(c *cobra.Command) error { return runFindDuplicates(c, nil) },
			cmd: func() *cobra.Command {
				c := &cobra.Command{Use: "find-duplicates"}
				c.Flags().String("method", "mechanical", "")
				c.Flags().Float64("threshold", 0.5, "")
				c.Flags().String("status", "", "")
				c.Flags().Int("limit", 50, "")
				c.Flags().String("model", "unused", "")
				addMaxRowsFlag(c)
				return c
			},
		},
	}

	for _, command := range commands {
		for _, source := range []string{"flag", "env"} {
			t.Run(command.name+"_"+source, func(t *testing.T) {
				c := command.cmd()
				if source == "flag" {
					t.Setenv(maxRowsEnvVar, "")
					if err := c.Flags().Set(maxRowsFlagName, "5"); err != nil {
						t.Fatal(err)
					}
				} else {
					t.Setenv(maxRowsEnvVar, "5")
				}

				var err error
				stderr := captureStderr(t, func() { err = command.run(c) })

				if strings.Contains(stderr, proxiedMaxRowsRefusal) {
					t.Fatalf("proxied `bd %s` refused a cap the proxied route enforces: %q", command.name, stderr)
				}
				if !strings.Contains(stderr, proxiedProviderNotReady) {
					t.Fatalf("proxied `bd %s` did not reach the proxied route: err=%v stderr=%q", command.name, err, stderr)
				}
			})
		}
	}
}
