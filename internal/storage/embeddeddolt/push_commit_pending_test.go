//go:build cgo

package embeddeddolt

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
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
		// clean runs the verb with nothing pending instead of creating an issue
		// first. The guard must tolerate an empty working set -- CommitPending
		// -> commitAll(tolerateEmpty=true) maps Dolt's "nothing to commit" to
		// (false, nil) -- and still reach the remote entry point with HEAD
		// unmoved. That is the common case for a push against an
		// already-committed store, and nothing else in this package covers it.
		clean bool
	}{
		{name: "Push", call: func(s *EmbeddedDoltStore) error { return s.Push(ctx) }, wantErr: wantStopRemote},
		{name: "ForcePush", call: func(s *EmbeddedDoltStore) error { return s.ForcePush(ctx) }, wantErr: wantStopRemote},
		{name: "PushRemote", call: func(s *EmbeddedDoltStore) error { return s.PushRemote(ctx, defaultRemote, false) }, wantErr: wantStopRemote},
		{name: "PushTo", call: func(s *EmbeddedDoltStore) error { return s.PushTo(ctx, defaultRemote) }, wantErr: wantRemoteNotFound},
		{name: "PushCleanStore", call: func(s *EmbeddedDoltStore) error { return s.Push(ctx) }, wantErr: wantStopRemote, clean: true},
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

			beforePush := beforeCreate

			if !tc.clean {
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
				beforePush = afterCreate
			}

			tc.wantErr(t, tc.call(store))

			afterPush, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit (after %s): %v", tc.name, err)
			}
			if tc.clean {
				if afterPush != beforePush {
					t.Fatalf("%s on a clean store must not create a commit: HEAD moved %s -> %s (the guard's tolerateEmpty tolerance is broken)", tc.name, beforePush, afterPush)
				}
				return
			}
			if afterPush == beforePush {
				t.Fatalf("%s must commit pending changes before pushing (#5433): HEAD did not move (%s), the created issue is still uncommitted", tc.name, afterPush)
			}
		})
	}
}

// TestRemoteOperationsRejectUncommittedChanges is the GH#5433 remote half:
// with an enabled pattern covering the tracked issues table and its latest
// write stranded in the working set, every push and pull verb's
// CommitPending guard returns the typed residual finding and stops there,
// before any remote entry point runs. PushTo and PullFrom included: their
// nonexistent-peer failure must not preempt the finding. Each verb gets its
// own store so one verb's outcome cannot mask another's.
func TestRemoteOperationsRejectUncommittedChanges(t *testing.T) {
	ctx := t.Context()

	cases := []struct {
		name string
		call func(*EmbeddedDoltStore) error
	}{
		{name: "Push", call: func(s *EmbeddedDoltStore) error { return s.Push(ctx) }},
		{name: "ForcePush", call: func(s *EmbeddedDoltStore) error { return s.ForcePush(ctx) }},
		{name: "PushRemote", call: func(s *EmbeddedDoltStore) error { return s.PushRemote(ctx, defaultRemote, false) }},
		{name: "PushRemoteForce", call: func(s *EmbeddedDoltStore) error { return s.PushRemote(ctx, defaultRemote, true) }},
		{name: "PushTo", call: func(s *EmbeddedDoltStore) error { return s.PushTo(ctx, "no-such-peer") }},
		{name: "Pull", call: func(s *EmbeddedDoltStore) error { return s.Pull(ctx) }},
		{name: "PullWithStrategy", call: func(s *EmbeddedDoltStore) error { return s.PullWithStrategy(ctx, "ours") }},
		{name: "PullRemote", call: func(s *EmbeddedDoltStore) error { return s.PullRemote(ctx, defaultRemote) }},
		{name: "PullRemoteWithStrategy", call: func(s *EmbeddedDoltStore) error { return s.PullRemoteWithStrategy(ctx, defaultRemote, "theirs") }},
		{name: "PullFrom", call: func(s *EmbeddedDoltStore) error { _, err := s.PullFrom(ctx, "no-such-peer"); return err }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			const database = "stallrem"
			store, err := Open(ctx, beadsDir, database, "main")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			rawSQL := func(t *testing.T, query string, args []any, dest ...any) {
				t.Helper()
				db, cleanup, err := OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), database, "main")
				if err != nil {
					t.Fatalf("OpenSQL: %v", err)
				}
				defer func() { _ = cleanup() }()
				if len(dest) == 0 {
					_, err = db.ExecContext(ctx, query, args...)
				} else {
					err = db.QueryRowContext(ctx, query, args...).Scan(dest...)
				}
				if err != nil {
					t.Fatalf("%q: %v", query, err)
				}
			}

			if err := store.SetConfig(ctx, "issue_prefix", "stall"); err != nil {
				t.Fatalf("SetConfig(issue_prefix): %v", err)
			}
			if err := store.Commit(ctx, "bd init"); err != nil {
				t.Fatalf("Commit(bd init): %v", err)
			}

			// The stall fixture: a tracked issue, a noncanonical pattern over
			// issues, and a write the pattern keeps out of every staging.
			issue := &types.Issue{
				ID:        "stall-" + tc.name,
				Title:     "tracked at head",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			if err := store.Commit(ctx, "test: track the issue"); err != nil {
				t.Fatalf("Commit(track the issue): %v", err)
			}
			rawSQL(t, "INSERT INTO dolt_ignore (pattern, ignored) VALUES ('issues', true)", nil)
			if err := store.Commit(ctx, "test: register ignore pattern"); err != nil {
				t.Fatalf("Commit(register ignore pattern): %v", err)
			}
			rawSQL(t, "UPDATE issues SET title = 'stranded' WHERE id = ?", []any{issue.ID})

			headBefore, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit: %v", err)
			}

			var got presentedAuth
			captureRemoteEntryPoints(t, &got)

			verbErr := tc.call(store)
			var residual *storage.UncommittedChangesError
			if !errors.As(verbErr, &residual) {
				t.Fatalf("%s in the stall = %v, want the typed residual finding through the guard", tc.name, verbErr)
			}
			if errors.Is(verbErr, errStopRemote) || got.remote != "" {
				t.Fatalf("%s reached its remote entry point (remote %q) despite the stall; the CommitPending guard must stop it before transport", tc.name, got.remote)
			}
			if len(residual.Tables) != 1 || residual.Tables[0] != "issues" || residual.Committed {
				t.Fatalf("%s residual = %+v, want exactly [issues] with Committed=false", tc.name, residual)
			}

			headAfter, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit after %s: %v", tc.name, err)
			}
			if headAfter != headBefore {
				t.Fatalf("%s moved HEAD across the refusal: %s -> %s", tc.name, headBefore, headAfter)
			}
			var working, head string
			rawSQL(t, "SELECT title FROM issues WHERE id = ?", []any{issue.ID}, &working)
			rawSQL(t, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", []any{issue.ID}, &head)
			if working != "stranded" || head != "tracked at head" {
				t.Fatalf("after %s issue title working=%q HEAD=%q, want working=stranded HEAD=%q", tc.name, working, head, "tracked at head")
			}
		})
	}
}
