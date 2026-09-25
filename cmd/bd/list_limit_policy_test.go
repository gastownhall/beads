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

// GH#6069: the --limit flag help must describe the switch pinned above, in
// the switch's own order, not claim an unconditional "default 50". The whole
// usage string is pinned verbatim: a substring check would accept a clause
// scoped wider than its arm (an earlier draft's "--all is always unlimited"
// contained every needle and was still false, because an explicit --limit
// outranks --all). The cases below tie the three claims a reader is most
// likely to get wrong back to the switch, each with list.limit configured.
func TestListLimitHelpDescribesPipedBehavior(t *testing.T) {
	flag := listCmd.Flags().Lookup("limit")
	if flag == nil {
		t.Fatal("listCmd has no --limit flag registered")
	}
	const want = "Limit results (an explicit --limit always wins, 0 meaning unlimited; otherwise --all is unlimited; otherwise a configured list.limit applies; otherwise unlimited when piped, else 20 in agent mode at a terminal, else 50)"
	if flag.Usage != want {
		t.Errorf("--limit help = %q\nwant %q", flag.Usage, want)
	}

	t.Chdir(t.TempDir())
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("list.limit", 7)
	if got := config.GetValueSource("list.limit"); got == config.SourceDefault {
		t.Fatal("precondition: config.Set did not make list.limit count as configured")
	}
	for _, tc := range []struct {
		name      string
		args      []string
		want      int
		needsPipe bool
	}{
		// limitChanged is the first arm, so --all does not make an explicit
		// --limit unlimited. Holds at a terminal or a pipe alike.
		{"an explicit --limit outranks --all", []string{"--all", "--limit", "5"}, 5, false},
		// The AllFlag arm precedes listLimitConfigured and !IsTerminal, so
		// this holds at a terminal too and never needs to skip.
		{"--all is unlimited even with list.limit configured", []string{"--all"}, 0, false},
		// listLimitConfigured precedes the !IsTerminal arm. Only a piped
		// stdout can show that ordering; at a terminal the same 7 would
		// prove nothing about the piped arm.
		{"piped output takes a configured list.limit", nil, 7, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsPipe && ui.IsTerminal() {
				t.Skip("this case shows a configured list.limit outranking the piped-stdout arm, so stdout must be a pipe; go test's is, this run's is not")
			}
			in, err := gatherListInput(newListLimitCommand(t, tc.args...))
			if err != nil {
				t.Fatalf("gatherListInput(%v): %v", tc.args, err)
			}
			if in.effectiveLimit != tc.want {
				t.Errorf("effectiveLimit = %d, want %d", in.effectiveLimit, tc.want)
			}
		})
	}
}
