package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// createOnlySyncCase is one tracker sync invocation for
// TestTrackerSyncRefusesCreateOnlyOnPull.
type createOnlySyncCase struct {
	name    string
	cmd     *cobra.Command
	tracker string
	flags   []string
	refused bool
}

// runTrackerSyncForTest runs cmd's RunE with flags set on the real cobra
// command, in a scratch directory that no path can resolve to a real
// workspace or real tracker credentials, and returns the RunE error with what
// it printed to stdout and stderr.
func runTrackerSyncForTest(t *testing.T, cmd *cobra.Command, flags []string) (runErr error, stdout, stderr string) {
	t.Helper()

	saveAndRestoreGlobals(t)
	store = nil
	dbPath = ""
	savedCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = savedCtx })

	t.Chdir(t.TempDir())
	for _, k := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "NOTION_TOKEN", "NOTION_DATA_SOURCE_ID"} {
		t.Setenv(k, "")
	}

	resetCommandFlags(cmd)
	t.Cleanup(func() { resetCommandFlags(cmd) })
	for _, f := range flags {
		if err := cmd.Flags().Set(f, "true"); err != nil {
			t.Fatalf("setting --%s on %s: %v", f, cmd.CommandPath(), err)
		}
	}
	cmd.SetContext(context.Background())

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	var outBuf, errBuf bytes.Buffer
	outDone, errDone := make(chan struct{}), make(chan struct{})
	go func() { _, _ = io.Copy(&outBuf, outR); close(outDone) }()
	go func() { _, _ = io.Copy(&errBuf, errR); close(errDone) }()
	os.Stdout, os.Stderr = outW, errW
	func() {
		defer func() {
			os.Stdout, os.Stderr = oldOut, oldErr
			_ = outW.Close()
			_ = errW.Close()
			<-outDone
			<-errDone
			_ = outR.Close()
			_ = errR.Close()
		}()
		runErr = cmd.RunE(cmd, nil)
	}()
	return runErr, outBuf.String(), errBuf.String()
}

// TestTrackerSyncRefusesCreateOnlyOnPull pins gastownhall/beads#8507:
// --create-only limits only the push, so a sync that never pushes must refuse
// it before it takes the sync lock, opens the store or reads tracker config,
// instead of silently updating every linked bead.
func TestTrackerSyncRefusesCreateOnlyOnPull(t *testing.T) {
	pinJSONOutput(t, false)

	cases := []createOnlySyncCase{
		{name: "linear pull", cmd: linearSyncCmd, tracker: "Linear", flags: []string{"pull", "create-only"}, refused: true},
		{name: "linear pull-if-stale", cmd: linearSyncCmd, tracker: "Linear", flags: []string{"pull-if-stale", "create-only"}, refused: true},
		{name: "linear pull dry-run", cmd: linearSyncCmd, tracker: "Linear", flags: []string{"pull", "create-only", "dry-run"}, refused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err, _, stderr := runTrackerSyncForTest(t, tc.cmd, tc.flags)
			refusal := "--create-only only applies when pushing to " + tc.tracker
			if !tc.refused {
				if strings.Contains(stderr, "--create-only only applies") {
					t.Fatalf("%s --%s refused --create-only; stderr:\n%s", tc.cmd.CommandPath(), strings.Join(tc.flags, " --"), stderr)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s --%s returned nil, want the --create-only refusal; stderr:\n%s", tc.cmd.CommandPath(), strings.Join(tc.flags, " --"), stderr)
			}
			if !strings.Contains(stderr, refusal) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, refusal)
			}
			for _, later := range []string{"database not available", "Acquiring sync lock", "Notion authentication", "notion.data_source_id"} {
				if strings.Contains(stderr, later) {
					t.Errorf("stderr contains %q: the refusal must come before any lock, store or config access; stderr:\n%s", later, stderr)
				}
			}
		})
	}

	t.Run("linear pull json", func(t *testing.T) {
		pinJSONOutput(t, true)
		err, stdout, _ := runTrackerSyncForTest(t, linearSyncCmd, []string{"pull", "create-only"})
		if err == nil {
			t.Fatal("bd linear sync --pull --create-only --json returned nil, want the refusal")
		}
		var payload map[string]interface{}
		if jerr := json.Unmarshal([]byte(stdout), &payload); jerr != nil {
			t.Fatalf("stdout is not a JSON error object: %v\nstdout:\n%s", jerr, stdout)
		}
		if data, ok := payload["data"].(map[string]interface{}); ok {
			payload = data // JSON envelope enabled
		}
		if msg, _ := payload["error"].(string); !strings.Contains(msg, "--create-only only applies when pushing to Linear") {
			t.Fatalf("JSON error = %q, want the --create-only refusal; stdout:\n%s", msg, stdout)
		}
	})
}

func TestCreateOnlyDirectionError(t *testing.T) {
	for _, tc := range []struct {
		createOnly, pullOnly bool
		wantErr              bool
	}{
		{createOnly: false, pullOnly: false},
		{createOnly: false, pullOnly: true},
		{createOnly: true, pullOnly: false},
		{createOnly: true, pullOnly: true, wantErr: true},
	} {
		err := createOnlyDirectionError("Jira", tc.createOnly, tc.pullOnly)
		if (err != nil) != tc.wantErr {
			t.Errorf("createOnlyDirectionError(createOnly=%v, pullOnly=%v) = %v, want error %v", tc.createOnly, tc.pullOnly, err, tc.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "--create-only only applies when pushing to Jira") {
			t.Errorf("error %q does not name the tracker", err)
		}
	}
}
