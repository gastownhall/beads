package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// listGatherOutcome is gatherOutcome for `bd list`.
type listGatherOutcome struct {
	in     listInput
	err    error
	stdout string
	stderr string
}

// runGatherListInputStreams is runGatherReadyInput for `bd list`. Unlike
// runGatherListInput it keeps stdout and stderr apart, because a usage error
// lands on stderr as text but on stdout as a JSON object, and a test that pins
// where it went has to see both.
func runGatherListInputStreams(t *testing.T, cmd *cobra.Command) listGatherOutcome {
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

	out := listGatherOutcome{}
	out.in, out.err = gatherListInput(cmd)

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	out.stdout, out.stderr = <-outDone, <-errDone
	_ = rOut.Close()
	_ = rErr.Close()
	return out
}

// assertEmptyPatternRefusal checks one refused gather: exit code 1, and the
// flag-specific message on exactly one stream - stderr as text, or stdout as a
// single JSON error object - with the other stream silent.
func assertEmptyPatternRefusal(t *testing.T, err error, stdout, stderr, flag string, wantJSON bool) {
	t.Helper()

	var ee *exitError
	if !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("err = %v, want exit code 1: a supplied-but-empty %s must be refused, not read as no filter", err, flag)
	}
	want := flag + " was supplied but contains no pattern"
	if !wantJSON {
		if stdout != "" {
			t.Errorf("stdout = %q, want nothing in text mode", stdout)
		}
		if strings.Count(stderr, want) != 1 || !strings.HasPrefix(stderr, "Error: ") {
			t.Errorf("stderr = %q, want one %q error", stderr, want)
		}
		return
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing in JSON mode", stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("stdout %q is not a JSON error object: %v", stdout, err)
	}
	if dec.More() {
		t.Errorf("stdout = %q, want exactly one JSON object", stdout)
	}
	msg, _ := obj["error"].(string)
	if msg == "" {
		if data, ok := obj["data"].(map[string]any); ok {
			msg, _ = data["error"].(string)
		}
	}
	if msg != want {
		t.Errorf("JSON error = %q (stdout %q), want %q", msg, stdout, want)
	}
}

// emptyPatternCases are the values each pattern flag must refuse: a glob that
// is empty after trimming (labels are normalized, so a blank glob matches
// nothing usable), and a regex that is exactly empty. A whitespace regex is
// not here on purpose: it is a real predicate (labels may contain spaces).
var emptyPatternCases = []struct {
	flag  string
	value string
}{
	{"--label-pattern", ""},
	{"--label-pattern", "   "},
	{"--label-pattern", "\t \t"},
	{"--label-regex", ""},
}

// TestGatherListInputRejectsEmptyLabelPatterns pins GH#6636 for `bd list`: a
// supplied-but-empty --label-pattern or --label-regex used to read as "no
// pattern" and silently drop the predicate. Covers text, --json and the
// command's own --format json.
func TestGatherListInputRejectsEmptyLabelPatterns(t *testing.T) {
	modes := []struct {
		name     string
		json     bool
		extra    []string
		wantJSON bool
	}{
		{"text", false, nil, false},
		{"json", true, nil, true},
		{"format-json", false, []string{"--format", "json"}, true},
	}
	for _, c := range emptyPatternCases {
		for _, m := range modes {
			t.Run(c.flag+"="+strings.ReplaceAll(c.value, "\t", `\t`)+"/"+m.name, func(t *testing.T) {
				pinJSONOutput(t, m.json)
				args := append([]string{c.flag + "=" + c.value}, m.extra...)
				got := runGatherListInputStreams(t, newListFlagsCommand(t, args...))
				assertEmptyPatternRefusal(t, got.err, got.stdout, got.stderr, c.flag, m.wantJSON)
			})
		}
	}
}

// TestGatherReadyInputRejectsEmptyLabelPatterns is the `bd ready` half of
// GH#6636. gatherReadyInput serves both the direct route (with its cap
// resolver) and the proxied-server route (nil resolver), so both are driven.
func TestGatherReadyInputRejectsEmptyLabelPatterns(t *testing.T) {
	resolvers := []struct {
		name    string
		resolve func(*cobra.Command) (int, string, error)
	}{
		{"direct", resolveMaxRows},
		{"proxied", nil},
	}
	for _, c := range emptyPatternCases {
		for _, r := range resolvers {
			for _, jsonMode := range []bool{false, true} {
				name := c.flag + "=" + strings.ReplaceAll(c.value, "\t", `\t`) + "/" + r.name
				if jsonMode {
					name += "/json"
				}
				t.Run(name, func(t *testing.T) {
					pinJSONOutput(t, jsonMode)
					got := runGatherReadyInput(t, newReadyFlagsCommand(t, c.flag+"="+c.value), r.resolve)
					assertEmptyPatternRefusal(t, got.err, got.stdout, got.stderr, c.flag, jsonMode)
				})
			}
		}
	}
}

// TestGatherInputsKeepUsableLabelPatterns is the other side of the GH#6636
// refusal: omitted pattern flags still mean no pattern, and every usable value
// passes through byte-for-byte - including a regex of only whitespace (labels
// may contain spaces, and the regex is matched unanchored), a glob with
// surrounding whitespace, and a malformed regex (this check validates presence,
// not syntax).
func TestGatherInputsKeepUsableLabelPatterns(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantGlob  string
		wantRegex string
	}{
		{"omitted", nil, "", ""},
		{"glob", []string{"--label-pattern", "tech-*"}, "tech-*", ""},
		{"glob-padded", []string{"--label-pattern", " tech-* "}, " tech-* ", ""},
		{"regex", []string{"--label-regex", "^tech-(debt|legacy)$"}, "", "^tech-(debt|legacy)$"},
		{"regex-space", []string{"--label-regex", " "}, "", " "},
		{"regex-tabs", []string{"--label-regex", "\t\t"}, "", "\t\t"},
		{"regex-padded", []string{"--label-regex", "  a b  "}, "", "  a b  "},
		{"regex-malformed", []string{"--label-regex", "("}, "", "("},
		{"both", []string{"--label-pattern", "tech-*", "--label-regex", " "}, "tech-*", " "},
	}
	for _, c := range cases {
		t.Run("list/"+c.name, func(t *testing.T) {
			pinJSONOutput(t, false)
			got := runGatherListInputStreams(t, newListFlagsCommand(t, c.args...))
			if got.err != nil {
				t.Fatalf("gatherListInput(%q) = %v (stderr %q), want no error", c.args, got.err, got.stderr)
			}
			if got.in.LabelPattern != c.wantGlob || got.in.LabelRegex != c.wantRegex {
				t.Errorf("LabelPattern=%q LabelRegex=%q, want %q and %q unchanged",
					got.in.LabelPattern, got.in.LabelRegex, c.wantGlob, c.wantRegex)
			}
		})
		for _, r := range []struct {
			name    string
			resolve func(*cobra.Command) (int, string, error)
		}{
			{"direct", resolveMaxRows},
			{"proxied", nil},
		} {
			t.Run("ready/"+r.name+"/"+c.name, func(t *testing.T) {
				pinJSONOutput(t, false)
				got := runGatherReadyInput(t, newReadyFlagsCommand(t, c.args...), r.resolve)
				if got.err != nil {
					t.Fatalf("gatherReadyInput(%q) = %v (stderr %q), want no error", c.args, got.err, got.stderr)
				}
				if got.in.LabelPattern != c.wantGlob || got.in.LabelRegex != c.wantRegex {
					t.Errorf("LabelPattern=%q LabelRegex=%q, want %q and %q unchanged",
						got.in.LabelPattern, got.in.LabelRegex, c.wantGlob, c.wantRegex)
				}
			})
		}
	}
}
