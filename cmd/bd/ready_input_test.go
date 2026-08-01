package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// newReadyFlagsCommand clones readyCmd's flag definitions onto a fresh command
// and parses args against them, so each case starts from pristine defaults.
// Cloning beats re-declaring the flags here: a hand-copied default would be a
// second place for `bd ready` to drift, which is the very thing gatherReadyInput
// exists to stop. The clone is by value, not AddFlagSet, so a flag set here
// cannot leak into readyCmd.
func newReadyFlagsCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "ready"}
	readyCmd.Flags().VisitAll(func(f *pflag.Flag) {
		switch f.Value.Type() {
		case "bool":
			cmd.Flags().Bool(f.Name, f.DefValue == "true", f.Usage)
		case "int":
			n, err := strconv.Atoi(f.DefValue)
			if err != nil {
				t.Fatalf("--%s has non-integer default %q: %v", f.Name, f.DefValue, err)
			}
			cmd.Flags().Int(f.Name, n, f.Usage)
		case "string":
			cmd.Flags().String(f.Name, f.DefValue, f.Usage)
		case "stringSlice":
			if f.DefValue != "[]" {
				t.Fatalf("--%s has a non-empty slice default %q, which this clone does not reproduce", f.Name, f.DefValue)
			}
			cmd.Flags().StringSlice(f.Name, nil, f.Usage)
		case "stringArray":
			if f.DefValue != "[]" {
				t.Fatalf("--%s has a non-empty array default %q, which this clone does not reproduce", f.Name, f.DefValue)
			}
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

// gatherOutcome is everything a `bd ready` invocation would have shown the user
// before the query runs: the input, the error, and both streams. Usage errors
// go to stderr, or to stdout as a JSON object under --json, and the cap warning
// only ever goes to stderr - so a test that pins where a message goes, or in
// what order two of them appear, has to watch both.
type gatherOutcome struct {
	in     readyInput
	err    error
	stdout string
	stderr string
}

func runGatherReadyInput(t *testing.T, cmd *cobra.Command, resolveCap func(*cobra.Command) (int, string, error)) gatherOutcome {
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

	out := gatherOutcome{}
	out.in, out.err = gatherReadyInput(cmd, resolveCap)

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	out.stdout, out.stderr = <-outDone, <-errDone
	_ = rOut.Close()
	_ = rErr.Close()
	return out
}

// pinJSONOutput fixes the package-level --json state for the duration of a
// test and puts it back afterwards.
//
// gatherReadyInput reports its usage errors through HandleErrorRespectJSON, so
// this global alone decides whether they land as text on stderr or as a JSON
// object on stdout. jsonOutput is not per-test state: several cmd/bd tests set
// it and never restore it (saveAndRestoreGlobals, which a number of them use,
// does not cover it), so a test that reads it instead of setting it passes
// under a narrow -run and fails in a full-package run depending on what ran
// first. Every test below that asserts on where a message went pins it.
func pinJSONOutput(t *testing.T, on bool) {
	t.Helper()
	restore := jsonOutput
	jsonOutput = on
	t.Cleanup(func() { jsonOutput = restore })
}

// configureDirectoryLabel points directory.labels at the test's own working
// directory: GetDirectoryLabels resolves against the cwd, so the test has to
// own both ends.
func configureDirectoryLabel(t *testing.T, label string) {
	t.Helper()

	// A leaf name, not the whole path: GetDirectoryLabels suffix-matches the
	// pattern against the cwd, and os.Getwd may resolve symlinks on the way.
	const leaf = "readydirlabel"
	dir := filepath.Join(t.TempDir(), leaf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	t.Chdir(dir)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("directory.labels", map[string]string{leaf: label})

	if got := config.GetDirectoryLabels(); !slices.Equal(got, []string{label}) {
		t.Fatalf("precondition: GetDirectoryLabels() = %q, want %q", got, []string{label})
	}
}

// TestGatherReadyInputResolvesCapWhereTheDirectBuilderDid pins where in the
// sequence of checks --max-rows / BEADS_MAX_ROWS is resolved. That is not
// decoration: resolveMaxRowsEnvOnly warns about a malformed BEADS_MAX_ROWS as a
// side effect of resolving, so moving the resolution past another check
// silently drops the warning for every command line that trips that check.
func TestGatherReadyInputResolvesCapWhereTheDirectBuilderDid(t *testing.T) {
	// Text mode, explicitly: the ordering this pins is an ordering WITHIN
	// stderr, and under --json the usage errors leave stderr entirely for a
	// JSON object on stdout while the cap warning stays behind. Comparing
	// positions across two streams would not be the same assertion, so the
	// test picks the mode it is about instead of inheriting one.
	pinJSONOutput(t, false)

	t.Run("malformed_env_still_warns_when_a_later_check_aborts", func(t *testing.T) {
		t.Setenv(maxRowsEnvVar, "bogus")

		got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--sort", "bogus"), resolveMaxRows)
		if got.err == nil {
			t.Fatal("gatherReadyInput(--sort bogus) = nil, want the sort-policy error")
		}
		warning := strings.Index(got.stderr, "is not a non-negative integer")
		sortErr := strings.Index(got.stderr, "invalid sort policy")
		switch {
		case warning < 0:
			t.Errorf("BEADS_MAX_ROWS warning missing; stderr was:\n%s", got.stderr)
		case sortErr < 0:
			t.Errorf("sort-policy error missing; stderr was:\n%s", got.stderr)
		case warning > sortErr:
			t.Errorf("the warning must come first; stderr was:\n%s", got.stderr)
		}
	})

	t.Run("bad_cap_flag_is_reported_before_the_sort_error", func(t *testing.T) {
		got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--max-rows", "-1", "--sort", "bogus"), resolveMaxRows)
		if got.err == nil {
			t.Fatal("gatherReadyInput(--max-rows -1 --sort bogus) = nil, want an error")
		}
		if !strings.Contains(got.stderr, "--max-rows must be non-negative") {
			t.Errorf("expected the --max-rows error, got:\n%s", got.stderr)
		}
	})

	// The other half of the ordering: the guards the direct builder ran before
	// it resolved the cap still win.
	t.Run("bad_cap_flag_loses_to_the_earlier_guards", func(t *testing.T) {
		cases := []struct {
			name string
			args []string
			want string
		}{
			{"mol_type", []string{"--max-rows", "-1", "--mol-type", "bogus"}, "invalid mol-type"},
			{"claim_assignee", []string{"--max-rows", "-1", "--claim", "--assignee", "alice"}, "--claim cannot be combined with --assignee"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				got := runGatherReadyInput(t, newReadyFlagsCommand(t, c.args...), resolveMaxRows)
				if got.err == nil {
					t.Fatalf("gatherReadyInput(%v) = nil, want an error", c.args)
				}
				if !strings.Contains(got.stderr, c.want) {
					t.Errorf("expected %q, got:\n%s", c.want, got.stderr)
				}
				if strings.Contains(got.stderr, "--max-rows must be non-negative") {
					t.Errorf("the cap was resolved too early; stderr was:\n%s", got.stderr)
				}
			})
		}
	})

	t.Run("resolved_cap_lands_on_the_filter", func(t *testing.T) {
		got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--max-rows", "5"), resolveMaxRows)
		if got.err != nil {
			t.Fatalf("gatherReadyInput: %v", got.err)
		}
		if want := "--" + maxRowsFlagName; got.in.filter.MaxRows != 5 || got.in.filter.MaxRowsSource != want {
			t.Errorf("filter cap = (%d, %q), want (5, %q)", got.in.filter.MaxRows, got.in.filter.MaxRowsSource, want)
		}
	})

	// The proxied route passes no resolver: it cannot enforce a cap, and its
	// RunE has already resolved the flag to reject it.
	t.Run("no_resolver_leaves_the_filter_uncapped", func(t *testing.T) {
		t.Setenv(maxRowsEnvVar, "5")

		got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--max-rows", "5"), nil)
		if got.err != nil {
			t.Fatalf("gatherReadyInput: %v", got.err)
		}
		if got.in.filter.MaxRows != 0 || got.in.filter.MaxRowsSource != "" {
			t.Errorf("filter cap = (%d, %q), want it unset", got.in.filter.MaxRows, got.in.filter.MaxRowsSource)
		}
		if got.stderr != "" {
			t.Errorf("no resolver should mean no cap output, got:\n%s", got.stderr)
		}
	})
}

// TestGatherReadyInputUsageErrorsRespectJSON pins the one behavior change in
// the collapse that the ready-filter golden structurally cannot see: the golden
// was recorded with jsonOutput false, where HandleError and
// HandleErrorRespectJSON print the same stderr line, so the divergence table in
// internal/workapi has no entry for it and no entry it could have.
//
// The two pre-collapse builders disagreed on which helper reported these five
// usage errors: the direct route used the RespectJSON variant, the proxied one
// did not. One shared gatherer can only have one, and it kept the direct
// route's - which leaves the direct route byte-identical and makes the proxied
// route emit a JSON error object on stdout for a --json caller, where it used
// to print "Error: ..." to stderr. That is the observable change, and this is
// what would fail if it were reverted or drifted back.
func TestGatherReadyInputUsageErrorsRespectJSON(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"sort_policy", []string{"--sort", "bogus"}, "invalid sort policy"},
		{"mol_type", []string{"--mol-type", "bogus"}, "invalid mol-type"},
		{"metadata_field_syntax", []string{"--metadata-field", "team"}, "invalid --metadata-field"},
		{"metadata_field_key", []string{"--metadata-field", "bad$key=x"}, "invalid --metadata-field key"},
		{"has_metadata_key", []string{"--has-metadata-key", "bad$key"}, "invalid --has-metadata-key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pinJSONOutput(t, true)

			got := runGatherReadyInput(t, newReadyFlagsCommand(t, c.args...), nil)
			if got.err == nil {
				t.Fatalf("gatherReadyInput(%v) = nil, want an error", c.args)
			}
			if got.stderr != "" {
				t.Errorf("under --json nothing should reach stderr, got:\n%s", got.stderr)
			}
			if msg := jsonErrorMessage(t, got.stdout); !strings.Contains(msg, c.want) {
				t.Errorf("stdout error = %q, want it to contain %q", msg, c.want)
			}
		})
	}
}

// jsonErrorMessage pulls the message out of whichever error shape
// buildJSONError produced - bare or BD_JSON_ENVELOPE-wrapped.
func jsonErrorMessage(t *testing.T, stdout string) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, stdout)
	}
	if data, ok := payload["data"].(map[string]any); ok {
		payload = data
	}
	msg, ok := payload["error"].(string)
	if !ok {
		t.Fatalf("no string \"error\" key in JSON error object:\n%s", stdout)
	}
	return msg
}

// TestGatherReadyInputIgnoresNegativeOffset pins the direct route's oldest
// answer to `bd ready --offset -1`: print ready work. Its RunE rejects
// --offset > 0 as proxied-only and never looked at the flag again, so a
// negative value has always been a no-op there. The shared gatherer must not
// turn it into an exit-1 usage error; the proxied route, which is the only one
// that pages, rejects it in its own RunE.
func TestGatherReadyInputIgnoresNegativeOffset(t *testing.T) {
	got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--offset", "-1"), nil)
	if got.err != nil {
		t.Fatalf("gatherReadyInput(--offset -1) = %v, want no error", got.err)
	}
	if got.in.filter.Offset != 0 {
		t.Errorf("filter.Offset = %d, want 0 (a negative offset must not reach storage)", got.in.filter.Offset)
	}
}

// TestGatherReadyInputKeepsDirectoryLabelVerbatim pins GH#541's label against
// the collapse into workapi. The configured label is not user input: `bd ready`
// has always put it on the filter exactly as configured, so it must not be
// routed through issueops.ReadyRequest, whose label sets BuildReadyFilter
// normalizes.
// The label below is one NormalizeLabels would visibly change, which is what
// makes this a test and not a tautology.
func TestGatherReadyInputKeepsDirectoryLabelVerbatim(t *testing.T) {
	const configured = "  scope:web  "
	configureDirectoryLabel(t, configured)

	got := runGatherReadyInput(t, newReadyFlagsCommand(t), nil)
	if got.err != nil {
		t.Fatalf("gatherReadyInput: %v", got.err)
	}
	if want := []string{configured}; !slices.Equal(got.in.filter.LabelsAny, want) {
		t.Errorf("filter.LabelsAny = %q, want %q (the configured value, unnormalized)", got.in.filter.LabelsAny, want)
	}
}

// TestGatherReadyInputDirectoryLabelDefaultsOnlyWhenNoLabelsGiven pins the two
// halves of the default's gate: an explicit label suppresses it, and a label
// that normalizes away does not.
func TestGatherReadyInputDirectoryLabelDefaultsOnlyWhenNoLabelsGiven(t *testing.T) {
	const configured = "scope:web"

	tests := []struct {
		name          string
		args          []string
		wantLabelsAny []string
	}{
		{"explicit_label_suppresses_default", []string{"--label", "chosen"}, nil},
		{"explicit_label_any_wins_over_default", []string{"--label-any", "chosen"}, []string{"chosen"}},
		{"blank_label_does_not_suppress_default", []string{"--label", "  "}, []string{configured}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configureDirectoryLabel(t, configured)

			got := runGatherReadyInput(t, newReadyFlagsCommand(t, tc.args...), nil)
			if got.err != nil {
				t.Fatalf("gatherReadyInput: %v", got.err)
			}
			if labels := got.in.filter.LabelsAny; len(labels) != 0 || len(tc.wantLabelsAny) != 0 {
				if !slices.Equal(labels, tc.wantLabelsAny) {
					t.Errorf("filter.LabelsAny = %q, want %q", labels, tc.wantLabelsAny)
				}
			}
		})
	}
}

// TestReadyExplainFilterDerivesTheReadyDefault pins the third copy of the ready
// default out of existence (bd-3fs.3). Both --explain routes used to inline
// WorkFilter{Status: StatusOpen, SortPolicy: SortPolicyPriority} beside the two
// builders bd-ehi had already collapsed into workapi.BuildReadyFilter, so a
// change to what "ready" means would have moved the listing and left the
// diagnostic explaining a different set.
//
// The limit is the one field --explain sets for itself, and it is asserted
// rather than derived: the listing takes workapi.DefaultReadyLimit, and an
// --explain that inherited it would explain the first 100 ready issues of a
// larger graph while reading as though it had explained the whole thing.
func TestReadyExplainFilterDerivesTheReadyDefault(t *testing.T) {
	got, err := readyExplainFilter()
	if err != nil {
		t.Fatalf("readyExplainFilter: %v", err)
	}

	want, err := workapi.BuildReadyFilter(issueops.ReadyRequest{Sort: string(types.SortPolicyPriority)})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	if want.Limit != workapi.DefaultReadyLimit {
		t.Fatalf("listing default limit = %d, want workapi.DefaultReadyLimit (%d): this test is measuring against the wrong baseline", want.Limit, workapi.DefaultReadyLimit)
	}
	want.Limit = 0

	if !reflect.DeepEqual(got, want) {
		t.Errorf("explain filter = %+v\nwant the listing default, unlimited = %+v", got, want)
	}
}
