package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// fakeHistoryBackend is a minimal historyBackend for exercising runHistory
// without a real store.
type fakeHistoryBackend struct {
	entries []*storage.HistoryEntry
}

func (f *fakeHistoryBackend) History(_ context.Context, _ string) ([]*storage.HistoryEntry, error) {
	return f.entries, nil
}

func (f *fakeHistoryBackend) IterEvents(_ context.Context, _ string, _ int) (storage.Iter[types.Event], error) {
	return nil, nil
}

// TestHistoryTextOutputShowsFullCommitHash covers be-95c0y FIX A: text-mode
// `bd history` output must print the full Dolt commit hash, not just its
// first 8 characters -- an 8-char prefix is not valid input to `bd show
// --as-of` (Dolt's AS OF does not resolve hash prefixes), so truncating it
// here hands the user a value that looks copyable but always fails.
func TestHistoryTextOutputShowsFullCommitHash(t *testing.T) {
	const fullHash = "0123456789abcdefghijklmnopqrstuv" // 32 chars, Dolt's [0-9a-v] alphabet
	if len(fullHash) != 32 {
		t.Fatalf("test fixture bug: fullHash is %d chars, want 32", len(fullHash))
	}

	backend := &fakeHistoryBackend{
		entries: []*storage.HistoryEntry{
			{
				CommitHash: fullHash,
				Committer:  "test-user",
				CommitDate: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				Issue: &types.Issue{
					ID:       "test-1",
					Title:    "test issue",
					Priority: 2,
					Status:   types.StatusOpen,
				},
			},
		},
	}

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return runHistory(context.Background(), backend, "test-1", 0, false)
	})

	if !strings.Contains(out, fullHash) {
		t.Errorf("bd history text output does not contain the full commit hash %q; got:\n%s", fullHash, out)
	}
	if strings.Contains(out, fullHash[:8]) && !strings.Contains(out, fullHash) {
		t.Errorf("bd history text output contains only the truncated 8-char hash %q, not the full hash", fullHash[:8])
	}
}

// TestHistoryTextOutputShowsSnapshotCloseReason pins GH#5593: reopening clears
// the live close_reason, so the retained closed snapshot is where the reason
// survives. Plain `bd history` must print it under that snapshot, and print no
// close-reason line for the reopened snapshot whose reason is empty.
func TestHistoryTextOutputShowsSnapshotCloseReason(t *testing.T) {
	const closeReason = "CLOSE_REASON_MARKER"
	snapshot := func(hash string, status types.Status, reason string) *storage.HistoryEntry {
		return &storage.HistoryEntry{
			CommitHash: hash,
			Committer:  "test-user",
			CommitDate: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Issue: &types.Issue{
				ID:          "test-1",
				Title:       "test issue",
				Priority:    2,
				Status:      status,
				CloseReason: reason,
			},
		}
	}
	backend := &fakeHistoryBackend{
		entries: []*storage.HistoryEntry{
			snapshot("reopenedreopenedreopenedreopened", types.StatusOpen, ""),
			snapshot("closedclosedclosedclosedclosedcl", types.StatusClosed, closeReason),
		},
	}

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return runHistory(context.Background(), backend, "test-1", 0, false)
	})

	if got := strings.Count(out, "Close reason:"); got != 1 {
		t.Fatalf("bd history printed %d close-reason lines, want 1 (closed snapshot only); got:\n%s", got, out)
	}
	closedAt := strings.Index(out, "closedclosedclosedclosedclosedcl")
	reasonAt := strings.Index(out, "  Close reason: "+closeReason+"\n")
	if closedAt < 0 || reasonAt < closedAt {
		t.Errorf("bd history did not print %q under the closed snapshot; got:\n%s", "Close reason: "+closeReason, out)
	}
}
