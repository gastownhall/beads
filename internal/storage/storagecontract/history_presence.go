// Package storagecontract holds cross-store contracts for storage CAPABILITY
// interfaces — the narrow optional interfaces a store may implement and that
// callers reach by type-asserting after storage.UnwrapStore.
//
// It is deliberately not backend/conformance. That package is the ROLE tier:
// its Run*(t, ctx, <Name>Fixture) entrypoints are enumerated by a drift gate
// that demands a RoleContractBundle field and a wiring on every registered leg
// (internal/storage/contract_leg_registry_test.go). A capability that only two
// of those legs can implement — HistoryPresence needs Dolt history, which the
// unit-of-work leg has none of — would have to be waived there rather than
// described. Here the wiring list IS the claim.
package storagecontract

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// HistoryPresenceFixture supplies the store-specific hooks the contract needs.
// Every hook must act on the SAME store as Presence.
type HistoryPresenceFixture struct {
	// Prefix namespaces the ids each case seeds so several suites can share
	// one database.
	Prefix string
	// Presence is the capability under test.
	Presence storage.HistoryPresence
	// CreateIssue seeds one durable issue.
	CreateIssue func(ctx context.Context, id, title string) error
	// DeleteIssue hard-deletes one issue.
	DeleteIssue func(ctx context.Context, id string) error
	// Commit makes a Dolt commit of whatever is staged.
	Commit func(ctx context.Context, msg string) error
	// CurrentCommit returns HEAD.
	CurrentCommit func(ctx context.Context) (string, error)
	// ResetHard rewinds the branch to commit, discarding the working set. A
	// nil hook means the leg cannot rewind its own history, and the rewind
	// case SKIPS rather than passing quietly.
	ResetHard func(ctx context.Context, commit string) error
}

// RunHistoryPresenceContract runs every case in order against one fixture.
//
// The cases share a store and run sequentially on purpose: the last one
// rewinds the branch, which would pull the ground out from under any case
// still running. Each case names the exact ids it seeded.
func RunHistoryPresenceContract(t *testing.T, ctx context.Context, fx HistoryPresenceFixture) {
	t.Helper()
	t.Run("ReportsALiveCommittedID", func(t *testing.T) {
		RunHistoryPresenceReportsALiveCommittedID(t, ctx, fx)
	})
	t.Run("ReportsADeletedID", func(t *testing.T) {
		RunHistoryPresenceReportsADeletedID(t, ctx, fx)
	})
	t.Run("OmitsAnIDThatNeverExisted", func(t *testing.T) {
		RunHistoryPresenceOmitsAnIDThatNeverExisted(t, ctx, fx)
	})
	t.Run("OmitsAnUncommittedCreateAndDelete", func(t *testing.T) {
		RunHistoryPresenceOmitsAnUncommittedCreateAndDelete(t, ctx, fx)
	})
	t.Run("AnswersAnEmptyRequest", func(t *testing.T) {
		RunHistoryPresenceAnswersAnEmptyRequest(t, ctx, fx)
	})
	t.Run("OmitsARewoundAwayID", func(t *testing.T) {
		RunHistoryPresenceOmitsARewoundAwayID(t, ctx, fx)
	})
}

// RunHistoryPresenceReportsALiveCommittedID: an id that is committed and still
// in the store is in its history. The auto-export guard never asks about one —
// a live id is not a candidate — but a capability that answered "absent" here
// would be reporting deletions, not history.
func RunHistoryPresenceReportsALiveCommittedID(t *testing.T, ctx context.Context, fx HistoryPresenceFixture) {
	t.Helper()
	id := fx.Prefix + "-live"
	mustCreate(t, ctx, fx, id, "Live")
	mustCommit(t, ctx, fx, "seed live")

	assertPresence(t, ctx, fx, []string{id}, map[string]bool{id: true})
}

// RunHistoryPresenceReportsADeletedID is the proof auto-export depends on: an
// id whose creation was committed and which has since been deleted is in
// history and absent from the store — the signature of a real deletion.
func RunHistoryPresenceReportsADeletedID(t *testing.T, ctx context.Context, fx HistoryPresenceFixture) {
	t.Helper()
	id := fx.Prefix + "-gone"
	mustCreate(t, ctx, fx, id, "Doomed")
	mustCommit(t, ctx, fx, "seed doomed")
	if err := fx.DeleteIssue(ctx, id); err != nil {
		t.Fatalf("DeleteIssue(%s): %v", id, err)
	}
	mustCommit(t, ctx, fx, "delete doomed")

	assertPresence(t, ctx, fx, []string{id}, map[string]bool{id: true})
}

// RunHistoryPresenceOmitsAnIDThatNeverExisted is the fail-safe half: a JSONL
// line naming an id this store has never held is exactly GH#4988's torn-store
// signal, and must stay unproven.
func RunHistoryPresenceOmitsAnIDThatNeverExisted(t *testing.T, ctx context.Context, fx HistoryPresenceFixture) {
	t.Helper()
	id := fx.Prefix + "-neverexisted"

	assertPresence(t, ctx, fx, []string{id}, map[string]bool{id: false})
}

// RunHistoryPresenceOmitsAnUncommittedCreateAndDelete pins that history
// presence follows COMMITS, not the working set. This shape — created and
// deleted without either write reaching a commit — is the one no store-side
// proof can ever cover, which is why auto-export also carries an in-process
// delete handoff.
func RunHistoryPresenceOmitsAnUncommittedCreateAndDelete(t *testing.T, ctx context.Context, fx HistoryPresenceFixture) {
	t.Helper()
	id := fx.Prefix + "-working"

	before, err := fx.CurrentCommit(ctx)
	if err != nil {
		t.Fatalf("CurrentCommit before: %v", err)
	}
	mustCreate(t, ctx, fx, id, "Working set only")
	if err := fx.DeleteIssue(ctx, id); err != nil {
		t.Fatalf("DeleteIssue(%s): %v", id, err)
	}
	after, err := fx.CurrentCommit(ctx)
	if err != nil {
		t.Fatalf("CurrentCommit after: %v", err)
	}
	if before != after {
		t.Skipf("this store commits its own writes (HEAD moved %s → %s), so it has no uncommitted-create shape to assert about", before, after)
	}

	assertPresence(t, ctx, fx, []string{id}, map[string]bool{id: false})
}

// RunHistoryPresenceAnswersAnEmptyRequest: no ids in, no ids out, no error.
// The guard calls this only when it has candidates, but a capability that
// errored on an empty slice would push that precondition onto every caller.
func RunHistoryPresenceAnswersAnEmptyRequest(t *testing.T, ctx context.Context, fx HistoryPresenceFixture) {
	t.Helper()
	present, err := fx.Presence.HistoricalIssueIDs(ctx, nil)
	if err != nil {
		t.Fatalf("HistoricalIssueIDs(nil): %v", err)
	}
	if len(present) != 0 {
		t.Errorf("HistoricalIssueIDs(nil) = %v, want an empty set", present)
	}
}

// RunHistoryPresenceOmitsARewoundAwayID is the asymmetry that makes the whole
// capability safe to read as proof of deletion. dolt_history_issues walks
// HEAD's ancestry, so a hard reset to before an id's creation makes its
// commits unreachable and the id unproven — the store looks like it lost data,
// which is precisely what it did, and auto-export must keep refusing.
//
// Runs last: it rewinds the branch the earlier cases seeded.
func RunHistoryPresenceOmitsARewoundAwayID(t *testing.T, ctx context.Context, fx HistoryPresenceFixture) {
	t.Helper()
	if fx.ResetHard == nil {
		t.Skip("fixture cannot rewind history")
	}
	id := fx.Prefix + "-rewound"

	anchor, err := fx.CurrentCommit(ctx)
	if err != nil {
		t.Fatalf("CurrentCommit: %v", err)
	}
	mustCreate(t, ctx, fx, id, "Rewound")
	mustCommit(t, ctx, fx, "seed rewound")
	assertPresence(t, ctx, fx, []string{id}, map[string]bool{id: true})

	if err := fx.ResetHard(ctx, anchor); err != nil {
		t.Fatalf("ResetHard(%s): %v", anchor, err)
	}
	assertPresence(t, ctx, fx, []string{id}, map[string]bool{id: false})
}

func assertPresence(t *testing.T, ctx context.Context, fx HistoryPresenceFixture, ask []string, want map[string]bool) {
	t.Helper()
	present, err := fx.Presence.HistoricalIssueIDs(ctx, ask)
	if err != nil {
		t.Fatalf("HistoricalIssueIDs(%v): %v", ask, err)
	}
	for id, wantPresent := range want {
		_, got := present[id]
		if got != wantPresent {
			t.Errorf("HistoricalIssueIDs(%v): %s present=%v, want %v", ask, id, got, wantPresent)
		}
	}
	for id := range present {
		if _, asked := want[id]; !asked {
			t.Errorf("HistoricalIssueIDs(%v) returned %s, which was not asked about", ask, id)
		}
	}
}

func mustCreate(t *testing.T, ctx context.Context, fx HistoryPresenceFixture, id, title string) {
	t.Helper()
	if err := fx.CreateIssue(ctx, id, title); err != nil {
		t.Fatalf("CreateIssue(%s): %v", id, err)
	}
}

func mustCommit(t *testing.T, ctx context.Context, fx HistoryPresenceFixture, msg string) {
	t.Helper()
	if err := fx.Commit(ctx, msg); err != nil {
		t.Fatalf("Commit(%q): %v", msg, err)
	}
}
