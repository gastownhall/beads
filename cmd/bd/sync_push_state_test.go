package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// syncRecordingStore is a storage.DoltStorage stand-in for `bd sync`'s
// push-state recording: pull and recompute succeed, push succeeds or fails as
// configured, and GetCurrentCommit reports a fixed HEAD.
type syncRecordingStore struct {
	storage.DoltStorage
	head     string
	pushErr  error
	pulls    int
	pushes   int
	lastPush struct {
		remote string
		force  bool
	}
}

func (s *syncRecordingStore) Pull(context.Context) error {
	s.pulls++
	return nil
}

func (s *syncRecordingStore) PullRemote(_ context.Context, _ string) error {
	s.pulls++
	return nil
}

func (s *syncRecordingStore) Push(context.Context) error {
	s.pushes++
	s.lastPush.remote = ""
	return s.pushErr
}

func (s *syncRecordingStore) PushRemote(_ context.Context, remote string, force bool) error {
	s.pushes++
	s.lastPush.remote, s.lastPush.force = remote, force
	return s.pushErr
}

func (s *syncRecordingStore) GetCurrentCommit(context.Context) (string, error) {
	return s.head, nil
}

func (s *syncRecordingStore) GetConflicts(context.Context) ([]storage.Conflict, error) {
	return nil, nil
}

func (s *syncRecordingStore) RecomputeAllBlocked(context.Context) (int, error) {
	return 0, nil
}

func (s *syncRecordingStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return []storage.RemoteInfo{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/o/r"}}, nil
}

// runSyncForState runs `bd sync` (with --remote when remote is set) against
// st in the seeded workspace and returns (RunE error, recorded push state).
func runSyncForState(t *testing.T, st *syncRecordingStore, remote string) (error, *pushState) {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	prevCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = prevCtx })
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	pinStorageMode(t, false)
	store = st
	// Adoption is real git shelling-out; its no-op is pinned by
	// TestSyncAdoptGitOriginIsWiredToAdoption elsewhere.
	prevAdopt := syncAdoptGitOrigin
	syncAdoptGitOrigin = func(context.Context, storage.DoltStorage, adoptPolicy, adoptOptIn) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() { syncAdoptGitOrigin = prevAdopt })
	for name, value := range map[string]string{"remote": remote, "attempts": "2", "yes": "false", "no-adopt": "false"} {
		if err := syncCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	t.Cleanup(func() { _ = syncCmd.Flags().Set("remote", "") })
	var runErr error
	_, _ = captureStdio(t, func() { runErr = syncCmd.RunE(syncCmd, nil) })
	ps := loadSeededPushState(t)
	return runErr, ps
}

// TestSyncRecordsPushState pins the `bd sync` half of GH#6289: a successful
// sync that pushed to the default remote (origin) or a remote named origin
// records the push in push-state.json, while a sync that pushed to another
// remote, whose push failed, or that skipped the push (no-push) leaves the
// recorded state alone. The proxied-server refusal is pinned separately.
func TestSyncRecordsPushState(t *testing.T) {
	const plain = "no-git-ops: true\n"

	t.Run("default remote sync records the push", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &syncRecordingStore{head: "s1"}
		runErr, ps := runSyncForState(t, st, "")
		if runErr != nil {
			t.Fatalf("bd sync: %v", runErr)
		}
		if st.pushes != 1 || st.lastPush.remote != "" {
			t.Fatalf("pushes = %d (last %q), want one push to the default remote", st.pushes, st.lastPush.remote)
		}
		requireRecordedPushState(t, ps, "s1")
	})

	t.Run("sync with --remote origin records the push", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &syncRecordingStore{head: "s2"}
		runErr, ps := runSyncForState(t, st, "origin")
		if runErr != nil {
			t.Fatalf("bd sync --remote origin: %v", runErr)
		}
		if st.pushes != 1 || st.lastPush.remote != "origin" {
			t.Fatalf("pushes = %d (last %q), want one push to origin", st.pushes, st.lastPush.remote)
		}
		requireRecordedPushState(t, ps, "s2")
	})

	t.Run("sync with another named remote leaves the state unchanged", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &syncRecordingStore{head: "s3"}
		runErr, ps := runSyncForState(t, st, "backup")
		if runErr != nil {
			t.Fatalf("bd sync --remote backup: %v", runErr)
		}
		if st.pushes != 1 || st.lastPush.remote != "backup" {
			t.Fatalf("pushes = %d (last %q), want one push to backup", st.pushes, st.lastPush.remote)
		}
		// The backup remote's success must not clear origin's recorded
		// failure or suppress its pending auto-push (GH#6289 design).
		requireUnchangedPushState(t, ps)
	})

	t.Run("failed push leaves the state unchanged", func(t *testing.T) {
		setupPushStateWorkspace(t, plain, stalePushStateSeed)
		st := &syncRecordingStore{head: "s4", pushErr: errors.New("connection refused")}
		runErr, ps := runSyncForState(t, st, "")
		if runErr == nil {
			t.Fatal("bd sync with a failing push: want an error exit")
		}
		requireUnchangedPushState(t, ps)
	})

	t.Run("no-push sync skips the push and leaves the state unchanged", func(t *testing.T) {
		setupPushStateWorkspace(t, "no-git-ops: true\nno-push: true\n", stalePushStateSeed)
		st := &syncRecordingStore{head: "s5"}
		runErr, ps := runSyncForState(t, st, "")
		if runErr != nil {
			t.Fatalf("bd sync under no-push: %v", runErr)
		}
		if st.pushes != 0 {
			t.Fatalf("pushes = %d, want none (no-push skips only the publish step; the pull still ran %d times)", st.pushes, st.pulls)
		}
		if st.pulls == 0 {
			t.Fatal("pulls = 0, want the pull to still run under no-push")
		}
		requireUnchangedPushState(t, ps)
	})
}

// TestSyncPushStateDoesNotRecordProxiedServer pins that the recording lives
// in the command body only: the proxied-server refusal fires before any
// store exists, so no state can be written there.
func TestSyncPushStateDoesNotRecordProxiedServer(t *testing.T) {
	setupPushStateWorkspace(t, "no-git-ops: true\n", stalePushStateSeed)
	saveAndRestoreGlobals(t)
	resetCommandContext()
	pinStorageMode(t, false)
	proxiedServerMode = true
	t.Cleanup(func() { proxiedServerMode = false })
	runErr := syncCmd.RunE(syncCmd, nil)
	if runErr == nil || fmt.Sprint(runErr) == "" {
		t.Fatalf("bd sync in proxied-server mode: want the refusal, got %v", runErr)
	}
	requireUnchangedPushState(t, loadSeededPushState(t))
}
