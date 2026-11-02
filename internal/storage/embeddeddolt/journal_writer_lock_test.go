//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournal_EmbeddedWritesTakeNoServerLock pins AC-3 of
// gastownhall/beads#6117 for the embedded engine: a journaled write in
// embedded mode retains its journal-enabled behavior — it succeeds promptly
// and records exactly its journal row — with NO journal-writer admission.
//
// The no-admission claim is structural: the embedded store never issues
// journal-lock SQL (the admission helpers in internal/storage/issueops have
// no call site under internal/storage/embeddeddolt), and this file pins the
// behavior half of the retention — a journaled embedded write that DID queue
// on an admission would blow the short deadline below.
//
// A live GET_LOCK probe was tried and discarded: the embedded engine answers
// GET_LOCK, and a second engine session holding one stalls the store's
// commit (the engine serializes on it), so the probe cannot tell "takes no
// lock" from engine-level serialization. Nothing about that quirk can make
// an admission here safe: the store would have to CALL GET_LOCK to queue, and
// it does not.
func TestEventsJournal_EmbeddedWritesTakeNoServerLock(t *testing.T) {
	env := newTestEnv(t, "ejl")
	store := env.store
	store.SetEventsJournalEnabled(true)

	// A journaled embedded write completes inside this short window — no
	// admission queue — and journals exactly its one create record.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	issue := &types.Issue{ID: "ejl-1", Title: "t", IssueType: types.TypeTask, Status: types.StatusOpen}
	if err := store.CreateIssue(ctx, issue, "actor"); err != nil {
		t.Fatalf("embedded journaled write failed: %v", err)
	}
	seqs := readJournalSeqs(t, env)
	if len(seqs) != 1 {
		t.Fatalf("journal rows = %d, want 1 (journal-enabled behavior retained)", len(seqs))
	}
}