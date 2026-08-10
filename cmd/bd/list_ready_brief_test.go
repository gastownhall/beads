package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// newListFlagsCommand clones listCmd's flag definitions onto a fresh command,
// for newReadyFlagsCommand's reason: re-declaring a default here would be a
// second place for `bd list` to drift from itself.
func newListFlagsCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "list"}
	listCmd.Flags().VisitAll(func(f *pflag.Flag) {
		switch f.Value.Type() {
		case "bool":
			cmd.Flags().Bool(f.Name, f.DefValue == "true", f.Usage)
		case "int":
			cmd.Flags().Int(f.Name, 0, f.Usage)
		case "string":
			cmd.Flags().String(f.Name, f.DefValue, f.Usage)
		case "stringSlice":
			cmd.Flags().StringSlice(f.Name, nil, f.Usage)
		case "stringArray":
			cmd.Flags().StringArray(f.Name, nil, f.Usage)
		default:
			t.Fatalf("--%s has unhandled flag type %q", f.Name, f.Value.Type())
		}
	})
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// TestBriefFlagIsRegisteredOnBothCommands pins the two registrations and their
// defaults. Off by default is the load-bearing half: #4122 objects to default
// payload changes and #5078 records what the last one cost.
func TestBriefFlagIsRegisteredOnBothCommands(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  *cobra.Command
	}{
		{"list", listCmd},
		{"ready", readyCmd},
	} {
		t.Run(c.name, func(t *testing.T) {
			flag := c.cmd.Flags().Lookup("brief")
			if flag == nil {
				t.Fatalf("brief flag is not registered on %sCmd", c.name)
			}
			if flag.DefValue != "false" {
				t.Errorf("brief default = %q, want false: the default payload must not change", flag.DefValue)
			}
		})
	}
}

// TestListBriefReachesTheRequest pins the hop from the flag onto
// issueops.ListRequest. Both `bd list` routes hand that request to
// issueops.Reader.List verbatim (list.go and list_proxied_server.go), so this
// one assignment is what reaches them; deleting it leaves the storage suite
// green and the flag inert, which is the failure mode #5546 is about.
func TestListBriefReachesTheRequest(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		in, err := gatherListInput(newListFlagsCommand(t))
		if err != nil {
			t.Fatalf("gatherListInput: %v", err)
		}
		if in.Brief {
			t.Error("ListRequest.Brief defaulted to true")
		}
	})

	t.Run("set", func(t *testing.T) {
		in, err := gatherListInput(newListFlagsCommand(t, "--brief"))
		if err != nil {
			t.Fatalf("gatherListInput(--brief): %v", err)
		}
		if !in.Brief {
			t.Error("gatherListInput did not carry --brief onto ListRequest.Brief")
		}
	})
}

// runGatherListInput captures both streams around gatherListInput, the way
// runGatherReadyInput does for `bd ready`: these usage errors are printed by
// HandleError rather than returned as text, so a test that only inspects the
// error cannot tell WHICH conflict fired.
func runGatherListInput(t *testing.T, cmd *cobra.Command) (listInput, error, string) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldStdout, oldStderr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = wOut, wErr

	drain := func(r *os.File) <-chan string {
		done := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(r)
			done <- buf.String()
		}()
		return done
	}
	outDone, errDone := drain(rOut), drain(rErr)

	in, gatherErr := gatherListInput(cmd)

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	shown := <-outDone + <-errDone
	_ = rOut.Close()
	_ = rErr.Close()
	return in, gatherErr, shown
}

// TestListBriefIsRefusedWhereItCannotBeHonored is the `bd list` half of the
// same table. Its page routes all reach issueops.Reader.List, whose query reads
// types.IssueFilter.Lite, so unlike `bd ready` the flag works in text mode too
// and is not gated on --json. The three below leave that query:
//
//	--watch re-queries through loadWatchedIssues, whose --ready arm calls the
//	bare GetReadyWork and whose --parent arm walks the tree; neither reads Lite.
//
//	--parent with --pretty is that tree walk.
//
//	--format hands the whole issue to a caller-written template, which can print
//	any of the six dropped fields with nothing to mark the omission. The long
//	format prints one of them and says so; a template cannot be annotated.
func TestListBriefIsRefusedWhereItCannotBeHonored(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"watch", []string{"--brief", "--watch"}, "--watch cannot be combined with --brief"},
		{"format", []string{"--brief", "--format", "{{.Issue.Description}}"}, "--format cannot be combined with --brief"},
		{"parent tree", []string{"--brief", "--parent", "bd-1", "--pretty"}, "--parent with --pretty cannot be combined with --brief"},
		// --tree defaults to true, so a bare --parent in text mode is the same
		// walk without the flag being typed.
		{"parent implies the tree in text mode", []string{"--brief", "--parent", "bd-1"}, "--parent with --pretty cannot be combined with --brief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinJSONOutput(t, false)
			_, err, shown := runGatherListInput(t, newListFlagsCommand(t, tc.args...))
			if err == nil {
				t.Fatalf("gatherListInput(%v) = nil, want a usage error naming %q", tc.args, tc.want)
			}
			if !strings.Contains(shown, tc.want) {
				t.Errorf("output = %q, want it to name %q", shown, tc.want)
			}
		})
	}
}

// TestListBriefIsAcceptedOnThePageRoutes is the negative control: the modes
// above are refusals, not a blanket one. --long is here on purpose, since it is
// the text rendering that prints an omitted field and is allowed precisely
// because it marks it.
func TestListBriefIsAcceptedOnThePageRoutes(t *testing.T) {
	for _, args := range [][]string{
		{"--brief"},
		{"--brief", "--long"},
		// --flat turns the tree off, which puts --parent back on the page route.
		{"--brief", "--parent", "bd-1", "--flat"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			pinJSONOutput(t, false)
			in, err := gatherListInput(newListFlagsCommand(t, args...))
			if err != nil {
				t.Fatalf("gatherListInput(%v) = %v, want no error", args, err)
			}
			if !in.Brief {
				t.Errorf("gatherListInput(%v) dropped Brief", args)
			}
		})
	}
}

// TestReadyBriefReachesTheFilter pins the hop for `bd ready`, which does NOT
// go through issueops.Reader on either route: the direct route calls
// GetReadyWorkWithCounts(ctx, in.filter) and the proxied one calls the same
// through the unit of work. The filter is therefore the thing that has to
// carry the projection, and types.WorkFilter.Lite is where it lands.
func TestReadyBriefReachesTheFilter(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		got := runGatherReadyInput(t, newReadyFlagsCommand(t), nil)
		if got.err != nil {
			t.Fatalf("gatherReadyInput: %v", got.err)
		}
		if got.in.Brief {
			t.Error("ReadyRequest.Brief defaulted to true")
		}
		if got.in.filter.Lite {
			t.Error("WorkFilter.Lite defaulted to true")
		}
	})

	t.Run("set", func(t *testing.T) {
		// --json because the gatherer requires it for --brief; see
		// TestReadyBriefIsRefusedWhereItCannotBeHonored.
		pinJSONOutput(t, true)
		got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--brief"), nil)
		if got.err != nil {
			t.Fatalf("gatherReadyInput(--brief): %v", got.err)
		}
		if !got.in.Brief {
			t.Error("gatherReadyInput did not read --brief")
		}
		if !got.in.filter.Lite {
			t.Error("--brief did not reach WorkFilter.Lite, so both ready routes would ignore it")
		}
	})
}

// TestReadyBriefIsRefusedWhereItCannotBeHonored is the table of combinations
// that would otherwise take the flag and ignore it.
//
// types.WorkFilter.Lite reaches the driver through one query, the counts
// mega-query behind GetReadyWorkWithCounts, and `bd ready` runs that query for
// --json alone: the text routes call the bare GetReadyWork and the three
// specialized modes answer with shapes of their own. --claim is refused for a
// stronger reason, that no route can serve it at all
// (issueops.ValidateClaimNextRequest returns ErrValidation for a projected
// claim, which refetches its winning row whole).
//
// Each case is asserted on the MESSAGE, not merely on the presence of an error,
// because every one of these flags has a prior conflict of its own and a test
// that accepted any error would pass on the wrong one.
func TestReadyBriefIsRefusedWhereItCannotBeHonored(t *testing.T) {
	// --json is a root persistent flag rather than one of readyCmd's, so it is
	// set through the package global the gatherer actually reads, not in args.
	for _, tc := range []struct {
		name string
		args []string
		json bool
		want string
	}{
		{"claim", []string{"--brief", "--claim"}, true, "--claim cannot be combined with --brief"},
		{"gated", []string{"--brief", "--gated"}, true, "--gated cannot be combined with --brief"},
		{"mol", []string{"--brief", "--mol", "bd-1"}, true, "--mol cannot be combined with --brief"},
		{"explain", []string{"--brief", "--explain"}, true, "--explain cannot be combined with --brief"},
		{"without --json", []string{"--brief"}, false, "--brief requires --json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinJSONOutput(t, tc.json)

			got := runGatherReadyInput(t, newReadyFlagsCommand(t, tc.args...), nil)
			if got.err == nil {
				t.Fatalf("gatherReadyInput(%v) = nil, want a usage error", tc.args)
			}
			// HandleErrorRespectJSON puts the message on stdout as JSON when
			// --json is on and on stderr as text otherwise, so both are read.
			if shown := got.stdout + got.stderr; !strings.Contains(shown, tc.want) {
				t.Errorf("output = %q, want it to name %q", shown, tc.want)
			}
		})
	}
}

// TestReadyCmdRefusesBriefBeforeItDispatches drives readyCmd's RunE, which the
// table above does not reach, and it is the half that was missing.
//
// --gated, --mol and --explain are dispatched by RunE BEFORE gatherReadyInput
// runs, so the gatherer's refusals guard the proxied route only. When those
// three checks were copies written into each dispatch branch, deleting one left
// `bd ready --gated --brief` silently ignoring the flag on the direct route
// with the whole package still green. They are now one hoisted call to
// briefModeConflict, and this is what fails if that call is removed.
//
// RunE is reachable in a test because the check sits above every store access:
// it returns before the proxied branch, the offset check and the mode dispatch.
func TestReadyCmdRefusesBriefBeforeItDispatches(t *testing.T) {
	setFlag := func(t *testing.T, name, value string) {
		t.Helper()
		if err := readyCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set %s=%s: %v", name, value, err)
		}
		// pflag.Set leaves Changed true, which outlives the value reset and
		// would contaminate later tests in the package.
		t.Cleanup(func() {
			def := readyCmd.Flags().Lookup(name).DefValue
			_ = readyCmd.Flags().Set(name, def)
			readyCmd.Flags().Lookup(name).Changed = false
		})
	}

	for _, tc := range []struct {
		name string
		flag string
		val  string
		want string
	}{
		{"gated", "gated", "true", "--gated cannot be combined with --brief"},
		{"mol", "mol", "bd-1", "--mol cannot be combined with --brief"},
		{"explain", "explain", "true", "--explain cannot be combined with --brief"},
		{"claim", "claim", "true", "--claim cannot be combined with --brief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinJSONOutput(t, true)
			setFlag(t, "brief", "true")
			setFlag(t, tc.flag, tc.val)

			stdioMutex.Lock()
			defer stdioMutex.Unlock()

			oldStdout, oldStderr := os.Stdout, os.Stderr
			rOut, wOut, _ := os.Pipe()
			rErr, wErr, _ := os.Pipe()
			os.Stdout, os.Stderr = wOut, wErr

			drain := func(r *os.File) <-chan string {
				done := make(chan string, 1)
				go func() {
					var buf bytes.Buffer
					_, _ = buf.ReadFrom(r)
					done <- buf.String()
				}()
				return done
			}
			outDone, errDone := drain(rOut), drain(rErr)

			err := readyCmd.RunE(readyCmd, nil)

			wOut.Close()
			wErr.Close()
			os.Stdout, os.Stderr = oldStdout, oldStderr
			shown := <-outDone + <-errDone
			_ = rOut.Close()
			_ = rErr.Close()

			if err == nil {
				t.Fatalf("readyCmd.RunE(--brief --%s) = nil, want a usage error naming %q", tc.flag, tc.want)
			}
			if !strings.Contains(shown, tc.want) {
				t.Errorf("output = %q, want it to name %q", shown, tc.want)
			}
		})
	}
}

// TestReadyBriefWithJSONIsAccepted is the negative control for the table above:
// with none of those modes set, the flag must get through. Without this, a
// refusal that fired unconditionally would leave every case there green.
func TestReadyBriefWithJSONIsAccepted(t *testing.T) {
	pinJSONOutput(t, true)

	got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--brief"), nil)
	if got.err != nil {
		t.Fatalf("gatherReadyInput(--brief --json) = %v, want no error", got.err)
	}
	if !got.in.filter.Lite {
		t.Error("--brief with --json did not reach WorkFilter.Lite")
	}
}

// TestReadyRoleRequestCarriesBrief is the other side of the refusal above, and
// the reason the refusal lives in the gatherer rather than here. This helper
// builds the request for BOTH roles `bd ready` talks to, and the two want
// opposite things: ReadyClaimer refuses a projection, ReadyCounter needs one,
// because the unit-of-work counter sizes the ready set by running the unbounded
// page (uow/ready_counter.go). Clearing Brief here to appease the claim would
// make `bd ready --brief` hydrate every heavy column of the whole ready set to
// fetch the total printed beside its page.
func TestReadyRoleRequestCarriesBrief(t *testing.T) {
	got := readyRoleRequest(readyInput{ReadyRequest: issueops.ReadyRequest{Brief: true}})
	if !got.Brief {
		t.Error("readyRoleRequest dropped Brief, so the ready count would run unprojected")
	}
	if got.Limit != nil || got.Offset != 0 {
		t.Errorf("readyRoleRequest stopped clearing the page: Limit=%v Offset=%d", got.Limit, got.Offset)
	}
}

// TestFormatIssueLongMarksAProjectedRow pins the one text rendering in the tree
// that prints a field --brief drops. Without the marker a projected listing is
// byte-identical to one whose issues genuinely have no description, which is
// the ambiguity the projection is otherwise careful to avoid (it is why the row
// carries IsLitePartial at all rather than just arriving blank).
//
// The marker keys off the ROW, so it is asserted on the row, not through a
// flag: a row that arrived projected reads the same whichever door set it.
func TestFormatIssueLongMarksAProjectedRow(t *testing.T) {
	render := func(issue *types.Issue) string {
		var buf strings.Builder
		formatIssueLong(&buf, issue, nil, false)
		return buf.String()
	}

	t.Run("projected row says so", func(t *testing.T) {
		out := render(&types.Issue{
			ID: "be-abc", Title: "t", Status: types.StatusOpen,
			IssueType: types.TypeTask, IsLitePartial: true,
		})
		if !strings.Contains(out, "Description: (omitted by --brief)") {
			t.Errorf("long format did not mark the projection:\n%s", out)
		}
	})

	t.Run("whole row with a description prints it", func(t *testing.T) {
		out := render(&types.Issue{
			ID: "be-abc", Title: "t", Status: types.StatusOpen,
			IssueType: types.TypeTask, Description: "the body",
		})
		if strings.Contains(out, "omitted by --brief") {
			t.Errorf("long format marked an unprojected row:\n%s", out)
		}
		if !strings.Contains(out, "the body") {
			t.Errorf("long format dropped the description:\n%s", out)
		}
	})

	t.Run("whole row with no description stays silent", func(t *testing.T) {
		out := render(&types.Issue{
			ID: "be-abc", Title: "t", Status: types.StatusOpen,
			IssueType: types.TypeTask,
		})
		if strings.Contains(out, "Description") {
			t.Errorf("long format invented a Description line for a textless row:\n%s", out)
		}
	})
}
