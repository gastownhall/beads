package storagecontract

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// StateHasherFixture supplies the store-specific hooks the StateHasher
// contract needs. Every hook must act on the SAME store as Store.
type StateHasherFixture struct {
	// Prefix namespaces the ids each case seeds so several suites can share
	// one database.
	Prefix string
	// Store is the store under test, as callers see it before their type
	// assertion. It is deliberately untyped: a leg that lacks the capability
	// fails the contract's own assertion instead of the build, which is how
	// auto-export meets it too (storeStateHash falls back to HEAD).
	Store any
	// CreateIssue seeds one durable issue without making a Dolt commit.
	CreateIssue func(ctx context.Context, id, title string) error
	// ReadIssue reads one issue back through the store's normal read path.
	ReadIssue func(ctx context.Context, id string) error
	// Commit makes a Dolt commit of the working set.
	Commit func(ctx context.Context, msg string) error
	// CurrentCommit returns HEAD.
	CurrentCommit func(ctx context.Context) (string, error)
}

// RunStateHasherContract pins what auto-export's change detection relies on
// (GH#6955): the state hash moves on an uncommitted write, which HEAD cannot
// see, and holds still across reads and across a commit that leaves the
// working data as it was, so neither triggers a needless export.
func RunStateHasherContract(t *testing.T, ctx context.Context, fx StateHasherFixture) {
	t.Helper()
	hasher, ok := fx.Store.(storage.StateHasher)
	if !ok {
		t.Fatalf("%T does not implement storage.StateHasher: change detection falls back to HEAD and misses uncommitted writes", fx.Store)
	}
	stateHash := func(when string) string {
		t.Helper()
		hash, err := hasher.GetStateHash(ctx)
		if err != nil {
			t.Fatalf("GetStateHash %s: %v", when, err)
		}
		if hash == "" {
			t.Fatalf("GetStateHash %s returned an empty hash", when)
		}
		return hash
	}
	head := func(when string) string {
		t.Helper()
		commit, err := fx.CurrentCommit(ctx)
		if err != nil {
			t.Fatalf("CurrentCommit %s: %v", when, err)
		}
		return commit
	}

	id := fx.Prefix + "-statehash"
	headBefore := head("before the write")
	hashBefore := stateHash("before the write")

	if err := fx.CreateIssue(ctx, id, "Uncommitted write"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", id, err)
	}
	headAfterWrite := head("after the write")
	if headAfterWrite != headBefore {
		t.Fatalf("CreateIssue moved HEAD %s -> %s; the fixture must leave the write uncommitted", headBefore, headAfterWrite)
	}
	hashAfterWrite := stateHash("after the write")
	if hashAfterWrite == hashBefore {
		t.Fatalf("state hash stayed %s across an uncommitted write while HEAD stayed %s: change detection cannot see the write", hashBefore, headBefore)
	}

	if err := fx.ReadIssue(ctx, id); err != nil {
		t.Fatalf("ReadIssue(%s): %v", id, err)
	}
	if got := stateHash("after a read"); got != hashAfterWrite {
		t.Fatalf("a read moved the state hash %s -> %s", hashAfterWrite, got)
	}

	if err := fx.Commit(ctx, "commit the state-hash write"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := head("after the commit"); got == headAfterWrite {
		t.Fatalf("Commit left HEAD at %s; the fixture's commit hook did not commit", got)
	}
	if got := stateHash("after the commit"); got != hashAfterWrite {
		t.Fatalf("a commit that kept the working data moved the state hash %s -> %s", hashAfterWrite, got)
	}
}
