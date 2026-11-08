package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// pushStateRecordingStore is a storage.DoltStorage stand-in for the push-state
// recording tests: origin is configured, Push/ForcePush/PushRemote succeed or
// fail as configured, and GetCurrentCommit reports a fixed HEAD.
type pushStateRecordingStore struct {
	storage.DoltStorage
	head    string
	pushErr error
	calls   []string
}

func (s *pushStateRecordingStore) Push(context.Context) error {
	s.calls = append(s.calls, "push")
	return s.pushErr
}

func (s *pushStateRecordingStore) ForcePush(context.Context) error {
	s.calls = append(s.calls, "force")
	return s.pushErr
}

func (s *pushStateRecordingStore) PushRemote(_ context.Context, remote string, force bool) error {
	s.calls = append(s.calls, fmt.Sprintf("pushremote %s force=%v", remote, force))
	return s.pushErr
}

func (s *pushStateRecordingStore) GetCurrentCommit(context.Context) (string, error) {
	return s.head, nil
}

func (s *pushStateRecordingStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return []storage.RemoteInfo{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/o/r"}}, nil
}

// stalePushStateSeed is a push-state.json a previous failed auto-push left
// behind: old success watermarks, an unresolved recorded failure.
const stalePushStateSeed = `{"last_push":"2026-01-01T00:00:00Z","last_commit":"oldcommit",` +
	`"last_attempt":"2026-01-01T01:00:00Z","last_error":"remote unreachable"}`

// setupPushStateWorkspace prepares a temp workspace holding seed ("" seeds
// nothing) and returns its .beads dir. The workspace opts into nothing
// beyond no-git-ops, so no-push/dolt.local-only start unset.
func setupPushStateWorkspace(t *testing.T, configYAML, seed string) string {
	t.Helper()
	beadsDir, _ := setupNoGitOpsAutoPushWorkspace(t, configYAML)
	if seed != "" {
		seedRawPushState(t, beadsDir, seed)
	}
	return beadsDir
}

// runDoltPushForState runs `bd dolt push` in the chosen storage mode against
// st and returns (RunE error, recorded push state).
func runDoltPushForState(t *testing.T, st *pushStateRecordingStore, remote string, force, server bool) error {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	pinStorageMode(t, server)
	store = st
	// The workspace config.yaml (no-push, dolt.local-only) must be read
	// fresh: ResetForTesting alone leaves every key at its default.
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	for name, value := range map[string]string{"force": fmt.Sprint(force), "yes": "false", "no-adopt": "false", "remote": remote} {
		if err := doltPushCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	t.Cleanup(func() { _ = doltPushCmd.Flags().Set("remote", "") })
	var runErr error
	_, _ = captureStdio(t, func() { runErr = doltPushCmd.RunE(doltPushCmd, nil) })
	return runErr
}

// requireRecordedPushState asserts that a successful push recorded success:
// fresh last_push, the pushed commit as last_commit, last_error cleared.
func requireRecordedPushState(t *testing.T, ps *pushState, head string) {
	t.Helper()
	if ps == nil {
		t.Fatal("push-state.json missing after a successful push; want success recorded")
	}
	if push, err := time.Parse(time.RFC3339, ps.LastPush); err != nil || time.Since(push) > time.Minute {
		t.Errorf("last_push = %q, want a fresh success timestamp: %v", ps.LastPush, err)
	}
	if ps.LastCommit != head {
		t.Errorf("last_commit = %q, want the pushed HEAD %q", ps.LastCommit, head)
	}
	if ps.LastError != "" {
		t.Errorf("last_error = %q after a successful push, want cleared", ps.LastError)
	}
}

// requireUnchangedPushState asserts the seeded state survived verbatim.
func requireUnchangedPushState(t *testing.T, ps *pushState) {
	t.Helper()
	if ps == nil {
		t.Fatal("push-state.json missing; want the seeded state untouched")
	}
	if ps.LastPush != "2026-01-01T00:00:00Z" || ps.LastCommit != "oldcommit" ||
		ps.LastAttempt != "2026-01-01T01:00:00Z" || ps.LastError != "remote unreachable" {
		t.Errorf("push state changed: %+v, want the seeded values verbatim", ps)
	}
}

// loadSeededPushState reads push-state.json from the active workspace and
// fails the test when it cannot be read.
func loadSeededPushState(t *testing.T) *pushState {
	t.Helper()
	ps, err := loadPushState()
	if err != nil {
		t.Fatalf("loadPushState after push: %v", err)
	}
	return ps
}

// TestDoltPushRecordsPushState pins GH#6289's manual-push half: a successful
// `bd dolt push` to the default remote or a remote named origin rewrites
// push-state.json the same way in server and embedded mode, while a push to
// another remote, a failed push, and every skipped push leave the recorded
// state alone.
func TestDoltPushRecordsPushState(t *testing.T) {
	const plain = "no-git-ops: true\n"

	t.Run("default remote success records in embedded mode", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &pushStateRecordingStore{head: "c1"}
		runErr := runDoltPushForState(t, st, "", false, false)
		if runErr != nil {
			t.Fatalf("bd dolt push: %v", runErr)
		}
		if len(st.calls) != 1 || st.calls[0] != "push" {
			t.Fatalf("store calls = %v, want one plain push", st.calls)
		}
		requireRecordedPushState(t, loadSeededPushState(t), "c1")
	})

	t.Run("default remote success records in server mode", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &pushStateRecordingStore{head: "c1"}
		runErr := runDoltPushForState(t, st, "", false, true)
		if runErr != nil {
			t.Fatalf("bd dolt push: %v", runErr)
		}
		requireRecordedPushState(t, loadSeededPushState(t), "c1")
	})

	t.Run("force push to the default remote records", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &pushStateRecordingStore{head: "c2"}
		runErr := runDoltPushForState(t, st, "", true, false)
		if runErr != nil {
			t.Fatalf("bd dolt push --force: %v", runErr)
		}
		if len(st.calls) != 1 || st.calls[0] != "force" {
			t.Fatalf("store calls = %v, want one force push", st.calls)
		}
		requireRecordedPushState(t, loadSeededPushState(t), "c2")
	})

	t.Run("push to a remote named origin records", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &pushStateRecordingStore{head: "c3"}
		runErr := runDoltPushForState(t, st, "origin", false, false)
		if runErr != nil {
			t.Fatalf("bd dolt push origin: %v", runErr)
		}
		if len(st.calls) != 1 || !strings.HasPrefix(st.calls[0], "pushremote origin") {
			t.Fatalf("store calls = %v, want one pushremote origin", st.calls)
		}
		requireRecordedPushState(t, loadSeededPushState(t), "c3")
	})

	t.Run("push to another remote leaves the state unchanged", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &pushStateRecordingStore{head: "c4"}
		runErr := runDoltPushForState(t, st, "backup", false, false)
		if runErr != nil {
			t.Fatalf("bd dolt push backup: %v", runErr)
		}
		if len(st.calls) != 1 || !strings.HasPrefix(st.calls[0], "pushremote backup") {
			t.Fatalf("store calls = %v, want one pushremote backup", st.calls)
		}
		// A named-remote backup push must not clear origin's recorded failure
		// or suppress its pending auto-push (GH#6289 design).
		requireUnchangedPushState(t, loadSeededPushState(t))
	})

	t.Run("failed push leaves the state unchanged", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &pushStateRecordingStore{head: "c5", pushErr: errors.New("connection refused")}
		runErr := runDoltPushForState(t, st, "", false, false)
		if runErr == nil {
			t.Fatal("bd dolt push with a failing store: want an error exit")
		}
		requireUnchangedPushState(t, loadSeededPushState(t))
	})

	t.Run("no-push skip leaves the state unchanged", func(t *testing.T) {
		setupPushStateWorkspace(t, "no-git-ops: true\nno-push: true\n", stalePushStateSeed)
		st := &pushStateRecordingStore{head: "c6"}
		runErr := runDoltPushForState(t, st, "", false, false)
		if runErr != nil {
			t.Fatalf("bd dolt push under no-push: %v", runErr)
		}
		if len(st.calls) != 0 {
			t.Fatalf("store calls = %v, want none (push skipped)", st.calls)
		}
		requireUnchangedPushState(t, loadSeededPushState(t))
	})

	t.Run("dolt.local-only skip leaves the state unchanged", func(t *testing.T) {
		setupPushStateWorkspace(t, "no-git-ops: true\ndolt:\n  local-only: true\n", stalePushStateSeed)
		st := &pushStateRecordingStore{head: "c7"}
		runErr := runDoltPushForState(t, st, "", false, false)
		if runErr != nil {
			t.Fatalf("bd dolt push under dolt.local-only: %v", runErr)
		}
		if len(st.calls) != 0 {
			t.Fatalf("store calls = %v, want none (push skipped)", st.calls)
		}
		requireUnchangedPushState(t, loadSeededPushState(t))
	})

	t.Run("a state file that cannot be written fails no push", func(t *testing.T) {
		beadsDir := setupPushStateWorkspace(t, plain, "")
		// A directory where push-state.json belongs makes every write fail;
		// the push already succeeded, so the command must still exit 0.
		if err := os.Mkdir(filepath.Join(beadsDir, "push-state.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		st := &pushStateRecordingStore{head: "c8"}
		runErr := runDoltPushForState(t, st, "", false, false)
		if runErr != nil {
			t.Fatalf("bd dolt push with an unwritable push-state.json: %v, want nil (bookkeeping is not fatal)", runErr)
		}
		if len(st.calls) != 1 {
			t.Fatalf("store calls = %v, want the push to have run", st.calls)
		}
	})
}
