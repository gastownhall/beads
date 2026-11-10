package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

// newQueryLimitCommand registers the flags gatherQueryInput reads, with
// queryCmd's own --limit default.
func newQueryLimitCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "query"}
	cmd.Flags().IntP("limit", "n", workapi.DefaultQueryLimit, "")
	cmd.Flags().Int("offset", 0, "")
	cmd.Flags().BoolP("all", "a", false, "")
	cmd.Flags().Bool("long", false, "")
	cmd.Flags().String("sort", "", "")
	cmd.Flags().BoolP("reverse", "r", false, "")
	cmd.Flags().Bool("parse-only", false, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// TestQueryLimitPolicyMatchesList pins GH#6229: `bd query` read --limit
// straight through, so `bd query ... | consumer` silently stopped at
// workapi.DefaultQueryLimit rows where `bd list | consumer` got every row
// (GH#4094). An unflagged query now resolves through the same policy as list.
func TestQueryLimitPolicyMatchesList(t *testing.T) {
	if ui.IsTerminal() {
		t.Skip("this test asserts the piped-stdout branch; go test's stdout is a pipe, but this run's is not")
	}
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"an explicit limit wins when piped", []string{"--limit", "7"}, 7},
		{"an explicit default-sized limit is still honored when piped", []string{"--limit", "50"}, 50},
		{"an explicit zero stays unlimited", []string{"--limit", "0"}, 0},
		{"piped stdout is unlimited, not the query default", nil, 0},
		// query's --all means "include closed", not "no limit"; piping is
		// what lifts the cap here.
		{"--all does not change the piped policy", []string{"--all"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := gatherQueryInput(newQueryLimitCommand(t, tc.args...), []string{"status=open"})
			if err != nil {
				t.Fatalf("gatherQueryInput(%v): %v", tc.args, err)
			}
			if in.limit != tc.want {
				t.Errorf("limit = %d, want %d", in.limit, tc.want)
			}
			if in.request.Limit == nil {
				t.Fatal("QueryRequest.Limit is nil; `bd query` resolves its own limit before the request exists")
			}
			if *in.request.Limit != tc.want {
				t.Errorf("QueryRequest.Limit = %d, want %d", *in.request.Limit, tc.want)
			}
		})
	}
}

// TestUnflaggedLimitBranches covers the terminal and agent-mode branches the
// piped `go test` process cannot reach through gatherQueryInput or
// gatherListInput, for both commands' defaults.
func TestUnflaggedLimitBranches(t *testing.T) {
	for _, fallback := range []int{workapi.DefaultListLimit, workapi.DefaultQueryLimit} {
		for _, tc := range []struct {
			name     string
			terminal bool
			agent    bool
			want     int
		}{
			{"terminal gets the command default", true, false, fallback},
			{"agent mode on a terminal is compact", true, true, agentModeLimit},
			{"piped stdout is unlimited", false, false, 0},
			// Piping outranks agent mode, as it always has for bd list.
			{"piped stdout is unlimited in agent mode too", false, true, 0},
		} {
			if got := resolveUnflaggedLimit(fallback, tc.terminal, tc.agent); got != tc.want {
				t.Errorf("%s: resolveUnflaggedLimit(%d, terminal=%v, agent=%v) = %d, want %d",
					tc.name, fallback, tc.terminal, tc.agent, got, tc.want)
			}
		}
	}
	if agentModeLimit != 20 {
		t.Errorf("agentModeLimit = %d, want 20 (bd list's historical agent-mode cap)", agentModeLimit)
	}
}
