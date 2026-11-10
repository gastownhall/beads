package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// residualFinding is the GH#5433 typed finding the CLI tests inject. Its
// table name reads "nothing to commit" on purpose: a string-only no-op
// classifier — the classification these tests exist to keep honest — would
// read the message and mis-swallow the finding as a healthy no-op.
func residualFinding() *storage.UncommittedChangesError {
	return &storage.UncommittedChangesError{
		Tables:    []string{"nothing to commit"},
		Patterns:  []string{"nothing to commit"},
		Committed: false,
	}
}

// uncommittedChangesStore serves the GH#5433 CLI tests: a store whose commit
// and push verbs report the typed residual finding, the way a stalled
// embedded store does once the commit-verification fix lands.
type uncommittedChangesStore struct {
	storage.DoltStorage
}

func (s *uncommittedChangesStore) CommitAll(context.Context, string) (bool, error) {
	return false, residualFinding()
}

func (s *uncommittedChangesStore) Commit(context.Context, string) error {
	return residualFinding()
}

func (s *uncommittedChangesStore) Push(context.Context) error {
	return fmt.Errorf("commit pending before push: %w", residualFinding())
}

func (s *uncommittedChangesStore) ForcePush(context.Context) error {
	return fmt.Errorf("commit pending before push: %w", residualFinding())
}

func (s *uncommittedChangesStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return []storage.RemoteInfo{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/o/r"}}, nil
}

func (s *uncommittedChangesStore) GetCurrentCommit(context.Context) (string, error) {
	return "c1", nil
}

// TestDoltCommitReportsUncommittedChanges: an explicit `bd dolt commit` that
// verifiably left tracked changes behind is an error, never the healthy
// "Nothing to commit." no-op — even when the residual finding's own table
// name reads like Dolt's no-op phrase, the classification the typed
// no-op rejection exists to guard (GH#5433 AC-4).
func TestDoltCommitReportsUncommittedChanges(t *testing.T) {
	saveAndRestoreGlobals(t)
	resetCommandContext()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	pinStorageMode(t, false)
	store = &uncommittedChangesStore{}
	if err := doltCommitCmd.Flags().Set("message", "probe"); err != nil {
		t.Fatalf("set --message: %v", err)
	}

	var runErr error
	_, stderr := captureStdio(t, func() { runErr = doltCommitCmd.RunE(doltCommitCmd, nil) })
	requireSilentExit(t, runErr, stderr)
	if !strings.Contains(stderr, "Error:") || !strings.Contains(stderr, "commit incomplete") {
		t.Errorf("stderr lacks the residual finding's Error line:\n%s", stderr)
	}
	if strings.Contains(stderr, "Nothing to commit.") {
		t.Errorf("the stalled commit was reported as a healthy no-op:\n%s", stderr)
	}
}

// TestDoltPushRejectsUncommittedChanges: a push whose pre-push CommitPending
// guard reports the typed finding stops with the Error line and never
// prints the success output (GH#5433 AC-4) — on both the plain and the
// --force route.
func TestDoltPushRejectsUncommittedChanges(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "push"
		if force {
			name = "push --force"
		}
		t.Run(name, func(t *testing.T) {
			saveAndRestoreGlobals(t)
			resetCommandContext()
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			pinStorageMode(t, false)
			store = &uncommittedChangesStore{}
			for flag, value := range map[string]string{"force": fmt.Sprintf("%v", force), "yes": "false", "no-adopt": "false", "remote": ""} {
				if err := doltPushCmd.Flags().Set(flag, value); err != nil {
					t.Fatalf("set --%s: %v", flag, err)
				}
			}
			t.Cleanup(func() { _ = doltPushCmd.Flags().Set("remote", "") })

			var runErr error
			_, stderr := captureStdio(t, func() { runErr = doltPushCmd.RunE(doltPushCmd, nil) })
			requireSilentExit(t, runErr, stderr)
			if !strings.Contains(stderr, "commit pending before push") || !strings.Contains(stderr, "commit incomplete") {
				t.Errorf("stderr lacks the guard's residual finding:\n%s", stderr)
			}
			if strings.Contains(stderr, "Push complete.") {
				t.Errorf("push printed success output despite the residual finding:\n%s", stderr)
			}
		})
	}
}

// TestMaybeAutoCommitStoreSurfacesUncommittedChanges: the auto-commit net
// must surface the typed finding under its existing caller policy instead of
// swallowing it as a tolerated no-op — the misleading table name included,
// which a string-only no-op classification would misread (GH#5433 AC-4).
func TestMaybeAutoCommitStoreSurfacesUncommittedChanges(t *testing.T) {
	saveStorageMode(t)
	serverMode = false
	proxiedServerMode = false

	fake := &uncommittedChangesStore{}
	setStore(fake)
	err := maybeAutoCommitStore(context.Background(), fake, doltAutoCommitParams{Command: "note"})
	if err == nil {
		t.Fatal("maybeAutoCommitStore swallowed the residual finding as a no-op, want it surfaced")
	}
	var residual *storage.UncommittedChangesError
	if !errors.As(err, &residual) {
		t.Fatalf("maybeAutoCommitStore error = %v, want the typed residual finding", err)
	}
	if len(residual.Tables) != 1 || residual.Tables[0] != "nothing to commit" {
		t.Fatalf("residual tables = %v, want the finding the store reported", residual.Tables)
	}
}
