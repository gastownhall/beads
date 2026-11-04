//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestStateHasherContractTracksWorkingData is the embedded half of the shared
// storage.StateHasher contract (the server-backed half lives beside the
// server implementation in internal/storage/dolt): writing a durable issue
// without a version commit must move the state hash while HEAD stands still,
// and reads and a commit that preserve the working data must leave the hash
// stable. This is the hash auto-export's change detection relies on: under
// batch/off auto-commit an embedded write is durable but unversioned, and a
// HEAD-only token would let the automatic JSONL lag forever (GH#6955).
func TestStateHasherContractTracksWorkingData(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "sh")

	sh, ok := any(te.store).(storage.StateHasher)
	if !ok {
		t.Fatalf("EmbeddedDoltStore must implement storage.StateHasher: auto-export change detection must see uncommitted embedded writes, which HEAD alone cannot (GH#6955)")
	}

	// A clean baseline: flush anything setup left uncommitted so the hash
	// movement below is this test's own write, not the fixture's.
	if _, err := te.store.CommitAll(ctx, "test: flush baseline"); err != nil {
		t.Fatalf("baseline CommitAll: %v", err)
	}
	headBefore, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit (baseline): %v", err)
	}
	hashBefore, err := sh.GetStateHash(ctx)
	if err != nil {
		t.Fatalf("GetStateHash (baseline): %v", err)
	}

	// A durable issue written without a version commit — the everyday shape
	// of an embedded write under batch/off auto-commit.
	issue := &types.Issue{
		Title:     "uncommitted durable issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	headAfterCreate, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit (after create): %v", err)
	}
	if headAfterCreate != headBefore {
		t.Fatalf("CreateIssue committed on its own (HEAD moved %s -> %s); the contract test needs a genuinely uncommitted write", headBefore, headAfterCreate)
	}
	hashAfterCreate, err := sh.GetStateHash(ctx)
	if err != nil {
		t.Fatalf("GetStateHash (after create): %v", err)
	}
	if hashAfterCreate == hashBefore {
		t.Fatalf("GetStateHash stayed %q across an uncommitted durable write; HEAD-based change detection would never notice it (GH#6955)", hashBefore)
	}

	// Subsequent reads must not move the working-data hash.
	if _, err := te.store.GetIssue(ctx, issue.ID); err != nil {
		t.Fatalf("GetIssue(%q): %v", issue.ID, err)
	}
	if _, err := te.store.SearchIssues(ctx, "uncommitted", types.IssueFilter{}); err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	hashAfterReads, err := sh.GetStateHash(ctx)
	if err != nil {
		t.Fatalf("GetStateHash (after reads): %v", err)
	}
	if hashAfterReads != hashAfterCreate {
		t.Fatalf("reads changed the state hash: %q -> %q (reads are not changes)", hashAfterCreate, hashAfterReads)
	}

	// A commit that preserves the working data: history advances, the
	// working-data hash does not.
	if err := te.store.Commit(ctx, "commit the uncommitted durable issue"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	headAfterCommit, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit (after commit): %v", err)
	}
	if headAfterCommit == headBefore {
		t.Fatalf("Commit did not advance HEAD from %s", headBefore)
	}
	hashAfterCommit, err := sh.GetStateHash(ctx)
	if err != nil {
		t.Fatalf("GetStateHash (after commit): %v", err)
	}
	if hashAfterCommit != hashAfterReads {
		t.Fatalf("a commit preserving the working data changed the state hash: %q -> %q", hashAfterReads, hashAfterCommit)
	}

	// And one more read round after the commit, to pin stability end to end.
	if _, err := te.store.GetIssue(ctx, issue.ID); err != nil {
		t.Fatalf("GetIssue(%q) after commit: %v", issue.ID, err)
	}
	hashFinal, err := sh.GetStateHash(ctx)
	if err != nil {
		t.Fatalf("GetStateHash (final): %v", err)
	}
	if hashFinal != hashAfterCommit {
		t.Fatalf("state hash not stable after post-commit reads: %q -> %q", hashAfterCommit, hashFinal)
	}
}
