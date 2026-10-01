//go:build cgo

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// resetDataOrderStore is the store reset-data sees, with the three calls the
// command makes recorded in order. detector true makes it report pending
// changes the way the server-mode store does: the answers are consumed
// front to back, so clean-then-dirty models a write landing while the
// confirmation prompt waits.
type resetDataOrderStore struct {
	storage.DoltStorage
	remotes   []storage.RemoteInfo
	commitErr error
	calls     []string

	detector bool
	pending  []bool
}

func (s *resetDataOrderStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return s.remotes, nil
}

func (s *resetDataOrderStore) CommitPending(context.Context, string) (bool, error) {
	s.calls = append(s.calls, "commit")
	return s.commitErr == nil, s.commitErr
}

func (s *resetDataOrderStore) PushRemote(context.Context, string, bool) error {
	s.calls = append(s.calls, "push")
	return nil
}

// resetDataOrderServerStore adds the detector; a separate type so the plain
// fake does not satisfy storage.PendingChangeDetector.
type resetDataOrderServerStore struct {
	*resetDataOrderStore
}

func (s *resetDataOrderServerStore) HasCommittablePending(context.Context) (bool, error) {
	s.calls = append(s.calls, "dirty?")
	if len(s.pending) == 0 {
		return false, nil
	}
	dirty := s.pending[0]
	s.pending = s.pending[1:]
	return dirty, nil
}

func runResetDataWith(t *testing.T, st storage.DoltStorage) error {
	t.Helper()
	saveStorageMode(t)
	store = st
	serverMode = false
	proxiedServerMode = false
	oldCtx, oldYes := rootCtx, doltRemoteResetDataYes
	rootCtx = context.Background()
	doltRemoteResetDataYes = true
	t.Cleanup(func() {
		rootCtx = oldCtx
		doltRemoteResetDataYes = oldYes
	})
	return doltRemoteResetDataCmd.RunE(doltRemoteResetDataCmd, []string{"origin"})
}

func absentFileRemote(t *testing.T) []storage.RemoteInfo {
	t.Helper()
	return []storage.RemoteInfo{{Name: "origin", URL: "file://" + filepath.Join(t.TempDir(), "absent")}}
}

func TestResetDataOrder(t *testing.T) {
	t.Run("embedded commits before the push", func(t *testing.T) {
		st := &resetDataOrderStore{remotes: absentFileRemote(t)}
		if err := runResetDataWith(t, st); err != nil {
			t.Fatalf("reset-data: %v", err)
		}
		if got := st.calls; len(got) != 2 || got[0] != "commit" || got[1] != "push" {
			t.Fatalf("calls = %v, want [commit push]", got)
		}
	})

	t.Run("embedded commit failure leaves the remote's ref in place", func(t *testing.T) {
		remoteDir := bareGitRemoteNamed(t, "remote.git")
		const ref = "refs/dolt/units/order"
		resetDataRunGit(t, remoteDir, "update-ref", ref, "refs/heads/main")
		before := lsRemoteRef(t, remoteDir, ref)
		if before == "" {
			t.Fatal("planting the ref failed")
		}
		st := &resetDataOrderStore{
			remotes:   []storage.RemoteInfo{{Name: "origin", URL: "git+file://" + remoteDir, Ref: ref}},
			commitErr: errors.New("the table(s) t have constraint violations"),
		}
		err := runResetDataWith(t, st)
		var exit *exitError
		if !errors.As(err, &exit) {
			t.Fatalf("reset-data returned %v, want the command's exit error", err)
		}
		if got := st.calls; len(got) != 1 || got[0] != "commit" {
			t.Errorf("calls = %v, want [commit] and no push", got)
		}
		if got := lsRemoteRef(t, remoteDir, ref); got != before {
			t.Errorf("%s changed across the failed reset: %q -> %q", ref, before, got)
		}
	})

	t.Run("server mode refuses a working set dirtied after the first check", func(t *testing.T) {
		inner := &resetDataOrderStore{remotes: absentFileRemote(t), detector: true, pending: []bool{false, true}}
		st := &resetDataOrderServerStore{resetDataOrderStore: inner}
		err := runResetDataWith(t, st)
		var exit *exitError
		if !errors.As(err, &exit) {
			t.Fatalf("reset-data returned %v, want the refusal", err)
		}
		if got := inner.calls; len(got) != 2 || got[0] != "dirty?" || got[1] != "dirty?" {
			t.Errorf("calls = %v, want two dirty checks, no commit, no push", got)
		}
	})

	t.Run("server mode never commits on the user's behalf", func(t *testing.T) {
		inner := &resetDataOrderStore{remotes: absentFileRemote(t), detector: true}
		st := &resetDataOrderServerStore{resetDataOrderStore: inner}
		if err := runResetDataWith(t, st); err != nil {
			t.Fatalf("reset-data: %v", err)
		}
		if got := inner.calls; len(got) != 3 || got[0] != "dirty?" || got[1] != "dirty?" || got[2] != "push" {
			t.Errorf("calls = %v, want [dirty? dirty? push]", got)
		}
	})
}
