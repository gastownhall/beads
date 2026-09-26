//go:build cgo

package embeddeddolt

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func wantStopRemote(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errStopRemote) {
		t.Fatalf("error = %v, want errStopRemote (stopped before the remote entry point)", err)
	}
}

func wantRemoteNotFound(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("error = %v, want a remote-not-found failure (no real remote is configured in this test store)", err)
	}
}

// TestPushCommitsPendingChangesFirst is a regression test for #5433: every
// Pull variant auto-commits pending changes before pulling (GH#2474), but no
// Push variant did, so a write that landed in the working set but was never
// explicitly committed (auto-commit off, or a session that ended right after
// a write with no intervening commit) silently pushed nothing new while
// still reporting success. Push/ForcePush/PushRemote/PushTo now call
// CommitPending first, matching Pull's own pattern.
//
// Uses captureRemoteEntryPoints (remote_peer_auth_test.go) to stop each verb
// at its remote entry point with errStopRemote, keeping the test off the
// network while still exercising the real CommitPending call that runs
// before it.
func TestPushCommitsPendingChangesFirst(t *testing.T) {
	ctx := t.Context()

	cases := []struct {
		name string
		call func(*EmbeddedDoltStore) error
		// wantErr checks the error from call. Push/ForcePush/PushRemote route
		// through the swappable vcPush/vcForcePush vars that
		// captureRemoteEntryPoints intercepts with errStopRemote before any
		// network IO. PushTo calls versioncontrolops.Push directly (no
		// swappable seam for it), so it actually attempts the push and fails
		// on "remote not found" -- still proves CommitPending ran first,
		// since that failure only happens after this fix's guard clause.
		wantErr func(t *testing.T, err error)
	}{
		{"Push", func(s *EmbeddedDoltStore) error { return s.Push(ctx) }, wantStopRemote},
		{"ForcePush", func(s *EmbeddedDoltStore) error { return s.ForcePush(ctx) }, wantStopRemote},
		{"PushRemote", func(s *EmbeddedDoltStore) error { return s.PushRemote(ctx, defaultRemote, false) }, wantStopRemote},
		{"PushTo", func(s *EmbeddedDoltStore) error { return s.PushTo(ctx, defaultRemote) }, wantRemoteNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newPeerAuthTestStore(t)

			// newPeerAuthTestStore opens a bare store with no issue_prefix
			// configured yet; give it one and commit so the fixture itself
			// isn't the pending change this test is trying to isolate.
			if err := store.SetConfig(ctx, "issue_prefix", "fedauth"); err != nil {
				t.Fatalf("SetConfig(issue_prefix): %v", err)
			}
			if err := store.Commit(ctx, "bd init"); err != nil {
				t.Fatalf("Commit(bd init): %v", err)
			}

			var got presentedAuth
			captureRemoteEntryPoints(t, &got)

			beforeCreate, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit (before create): %v", err)
			}

			issue := &types.Issue{
				ID:        "fedauth-" + tc.name,
				Title:     "pending change",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}

			afterCreate, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit (after create): %v", err)
			}
			if afterCreate != beforeCreate {
				t.Fatalf("test invariant broken: CreateIssue committed on its own (HEAD moved %s -> %s) -- this test needs a genuinely pending change to be meaningful", beforeCreate, afterCreate)
			}

			tc.wantErr(t, tc.call(store))

			afterPush, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit (after %s): %v", tc.name, err)
			}
			if afterPush == afterCreate {
				t.Fatalf("%s must commit pending changes before pushing (#5433): HEAD did not move (%s), the created issue is still uncommitted", tc.name, afterPush)
			}
		})
	}
}
