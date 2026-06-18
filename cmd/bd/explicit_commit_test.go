//go:build cgo

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

type fakeExplicitCommitStore struct {
	storage.DoltStorage
	pending           bool
	pendingErr        error
	withConfigCalls   int
	withConfigMessage string
	withConfigErr     error
	pendingCalls      int
	pendingCommitted  bool
	pendingCommitErr  error
}

func (f *fakeExplicitCommitStore) HasPendingChanges(_ context.Context) (bool, error) {
	return f.pending, f.pendingErr
}

func (f *fakeExplicitCommitStore) CommitWithConfig(_ context.Context, message string) error {
	f.withConfigCalls++
	f.withConfigMessage = message
	return f.withConfigErr
}

func (f *fakeExplicitCommitStore) CommitPending(_ context.Context, _ string) (bool, error) {
	f.pendingCalls++
	return f.pendingCommitted, f.pendingCommitErr
}

// GH#4078: explicit user commits (bd dolt commit / bd vc commit) are
// intentional flush points — they must include the config table and report
// truthfully. The old path called Commit(), which excludes config and
// returns nil early on a config-only working set, printing success while
// committing nothing.

func TestExplicitCommitNoMessageUsesCommitPending(t *testing.T) {
	fake := &fakeExplicitCommitStore{pendingCommitted: true}

	committed, err := explicitDoltCommit(context.Background(), fake, "")
	if err != nil {
		t.Fatalf("explicitDoltCommit: %v", err)
	}
	if !committed {
		t.Fatal("committed = false, want true from CommitPending")
	}
	if fake.pendingCalls != 1 {
		t.Fatalf("CommitPending calls = %d, want 1", fake.pendingCalls)
	}
	if fake.withConfigCalls != 0 {
		t.Fatalf("CommitWithConfig calls = %d, want 0 on the no-message path", fake.withConfigCalls)
	}
}

func TestExplicitCommitWithMessageCommitsConfigInclusive(t *testing.T) {
	fake := &fakeExplicitCommitStore{pending: true}

	committed, err := explicitDoltCommit(context.Background(), fake, "checkpoint: my message")
	if err != nil {
		t.Fatalf("explicitDoltCommit: %v", err)
	}
	if !committed {
		t.Fatal("committed = false, want true when changes are pending")
	}
	if fake.withConfigCalls != 1 {
		t.Fatalf("CommitWithConfig calls = %d, want 1", fake.withConfigCalls)
	}
	if fake.withConfigMessage != "checkpoint: my message" {
		t.Fatalf("commit message = %q, want the user's message verbatim", fake.withConfigMessage)
	}
}

// The truthfulness half: a clean working set must report committed=false
// (the command prints "Nothing to commit"), never a fake success.
func TestExplicitCommitWithMessageNothingPendingIsTruthful(t *testing.T) {
	fake := &fakeExplicitCommitStore{pending: false}

	committed, err := explicitDoltCommit(context.Background(), fake, "checkpoint: my message")
	if err != nil {
		t.Fatalf("explicitDoltCommit: %v", err)
	}
	if committed {
		t.Fatal("committed = true with nothing pending — the GH#4078 silent-success lie")
	}
	if fake.withConfigCalls != 0 {
		t.Fatalf("CommitWithConfig calls = %d, want 0 with nothing pending", fake.withConfigCalls)
	}
}

// Embedded CommitWithConfig surfaces dolt's raw "nothing to commit" error
// (it does not swallow it like the server store) — treat it as the
// truthful not-committed outcome, not a failure.
func TestExplicitCommitNothingToCommitRaceReportsNotCommitted(t *testing.T) {
	fake := &fakeExplicitCommitStore{pending: true, withConfigErr: errors.New("nothing to commit")}

	committed, err := explicitDoltCommit(context.Background(), fake, "checkpoint: my message")
	if err != nil {
		t.Fatalf("explicitDoltCommit: %v, want nothing-to-commit treated as not-committed", err)
	}
	if committed {
		t.Fatal("committed = true on a nothing-to-commit result")
	}
}

func TestExplicitCommitSurfacesCommitError(t *testing.T) {
	fake := &fakeExplicitCommitStore{pending: true, withConfigErr: errors.New("connection refused")}

	committed, err := explicitDoltCommit(context.Background(), fake, "checkpoint: my message")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the commit error surfaced", err)
	}
	if committed {
		t.Fatal("committed = true on a failed commit")
	}
}

func TestExplicitCommitSurfacesPendingCheckError(t *testing.T) {
	fake := &fakeExplicitCommitStore{pendingErr: errors.New("dolt_status query failed")}

	_, err := explicitDoltCommit(context.Background(), fake, "checkpoint: my message")
	if err == nil || !strings.Contains(err.Error(), "dolt_status query failed") {
		t.Fatalf("err = %v, want the pending-check error surfaced", err)
	}
}
