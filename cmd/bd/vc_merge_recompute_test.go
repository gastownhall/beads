package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// mergeRecomputeFakeStore is a bare fake for the resolve-then-recompute seam
// (resolveMergeConflictsAndRecompute): only the methods that seam calls are
// implemented, everything else panics via the nil embedded DoltStorage if
// ever reached.
type mergeRecomputeFakeStore struct {
	storage.DoltStorage
	resolveErr error
	commitErr  error
	recompute  *fakeRecomputer // nil => backend does not support recompute
	resolved   []string
	committed  string
}

// fakeRecomputer is the optional RecomputeBlockedAfterMerge surface, kept
// separate from mergeRecomputeFakeStore so storage.UnwrapStore's assertion
// only finds it when the test wires it in — proving the unsupported-backend
// branch is reachable with a store that has no recompute method at all.
type fakeRecomputer struct {
	err   error
	calls []string
}

func (r *fakeRecomputer) RecomputeBlockedAfterMerge(_ context.Context, fromCommit string) error {
	r.calls = append(r.calls, fromCommit)
	return r.err
}

type mergeRecomputeFakeStoreWithRecompute struct {
	*mergeRecomputeFakeStore
	*fakeRecomputer
}

func (s *mergeRecomputeFakeStore) ResolveConflicts(_ context.Context, table, _ string) error {
	if s.resolveErr != nil {
		return s.resolveErr
	}
	s.resolved = append(s.resolved, table)
	return nil
}

func (s *mergeRecomputeFakeStore) CommitMergeResolution(_ context.Context, message string) error {
	if s.commitErr != nil {
		return s.commitErr
	}
	s.committed = message
	return nil
}

func withMergeRecomputeStore(t *testing.T, st storage.DoltStorage) {
	t.Helper()
	old := store
	store = st
	t.Cleanup(func() { store = old })
}

// The regression this pins: reverting resolveMergeConflictsAndRecompute's
// call site back to an inline assertion straight on `store` (skipping
// blockedAfterMergeRecomputerFor's UnwrapStore) makes this fail exactly like
// vc_recompute_test.go's decorated-chain cases, and dropping the else branch
// makes the warning assertion below fail silently instead (F4, wy-9k58l).
func TestResolveMergeConflictsAndRecompute_CallsRecomputeThroughHelper(t *testing.T) {
	rec := &fakeRecomputer{}
	fake := &mergeRecomputeFakeStore{}
	withMergeRecomputeStore(t, &mergeRecomputeFakeStoreWithRecompute{fake, rec})

	conflicts := []storage.Conflict{{IssueID: "wy-1", Field: "title"}, {IssueID: "wy-2"}}
	if err := resolveMergeConflictsAndRecompute(context.Background(), "feature", conflicts, "ours", "deadbeef"); err != nil {
		t.Fatalf("resolveMergeConflictsAndRecompute: %v", err)
	}
	if want := []string{"title", "issues"}; len(fake.resolved) != len(want) || fake.resolved[0] != want[0] || fake.resolved[1] != want[1] {
		t.Errorf("resolved tables = %v, want %v (empty Field defaults to \"issues\")", fake.resolved, want)
	}
	if fake.committed == "" {
		t.Errorf("CommitMergeResolution was not called")
	}
	if len(rec.calls) != 1 || rec.calls[0] != "deadbeef" {
		t.Errorf("recompute calls = %v, want one call with fromCommit \"deadbeef\"", rec.calls)
	}
}

// A store without the optional interface must warn on stderr, not skip in
// silence — the old `if ok` with no else (F4/F6, wy-9k58l).
func TestResolveMergeConflictsAndRecompute_UnsupportedBackendWarns(t *testing.T) {
	fake := &mergeRecomputeFakeStore{}
	withMergeRecomputeStore(t, fake)

	stderr := captureStderr(t, func() {
		if err := resolveMergeConflictsAndRecompute(context.Background(), "feature", []storage.Conflict{{Field: "title"}}, "ours", "deadbeef"); err != nil {
			t.Fatalf("resolveMergeConflictsAndRecompute: %v", err)
		}
	})
	if !containsAll(stderr, "Warning:", "cannot recompute is_blocked", "recompute-blocked") {
		t.Fatalf("stderr = %q; want the unsupported-backend warning", stderr)
	}
}

// A resolve failure must stop before commit and before recompute.
func TestResolveMergeConflictsAndRecompute_ResolveErrorStopsEarly(t *testing.T) {
	rec := &fakeRecomputer{}
	fake := &mergeRecomputeFakeStore{resolveErr: errors.New("resolve boom")}
	withMergeRecomputeStore(t, &mergeRecomputeFakeStoreWithRecompute{fake, rec})

	err := resolveMergeConflictsAndRecompute(context.Background(), "feature", []storage.Conflict{{Field: "title"}}, "ours", "deadbeef")
	if err == nil {
		t.Fatal("expected an error")
	}
	if fake.committed != "" || len(rec.calls) != 0 {
		t.Fatalf("resolve failed but commit=%q recompute calls=%v", fake.committed, rec.calls)
	}
}

// A recompute failure must surface as an error, not a swallowed warning.
func TestResolveMergeConflictsAndRecompute_RecomputeErrorPropagates(t *testing.T) {
	rec := &fakeRecomputer{err: errors.New("recompute boom")}
	fake := &mergeRecomputeFakeStore{}
	withMergeRecomputeStore(t, &mergeRecomputeFakeStoreWithRecompute{fake, rec})

	err := resolveMergeConflictsAndRecompute(context.Background(), "feature", []storage.Conflict{{Field: "title"}}, "ours", "deadbeef")
	if err == nil || !containsAll(err.Error(), "recompute boom") {
		t.Fatalf("err = %v, want it to wrap the recompute error", err)
	}
}
