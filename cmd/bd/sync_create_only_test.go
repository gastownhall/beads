package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// trackerRefusalCase is one invocation of a tracker sync command whose
// --create-only must either be refused up front (a sync that never pushes)
// or accepted past the direction check (push or bidirectional sync).
type trackerRefusalCase struct {
	name    string
	tracker string // tracker name the refusal must carry
	cmd     *cobra.Command
	flags   []string // bool flags to set true
	refused bool
}

// isolateTrackerSyncCommand points one tracker sync RunE at an environment
// where every side effect fails loudly: no workspace (so the store open
// errors), no tracker credentials (so config and auth error), and a temp
// working directory no path variable can escape. The refusal under test has
// to fire before any of that, so which failure reaches stderr is itself the
// thing the assertions read.
func isolateTrackerSyncCommand(t *testing.T, cmd *cobra.Command, flags []string) {
	t.Helper()

	saveAndRestoreGlobals(t)
	store = nil
	dbPath = ""
	oldRootCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = oldRootCtx })
	t.Chdir(t.TempDir())
	for _, k := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "NOTION_TOKEN", "NOTION_DATA_SOURCE_ID"} {
		t.Setenv(k, "")
	}
	// Pin the proxied-server route off: the notion runner's proxied refusal
	// sits before its direction check and would shadow the case under test.
	oldProxied, oldCmdCtx := proxiedServerMode, cmdCtx
	proxiedServerMode, cmdCtx = false, nil
	t.Cleanup(func() { proxiedServerMode, cmdCtx = oldProxied, oldCmdCtx })

	t.Cleanup(func() { resetCommandFlags(cmd) })
	for _, f := range flags {
		if err := cmd.Flags().Set(f, "true"); err != nil {
			t.Fatalf("setting --%s on %s sync: %v", f, cmd.Name(), err)
		}
	}
}

// sideEffectMarkers name the errors a tracker sync command prints once it is
// past input validation and starts doing work. None of them may appear on a
// refused run: the refusal has to come first (gastownhall/beads#8507).
var sideEffectMarkers = []string{
	"database not available",                  // store open (linear, jira)
	"Notion authentication is not configured", // notion auth
	"notion.data_source_id is not configured", // notion config
}

// TestTrackerSyncRefusesCreateOnlyOnPull pins #8507: --create-only limits
// only the push, so each tracker sync command must refuse it, before any
// side effect, on a sync that never pushes, and keep accepting it on a push
// or bidirectional sync, where the flag limits the push half.
func TestTrackerSyncRefusesCreateOnlyOnPull(t *testing.T) {
	// Not parallel: sets flags on the shared cobra tree and redirects stderr.

	for _, tc := range []trackerRefusalCase{
		// Refused: a pull never reads --create-only, so accepting it there
		// silently updates every linked bead the flag seems to protect.
		{name: "linear_pull", tracker: "Linear", cmd: linearSyncCmd, flags: []string{"pull", "create-only"}, refused: true},
		{name: "linear_pull_if_stale", tracker: "Linear", cmd: linearSyncCmd, flags: []string{"pull-if-stale", "create-only"}, refused: true},
		{name: "linear_pull_dry_run", tracker: "Linear", cmd: linearSyncCmd, flags: []string{"pull", "create-only", "dry-run"}, refused: true},
		// Accepted: --push runs a push the flag limits, and the default
		// bidirectional sync keeps pulling linked beads current.
		{name: "linear_push", tracker: "Linear", cmd: linearSyncCmd, flags: []string{"push", "create-only"}},
		{name: "linear_bidirectional", tracker: "Linear", cmd: linearSyncCmd, flags: []string{"create-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTrackerSyncCommand(t, tc.cmd, tc.flags)

			var runErr error
			stderr := captureStderr(t, func() { runErr = tc.cmd.RunE(tc.cmd, []string{}) })

			wantRefusal := "--create-only only applies when pushing to " + tc.tracker
			if tc.refused {
				if runErr == nil {
					t.Errorf("RunE = nil, want the --create-only refusal")
				}
				if !strings.Contains(stderr, wantRefusal) {
					t.Errorf("stderr = %q, want the refusal %q", stderr, wantRefusal)
				}
				for _, marker := range sideEffectMarkers {
					if strings.Contains(stderr, marker) {
						t.Errorf("stderr contains %q, so the command ran past the direction check", marker)
					}
				}
			} else if strings.Contains(stderr, wantRefusal) {
				t.Errorf("stderr = %q, did not expect the refusal on a run that pushes", stderr)
			}
		})
	}

	// The --json shape of the refusal: HandleErrorRespectJSON reports it as a
	// JSON error object on stdout, so an agent parsing stdout sees it too.
	t.Run("linear_pull_json", func(t *testing.T) {
		pinJSONOutput(t, true)
		isolateTrackerSyncCommand(t, linearSyncCmd, []string{"pull", "create-only"})

		stdout, runErr := captureStdoutErr(t, func() error {
			return linearSyncCmd.RunE(linearSyncCmd, []string{})
		})
		if runErr == nil {
			t.Error("RunE = nil, want the --create-only refusal")
		}
		if !strings.Contains(stdout, "--create-only only applies when pushing to Linear") {
			t.Errorf("stdout = %q, want a JSON error naming --create-only", stdout)
		}
	})
}

// captureStdoutErr captures what fn writes to stdout while it runs and
// returns it with fn's error. HandleErrorRespectJSON reports a refusal as a
// JSON object on stdout, and unlike captureStdout this must not fail the
// test on the error: the error is the outcome under test.
func captureStdoutErr(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	old := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	runErr := fn()

	w.Close()
	os.Stdout = old
	<-done
	_ = r.Close()
	return buf.String(), runErr
}

// TestCreateOnlyDirectionError covers the helper behind the refusal: only
// the pull-only + create-only combination is an error, and the error names
// the tracker so each command's refusal is self-describing.
func TestCreateOnlyDirectionError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		createOnly bool
		pullOnly   bool
		wantErr    bool
	}{
		{name: "pull_only_with_create_only", createOnly: true, pullOnly: true, wantErr: true},
		{name: "push_with_create_only", createOnly: true, pullOnly: false},
		{name: "pull_only_without_create_only", createOnly: false, pullOnly: true},
		{name: "bidirectional_without_create_only", createOnly: false, pullOnly: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := createOnlyDirectionError("Linear", tc.createOnly, tc.pullOnly)
			if !tc.wantErr {
				if err != nil {
					t.Errorf("createOnlyDirectionError = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("createOnlyDirectionError = nil, want the refusal")
			}
			if !strings.Contains(err.Error(), "--create-only only applies when pushing to Linear") {
				t.Errorf("error = %v, want it to name the tracker", err)
			}
		})
	}
}
