package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// excludeParentsConflicts are the ready modes that answer with a shape of their
// own and so cannot honor --exclude-parents (GH#6817).
var excludeParentsConflicts = []struct {
	name string
	flag string
	val  string
	want string
}{
	{"gated", "gated", "true", "--gated cannot be combined with --exclude-parents"},
	{"mol", "mol", "bd-1", "--mol cannot be combined with --exclude-parents"},
	{"explain", "explain", "true", "--explain cannot be combined with --exclude-parents"},
}

// assertExcludeParentsRefusal checks the usage error names both flags, and that
// it is a JSON object on stdout under --json and text on stderr otherwise.
func assertExcludeParentsRefusal(t *testing.T, jsonOn bool, err error, stdout, stderr, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want a usage error naming %q", want)
	}
	if jsonOn {
		var payload map[string]any
		if decodeErr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &payload); decodeErr != nil {
			t.Fatalf("--json stdout = %q, want one JSON error object: %v", stdout, decodeErr)
		}
		if msg, _ := payload["error"].(string); !strings.Contains(msg, want) {
			t.Errorf("--json error = %q, want it to name %q", msg, want)
		}
		return
	}
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want it to name %q", stderr, want)
	}
}

// TestReadyExcludeParentsIsRefusedWithTheSpecialModes drives the gatherer: each
// special mode, with and without --json, refuses the flag naming both flags.
func TestReadyExcludeParentsIsRefusedWithTheSpecialModes(t *testing.T) {
	for _, tc := range excludeParentsConflicts {
		for _, jsonOn := range []bool{true, false} {
			name := tc.name + "/text"
			if jsonOn {
				name = tc.name + "/json"
			}
			t.Run(name, func(t *testing.T) {
				pinJSONOutput(t, jsonOn)
				got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--exclude-parents", "--"+tc.flag+"="+tc.val), nil)
				assertExcludeParentsRefusal(t, jsonOn, got.err, got.stdout, got.stderr, tc.want)
			})
		}
	}
}

// TestReadyCmdRefusesExcludeParentsBeforeItDispatches drives readyCmd's RunE:
// --gated, --mol and --explain are dispatched before the gatherer runs, so the
// gatherer's check alone would leave the direct route taking the flag and
// ignoring it. The check sits above every store access, so RunE is reachable.
func TestReadyCmdRefusesExcludeParentsBeforeItDispatches(t *testing.T) {
	setFlag := func(t *testing.T, name, value string) {
		t.Helper()
		if err := readyCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set %s=%s: %v", name, value, err)
		}
		t.Cleanup(func() {
			def := readyCmd.Flags().Lookup(name).DefValue
			_ = readyCmd.Flags().Set(name, def)
			readyCmd.Flags().Lookup(name).Changed = false
		})
	}

	for _, tc := range excludeParentsConflicts {
		t.Run(tc.name, func(t *testing.T) {
			pinJSONOutput(t, true)
			setFlag(t, "exclude-parents", "true")
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
			stdout, stderr := <-outDone, <-errDone
			_ = rOut.Close()
			_ = rErr.Close()

			assertExcludeParentsRefusal(t, true, err, stdout, stderr, tc.want)
		})
	}
}

// TestReadyExcludeParentsReachesTheFilterAndTheClaim is the negative control
// for the refusals above: alone, or with --claim and --parent, the flag gets
// through to the WorkFilter and to the role request the claim and the count
// are built from.
func TestReadyExcludeParentsReachesTheFilterAndTheClaim(t *testing.T) {
	pinJSONOutput(t, true)
	for _, args := range [][]string{
		{"--exclude-parents"},
		{"--exclude-parents", "--claim"},
		{"--exclude-parents", "--parent", "bd-1"},
	} {
		got := runGatherReadyInput(t, newReadyFlagsCommand(t, args...), nil)
		if got.err != nil {
			t.Fatalf("gatherReadyInput(%v) = %v, want no error", args, got.err)
		}
		if !got.in.filter.ExcludeParentsWithOpenChildren {
			t.Errorf("gatherReadyInput(%v) did not set WorkFilter.ExcludeParentsWithOpenChildren", args)
		}
		if !readyRoleRequest(got.in).ExcludeParentsWithOpenChildren {
			t.Errorf("readyRoleRequest(%v) dropped ExcludeParentsWithOpenChildren", args)
		}
	}

	got := runGatherReadyInput(t, newReadyFlagsCommand(t), nil)
	if got.err != nil {
		t.Fatalf("gatherReadyInput() = %v", got.err)
	}
	if got.in.filter.ExcludeParentsWithOpenChildren || readyRoleRequest(got.in).ExcludeParentsWithOpenChildren {
		t.Error("the flag is on without --exclude-parents; the default must be unchanged")
	}
}
