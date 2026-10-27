package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// TestDoltPushPrintsRemoteDanglingReferenceGuidance pins the recovery
// guidance `bd dolt push` must print when Dolt refuses to update the remote
// because the remote reports a chunk the push points to is missing
// ("Found dangling references to HashSet", GH#3358). The raw Error line stays
// unchanged; the guidance is appended on stderr, so --json consumers on
// stdout are unaffected, and the command still exits through SilentExit().
func TestDoltPushPrintsRemoteDanglingReferenceGuidance(t *testing.T) {
	// The reporter's representative SQL-route refusal (GH#3358).
	sqlRouteRefusal := errors.New("failed to push to origin/main: Error 1105 (HY000): unknown push error; Found dangling references to HashSet {\n  e564v1teutmhp289qspj9ba1lhrvit00\n}")

	t.Run("default remote", func(t *testing.T) {
		fake := &fakeDanglingRefusalStore{
			pushErr: sqlRouteRefusal,
			remotes: []storage.RemoteInfo{
				{Name: "origin", URL: "https://doltremoteapi.dolthub.com/org/repo"},
			},
		}
		setupDanglingRefPushGlobals(t, fake)

		_, stderr, runErr := runPushCapturingStdio(t, func() error {
			return doltPushCmd.RunE(doltPushCmd, nil)
		})

		var exit *exitError
		if !errors.As(runErr, &exit) || exit.Code != 1 {
			t.Errorf("RunE = %v, want the SilentExit() exit error (code 1)", runErr)
		}
		if !strings.Contains(stderr, "Error: failed to push to origin/main:") {
			t.Errorf("stderr must keep the unchanged raw Error line, got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "e564v1teutmhp289qspj9ba1lhrvit00") {
			t.Errorf("stderr must include the hash from the refusal, got:\n%s", stderr)
		}
		assertRemoteDanglingReferenceGuidance(t, stderr)
		if strings.Contains(stderr, "schema fork") {
			t.Errorf("a dangling-refusal push must not print PK-mismatch guidance, got:\n%s", stderr)
		}
		if strings.Contains(stderr, "diverged") {
			t.Errorf("a dangling-refusal push must not print diverged-history guidance, got:\n%s", stderr)
		}
	})
}

// assertRemoteDanglingReferenceGuidance checks that every phrase the design
// pins for this guidance (GH#3358) appears verbatim, each on a single line.
func assertRemoteDanglingReferenceGuidance(t *testing.T, stderr string) {
	t.Helper()
	pinned := []string{
		"Dolt refused to update the remote",
		"did not move the remote branch",
		"same hash",
		"Re-run the same push once",
		"bd dolt remote add <name> <url>",
		"bd dolt push --remote <name>",
		"https://github.com/dolthub/dolt/issues",
		"github.com/gastownhall/beads/blob/main/docs/recovery/sync-failures.md#push-fails-with-found-dangling-references",
	}
	for _, phrase := range pinned {
		if !phraseAppearsOnOneLine(stderr, phrase) {
			t.Errorf("guidance lacks pinned phrase %q on a single line, got:\n%s", phrase, stderr)
		}
	}
}

func phraseAppearsOnOneLine(s, phrase string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, phrase) {
			return true
		}
	}
	return false
}

// fakeDanglingRefusalStore implements storage.DoltStorage by embedding the
// interface, overriding the push surface and the remote listing the push
// command consults before and after a failed push.
type fakeDanglingRefusalStore struct {
	storage.DoltStorage
	pushErr       error
	forcePushErr  error
	pushRemoteErr error
	remotes       []storage.RemoteInfo
}

func (f *fakeDanglingRefusalStore) Push(context.Context) error { return f.pushErr }
func (f *fakeDanglingRefusalStore) ForcePush(context.Context) error {
	return f.forcePushErr
}
func (f *fakeDanglingRefusalStore) PushRemote(_ context.Context, _ string, _ bool) error {
	return f.pushRemoteErr
}
func (f *fakeDanglingRefusalStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return f.remotes, nil
}

// setupDanglingRefPushGlobals installs fake as the store the push command
// reaches. Cannot be parallel: touches process-global store, config and
// jsonOutput.
func setupDanglingRefPushGlobals(t *testing.T, fake storage.DoltStorage) {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	store = fake
	// Reset (not Initialize) so no-push / dolt.local-only read as false and
	// no user config or env can leak into the run.
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	origJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = origJSON })
}

// runPushCapturingStdio runs fn with os.Stdout and os.Stderr captured and
// returns both streams plus fn's error. Uses stdioMutex like captureStdout /
// captureStderr, so it cannot run concurrently with those helpers.
func runPushCapturingStdio(t *testing.T, fn func() error) (stdout, stderr string, runErr error) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err1 := os.Pipe()
	rErr, wErr, err2 := os.Pipe()
	if err1 != nil || err2 != nil {
		t.Fatalf("os.Pipe: %v / %v", err1, err2)
	}
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	var bOut, bErr bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&bOut, rOut)
		_, _ = io.Copy(&bErr, rErr)
		close(done)
	}()

	runErr = fn()

	_ = wOut.Close()
	_ = wErr.Close()
	<-done
	_ = rOut.Close()
	_ = rErr.Close()
	return bOut.String(), bErr.String(), runErr
}
