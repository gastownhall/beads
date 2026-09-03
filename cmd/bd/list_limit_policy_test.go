package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

// newListLimitCommand registers the flags the limit policy reads, with
// listCmd's own --limit default so a change to that registration shows up here
// rather than being reproduced by hand.
func newListLimitCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().IntP("limit", "n", workapi.DefaultListLimit, "")
	cmd.Flags().Bool("all", false, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// TestListLimitPolicyIsResolvedBeforeTheRequest pins the third structural
// divergence between `bd list` and GET /v0/beads/issues, the one the parity
// oracle cannot see because every comparison there passes an explicit limit.
//
// The endpoint always defaults to workapi.DefaultListLimit. `bd list` resolves
// its own policy first and puts the RESULT on the request, so the shared
// default that both surfaces are supposed to read is unreachable from the CLI:
// a client swapping `bd list --json | ...` (piped, therefore unlimited) for the
// HTTP call silently loses every row past 50.
//
// That is deliberate — GH#4094 made piped output stop truncating — but it is a
// divergence, and an undocumented, unpinned divergence is how a client learns
// about it from a missing row. Each branch below is a policy decision that a
// reader of the switch would otherwise have to take on trust.
func TestListLimitPolicyIsResolvedBeforeTheRequest(t *testing.T) {
	if ui.IsTerminal() {
		t.Skip("this test asserts the piped-stdout branch; go test's stdout is a pipe, but this run's is not")
	}
	// A real config with its real defaults, in an empty directory. Without it
	// `config.GetInt("list.limit")` answers 0 from an uninitialized viper and
	// EVERY case below passes for the wrong reason — including the piped one,
	// whose whole point is that it overrides a nonzero default.
	t.Chdir(t.TempDir())
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if got := config.GetInt("list.limit"); got != workapi.DefaultListLimit {
		t.Fatalf("precondition: config list.limit = %d, want the shared default %d", got, workapi.DefaultListLimit)
	}

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		// An explicit --limit is the caller's own number, verbatim, including
		// an explicit 0 (unlimited) that must not be confused with "unset".
		{"an explicit limit wins", []string{"--limit", "7"}, 7},
		{"an explicit zero stays unlimited", []string{"--limit", "0"}, 0},
		// --all is "show me everything", which is a limit decision too.
		{"--all is unlimited", []string{"--all"}, 0},
		// The branch that makes the divergence: no --limit, stdout is a pipe.
		// NOT workapi.DefaultListLimit, which is what the endpoint would use.
		{"piped stdout is unlimited, not the shared default", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := gatherListInput(newListLimitCommand(t, tc.args...))
			if err != nil {
				t.Fatalf("gatherListInput(%v): %v", tc.args, err)
			}
			if in.effectiveLimit != tc.want {
				t.Errorf("effectiveLimit = %d, want %d", in.effectiveLimit, tc.want)
			}
			// The request carries the RESOLVED number, never nil — which is
			// why workapi.PageLimit's shared-default fallback is dead code
			// from this front door.
			if in.Limit == nil {
				t.Fatal("ListRequest.Limit is nil; `bd list` always resolves its own limit before the request exists")
			}
			if *in.Limit != tc.want {
				t.Errorf("ListRequest.Limit = %d, want %d", *in.Limit, tc.want)
			}
		})
	}
}
