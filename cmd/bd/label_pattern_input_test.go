package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// emptyLabelPatternCases is the refusal table `bd list` and `bd ready` share:
// every spelling of a supplied-but-empty pattern flag that used to be read as
// "no pattern", silently dropping the label predicate from the query (GH#6636).
// A whitespace-only --label-regex is NOT here, deliberately: it is a real
// unanchored substring predicate against labels that may contain spaces, and
// only the exact empty string is refused. TestGatherInputsKeepUsableLabelPatterns
// pins that half.
var emptyLabelPatternCases = []struct {
	name string
	args []string
	want string
}{
	{"empty glob", []string{"--label-pattern", ""}, "--label-pattern was supplied but contains no pattern"},
	{"whitespace glob", []string{"--label-pattern", "  "}, "--label-pattern was supplied but contains no pattern"},
	{"tab glob", []string{"--label-pattern", "\t"}, "--label-pattern was supplied but contains no pattern"},
	{"empty regex", []string{"--label-regex", ""}, "--label-regex was supplied but contains no pattern"},
}

// runGatherListInputSplit is runGatherListInput (list_ready_brief_test.go)
// with the two streams kept apart. The refusals under test are reported
// through HandleErrorRespectJSON, which puts the message on stderr as text or
// on stdout as a JSON object depending on the package's --json state, so a
// test that pins WHICH stream carried it cannot work from the combined
// capture.
func runGatherListInputSplit(t *testing.T, cmd *cobra.Command) (listInput, error, string, string) {
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
	stdout, stderr := <-outDone, <-errDone
	_ = rOut.Close()
	_ = rErr.Close()
	return in, gatherErr, stdout, stderr
}

// assertLabelPatternRefusal checks the shape every pattern refusal shares:
// exit code 1, the flag-specific message on the one stream the text/JSON
// convention says carries it, and nothing on the other.
func assertLabelPatternRefusal(t *testing.T, err error, stdout, stderr, want string, jsonOut bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("input gathering returned nil error, want the refusal %q", want)
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.Code != 1 {
		t.Errorf("error = %v, want an exitError with code 1", err)
	}
	if jsonOut {
		if stderr != "" {
			t.Errorf("under --json the refusal must not reach stderr, got:\n%s", stderr)
		}
		if msg := jsonErrorMessage(t, stdout); !strings.Contains(msg, want) {
			t.Errorf("stdout error = %q, want it to contain %q", msg, want)
		}
	} else {
		if stdout != "" {
			t.Errorf("in text mode the refusal must not reach stdout, got:\n%s", stdout)
		}
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr, want)
		}
		if n := strings.Count(stderr, want); n != 1 {
			t.Errorf("refusal message appeared %d times, want exactly 1 (no double print)", n)
		}
	}
}

// TestGatherListInputRejectsEmptyLabelPatterns pins GH#6636 for `bd list`:
// gatherListInput runs ahead of the direct and proxied route split (list.go
// and list_proxied_server.go both hand its request to issueops.Reader), so
// refusing there covers both routes. Text, --json, and the command's own
// --format json are covered separately because the last one promotes the
// package global itself (GH#6278), which is a third way the JSON branch can
// be reached.
func TestGatherListInputRejectsEmptyLabelPatterns(t *testing.T) {
	for _, tc := range emptyLabelPatternCases {
		t.Run(tc.name+"/text", func(t *testing.T) {
			pinJSONOutput(t, false)
			_, err, stdout, stderr := runGatherListInputSplit(t, newListFlagsCommand(t, tc.args...))
			assertLabelPatternRefusal(t, err, stdout, stderr, tc.want, false)
		})
		t.Run(tc.name+"/json", func(t *testing.T) {
			pinJSONOutput(t, true)
			_, err, stdout, stderr := runGatherListInputSplit(t, newListFlagsCommand(t, tc.args...))
			assertLabelPatternRefusal(t, err, stdout, stderr, tc.want, true)
		})
		t.Run(tc.name+"/format_json", func(t *testing.T) {
			pinJSONOutput(t, false)
			args := append(append([]string(nil), tc.args...), "--format", "json")
			_, err, stdout, stderr := runGatherListInputSplit(t, newListFlagsCommand(t, args...))
			assertLabelPatternRefusal(t, err, stdout, stderr, tc.want, true)
		})
	}
}

// TestGatherReadyInputRejectsEmptyLabelPatterns is the `bd ready` half of the
// same refusal. Both ready routes call gatherReadyInput (ready.go's direct
// route and ready_proxied_server.go), so one check in the gatherer covers the
// two doors; the special modes that dispatch before the gatherer
// (--gated/--mol/--explain) never read these listing flags, so they are not
// promised anything here.
func TestGatherReadyInputRejectsEmptyLabelPatterns(t *testing.T) {
	for _, tc := range emptyLabelPatternCases {
		t.Run(tc.name+"/text", func(t *testing.T) {
			pinJSONOutput(t, false)
			got := runGatherReadyInput(t, newReadyFlagsCommand(t, tc.args...), nil)
			assertLabelPatternRefusal(t, got.err, got.stdout, got.stderr, tc.want, false)
		})
		t.Run(tc.name+"/json", func(t *testing.T) {
			pinJSONOutput(t, true)
			got := runGatherReadyInput(t, newReadyFlagsCommand(t, tc.args...), nil)
			assertLabelPatternRefusal(t, got.err, got.stdout, got.stderr, tc.want, true)
		})
	}
}

// usableLabelPatternCases is the acceptance half of GH#6636's pattern fix: the
// spellings that must keep working exactly as before. Omitting both flags
// still means no pattern filter; every usable value — padded globs, anchored
// regexes, regexes made entirely of whitespace, regexes that do not even
// compile — passes through byte-for-byte. The malformed regex is here to pin
// that the new input check adds no syntax validation: the pattern reaches the
// engine unvalidated today (an unanchored SQL REGEXP), and refusing it here
// would be a new behavior nobody asked for.
var usableLabelPatternCases = []struct {
	name      string
	args      []string
	wantGlob  string
	wantRegex string
}{
	{"omitted", nil, "", ""},
	{"glob", []string{"--label-pattern", "tech-*"}, "tech-*", ""},
	{"anchored regex", []string{"--label-regex", "^tech-(debt|legacy)$"}, "", "^tech-(debt|legacy)$"},
	{"literal space regex", []string{"--label-regex", " "}, "", " "},
	{"literal tab regex", []string{"--label-regex", "\t"}, "", "\t"},
	{"padded glob", []string{"--label-pattern", " tech-* "}, " tech-* ", ""},
	{"padded regex", []string{"--label-regex", " tech "}, "", " tech "},
	{"malformed regex", []string{"--label-regex", "["}, "", "["},
}

// TestGatherInputsKeepUsableLabelPatterns is the negative control for the
// refusals above: the fix must refuse only unusable input, never rewrite or
// drop a usable pattern. A whitespace-only regex is the row that draws the
// line — labels may contain spaces (label_whitespace_warn.go), so it is a
// real substring predicate, while the same value as a glob is refused because
// normalized labels are trimmed and it could never match one.
func TestGatherInputsKeepUsableLabelPatterns(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		for _, tc := range usableLabelPatternCases {
			t.Run(tc.name, func(t *testing.T) {
				pinJSONOutput(t, false)
				in, err := gatherListInput(newListFlagsCommand(t, tc.args...))
				if err != nil {
					t.Fatalf("gatherListInput(%v) = %v, want no error", tc.args, err)
				}
				if in.LabelPattern != tc.wantGlob {
					t.Errorf("LabelPattern = %q, want %q (byte-for-byte)", in.LabelPattern, tc.wantGlob)
				}
				if in.LabelRegex != tc.wantRegex {
					t.Errorf("LabelRegex = %q, want %q (byte-for-byte)", in.LabelRegex, tc.wantRegex)
				}
			})
		}
	})

	t.Run("ready", func(t *testing.T) {
		// Both resolver shapes the gatherer is called with: the direct
		// route's resolveMaxRows and the proxied route's nil.
		for _, resolver := range []struct {
			name string
			f    func(*cobra.Command) (int, string, error)
		}{
			{"nil resolver", nil},
			{"direct route resolver", resolveMaxRows},
		} {
			t.Run(resolver.name, func(t *testing.T) {
				for _, tc := range usableLabelPatternCases {
					t.Run(tc.name, func(t *testing.T) {
						pinJSONOutput(t, false)
						got := runGatherReadyInput(t, newReadyFlagsCommand(t, tc.args...), resolver.f)
						if got.err != nil {
							t.Fatalf("gatherReadyInput(%v) = %v, want no error", tc.args, got.err)
						}
						if got.in.LabelPattern != tc.wantGlob {
							t.Errorf("request LabelPattern = %q, want %q (byte-for-byte)", got.in.LabelPattern, tc.wantGlob)
						}
						if got.in.LabelRegex != tc.wantRegex {
							t.Errorf("request LabelRegex = %q, want %q (byte-for-byte)", got.in.LabelRegex, tc.wantRegex)
						}
						// The filter is what the query consumes; it must
						// carry the same pattern the request does.
						if got.in.filter.LabelPattern != tc.wantGlob {
							t.Errorf("filter LabelPattern = %q, want %q", got.in.filter.LabelPattern, tc.wantGlob)
						}
						if got.in.filter.LabelRegex != tc.wantRegex {
							t.Errorf("filter LabelRegex = %q, want %q", got.in.filter.LabelRegex, tc.wantRegex)
						}
					})
				}
			})
		}
	})
}
