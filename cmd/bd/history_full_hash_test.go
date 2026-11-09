package main

import (
	"context"
	"encoding/json"
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
	events  []*types.Event
}

func (f *fakeHistoryBackend) History(_ context.Context, _ string) ([]*storage.HistoryEntry, error) {
	return f.entries, nil
}

func (f *fakeHistoryBackend) IterEvents(_ context.Context, _ string, _ int) (storage.Iter[types.Event], error) {
	return storage.NewSliceIter(f.events), nil
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

// TestHistoryTextOutputPrintsRetainedCloseReason pins GH#5593's history
// half: plain-text history prints a retained committed snapshot's close
// reason, while empty close reasons, nil snapshots, --json, --limit and the
// --events view keep their exact prior behavior. Reopening clears the
// current close fields; the committed snapshots are where the reason
// survives, and `Close reason:` is the only line the renderer adds.
func TestHistoryTextOutputPrintsRetainedCloseReason(t *testing.T) {
	entry := func(reason string, issue *types.Issue) *storage.HistoryEntry {
		return &storage.HistoryEntry{
			CommitHash: "0123456789abcdefghijklmnopqrstuv",
			Committer:  "test-user",
			CommitDate: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Issue:      issue,
		}
	}
	withReason := entry("not reproducible", &types.Issue{
		ID: "cr-1", Title: "closed issue", Priority: 2,
		Status: types.StatusClosed, CloseReason: "not reproducible",
	})
	withoutReason := entry("", &types.Issue{
		ID: "cr-2", Title: "closed without a reason", Priority: 2,
		Status: types.StatusClosed,
	})
	nilSnapshot := entry("", nil)
	events := []*types.Event{{
		ID: "ev-1", IssueID: "cr-1", EventType: types.EventReopened, Actor: "writer",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
	}}

	textHistory := func(backend historyBackend, limit int, showEvents bool) string {
		t.Helper()
		return captureStdout(t, func() error {
			return runHistory(context.Background(), backend, "cr-1", limit, showEvents)
		})
	}
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	t.Run("snapshot_with_close_reason_prints_it", func(t *testing.T) {
		out := textHistory(&fakeHistoryBackend{entries: []*storage.HistoryEntry{withReason}}, 0, false)
		if !strings.Contains(out, "Close reason: not reproducible") {
			t.Errorf("plain history does not print the retained close reason; got:\n%s", out)
		}
	})

	t.Run("snapshot_without_close_reason_prints_no_line", func(t *testing.T) {
		out := textHistory(&fakeHistoryBackend{entries: []*storage.HistoryEntry{withoutReason}}, 0, false)
		if strings.Contains(out, "Close reason") {
			t.Errorf("empty close reason must print no close-reason line; got:\n%s", out)
		}
	})

	t.Run("nil_snapshot_renders_without_panic", func(t *testing.T) {
		out := textHistory(&fakeHistoryBackend{entries: []*storage.HistoryEntry{nilSnapshot}}, 0, false)
		if strings.Contains(out, "Close reason") {
			t.Errorf("nil snapshot must print no close-reason line; got:\n%s", out)
		}
	})

	t.Run("limit_keeps_and_drops_close_reasons_with_their_snapshots", func(t *testing.T) {
		// Newest first: the newest snapshot carries the reason, the two
		// older ones do not, so --limit 2 keeps the line and --limit 1
		// on the reversed order drops it.
		newestFirst := []*storage.HistoryEntry{withReason, withoutReason, nilSnapshot}
		if out := textHistory(&fakeHistoryBackend{entries: newestFirst}, 2, false); !strings.Contains(out, "Close reason: not reproducible") {
			t.Errorf("limit 2 must keep the newest snapshot's close reason; got:\n%s", out)
		}
		if out := textHistory(&fakeHistoryBackend{entries: []*storage.HistoryEntry{nilSnapshot, withoutReason, withReason}}, 1, false); strings.Contains(out, "Close reason") {
			t.Errorf("limit 1 must drop the close reason with its snapshot; got:\n%s", out)
		}
	})

	t.Run("json_output_is_unchanged", func(t *testing.T) {
		jsonOutput = true
		defer func() { jsonOutput = false }()
		out := textHistory(&fakeHistoryBackend{entries: []*storage.HistoryEntry{withReason}}, 0, false)
		if strings.Contains(out, "Close reason") {
			t.Errorf("JSON output must not gain a close-reason prose line; got:\n%s", out)
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entries); err != nil {
			t.Fatalf("history --json is not a JSON array: %v\n%s", err, out)
		}
		if len(entries) != 1 {
			t.Fatalf("history --json has %d entries, want 1:\n%s", len(entries), out)
		}
		// The snapshot's close_reason was always part of the Issue JSON;
		// the renderer change adds nothing to it.
		var issue struct {
			CloseReason string `json:"close_reason"`
		}
		if err := json.Unmarshal(entries[0]["Issue"], &issue); err != nil {
			t.Fatalf("snapshot Issue is not JSON: %v\n%s", err, entries[0]["Issue"])
		}
		if issue.CloseReason != "not reproducible" {
			t.Errorf("snapshot close_reason = %q, want %q", issue.CloseReason, "not reproducible")
		}
	})

	t.Run("events_view_is_unchanged", func(t *testing.T) {
		out := textHistory(&fakeHistoryBackend{events: events}, 0, true)
		if !strings.Contains(out, "History events for") {
			t.Errorf("expected the events view header; got:\n%s", out)
		}
		if strings.Contains(out, "Close reason") {
			t.Errorf("the events view must not print close-reason lines; got:\n%s", out)
		}
	})
}
