//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// misleadingResidual is a GH#5433 finding whose table name holds Dolt's own
// no-op words, so a message match alone would mistake it for "nothing to
// commit".
func misleadingResidual(committed bool) *storage.UncommittedChangesError {
	return &storage.UncommittedChangesError{Tables: []string{"nothing to commit"}, Committed: committed}
}

// commitAllStore answers CommitAll with a fixed result.
type commitAllStore struct {
	storage.DoltStorage
	committed bool
	err       error
}

func (s *commitAllStore) CommitAll(context.Context, string) (bool, error) {
	return s.committed, s.err
}

func (s *commitAllStore) GetCurrentCommit(context.Context) (string, error) {
	return "c1", nil
}

func requireUncommittedChangesExit(t *testing.T, err error, stdout, stderr string) {
	t.Helper()
	var exit *exitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("RunE = %v, want exit 1\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "commit incomplete") {
		t.Errorf("stderr lacks the residual-change report:\n%s", stderr)
	}
}

// TestDoltCommitReportsUncommittedChanges: bd dolt commit and bd vc commit
// fail on a commit that left tracked changes behind, refused or partial,
// instead of printing a no-op or a success.
func TestDoltCommitReportsUncommittedChanges(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := map[bool]string{false: "refused", true: "partial"}[committed]
		t.Run("dolt commit/"+name, func(t *testing.T) {
			saveStorageMode(t)
			serverMode, proxiedServerMode = false, false
			store = &commitAllStore{committed: committed, err: misleadingResidual(committed)}
			var runErr error
			stdout, stderr := captureStdio(t, func() { runErr = doltCommitCmd.RunE(doltCommitCmd, nil) })
			requireUncommittedChangesExit(t, runErr, stdout, stderr)
			for _, unwanted := range []string{"Nothing to commit.", "Committed."} {
				if strings.Contains(stdout, unwanted) {
					t.Errorf("stdout has %q:\n%s", unwanted, stdout)
				}
			}
		})
		t.Run("vc commit/"+name, func(t *testing.T) {
			saveStorageMode(t)
			serverMode, proxiedServerMode = false, false
			oldMsg, oldJSON, oldCtx := vcCommitMessage, jsonOutput, rootCtx
			t.Cleanup(func() { vcCommitMessage, jsonOutput, rootCtx = oldMsg, oldJSON, oldCtx })
			vcCommitMessage, jsonOutput, rootCtx = "test: commit", false, context.Background()
			store = &commitAllStore{committed: committed, err: misleadingResidual(committed)}
			var runErr error
			stdout, stderr := captureStdio(t, func() { runErr = vcCommitCmd.RunE(vcCommitCmd, nil) })
			requireUncommittedChangesExit(t, runErr, stdout, stderr)
			for _, unwanted := range []string{"Nothing to commit", "Created commit"} {
				if strings.Contains(stdout, unwanted) {
					t.Errorf("stdout has %q:\n%s", unwanted, stdout)
				}
			}
		})
	}
}

// TestDoltPushRejectsUncommittedChanges: an embedded push, plain or
// --force, stops at its CommitPending guard when the commit left tracked
// changes behind, so the push fails with the report and never prints
// "Push complete.".
func TestDoltPushRejectsUncommittedChanges(t *testing.T) {
	for _, tc := range []struct {
		remote string
		force  bool
	}{{"", false}, {"backup", false}, {"", true}, {"backup", true}} {
		t.Run(fmt.Sprintf("remote=%s/force=%v", tc.remote, tc.force), func(t *testing.T) {
			st := &failingPushStore{pushErr: misleadingResidual(true)}
			saveAndRestoreGlobals(t)
			resetCommandContext()
			pinStorageMode(t, false)
			store = st
			for name, value := range map[string]string{"force": fmt.Sprint(tc.force), "yes": "false", "no-adopt": "true", "remote": tc.remote} {
				if err := doltPushCmd.Flags().Set(name, value); err != nil {
					t.Fatalf("set --%s: %v", name, err)
				}
			}
			t.Cleanup(func() {
				_ = doltPushCmd.Flags().Set("remote", "")
				_ = doltPushCmd.Flags().Set("no-adopt", "false")
				_ = doltPushCmd.Flags().Set("force", "false")
			})
			var runErr error
			stdout, stderr := captureStdio(t, func() { runErr = doltPushCmd.RunE(doltPushCmd, nil) })
			requireUncommittedChangesExit(t, runErr, stdout, stderr)
			if strings.Contains(stdout, "Push complete.") {
				t.Errorf("stdout claims success:\n%s", stdout)
			}
		})
	}
}

// TestMaybeAutoCommitStoreSurfacesUncommittedChanges: the auto-commit after
// a write returns the residual-change report to its caller (whose policy
// decides warn or fail) instead of absorbing it as a no-op.
func TestMaybeAutoCommitStoreSurfacesUncommittedChanges(t *testing.T) {
	saveStorageMode(t)
	serverMode, proxiedServerMode = false, false
	want := misleadingResidual(false)
	fake := &fakeCommitPendingStore{err: want}
	err := maybeAutoCommitStore(context.Background(), fake, doltAutoCommitParams{Command: "note"})
	var residual *storage.UncommittedChangesError
	if !errors.As(err, &residual) {
		t.Fatalf("maybeAutoCommitStore error = %v, want *storage.UncommittedChangesError", err)
	}
	if fake.commitCalls != 1 {
		t.Fatalf("Commit calls = %d, want 1", fake.commitCalls)
	}
}
