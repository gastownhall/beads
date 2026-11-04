package issueops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// scopedRecheckTx opens a sqlmock transaction scoped for recheck recording.
func scopedRecheckTx(t *testing.T) DBTX {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(ScopeBlockedRecheckTransaction(tx))
	return tx
}

// TestBlockedRecheck_RecordsEveryUnblockingWrite pins the recording contract
// the stores rely on: ids are deduplicated across writes, an excluded id never
// reaches the recheck even when a later write names it, sources are labels
// joined in order, and Take clears the scope.
func TestBlockedRecheck_RecordsEveryUnblockingWrite(t *testing.T) {
	tx := scopedRecheckTx(t)

	noteBlockedRecheck(tx, "close of rb-a", []string{"rb-a"}, []string{"rb-a", "rb-c"}, nil)
	noteBlockedRecheck(tx, "dependency removal rb-c -> rb-b", nil, []string{"rb-c"}, []string{"rb-w"})
	noteBlockedRecheck(tx, deleteRecheckLabel([]string{"rb-b"}, ""), []string{"rb-b"}, []string{"rb-b", "rb-d"}, nil)
	noteBlockedRecheck(tx, "close of rb-a", []string{"rb-a"}, []string{"rb-c"}, nil)

	pending := TakeBlockedRecheck(tx)
	if want := []string{"rb-c", "rb-d"}; !slices.Equal(pending.IssueIDs, want) {
		t.Fatalf("IssueIDs = %v, want %v", pending.IssueIDs, want)
	}
	if want := []string{"rb-w"}; !slices.Equal(pending.WispIDs, want) {
		t.Fatalf("WispIDs = %v, want %v", pending.WispIDs, want)
	}
	if want := "bd: recheck blocked after close of rb-a, dependency removal rb-c -> rb-b, delete of rb-b"; pending.CommitMessage() != want {
		t.Fatalf("CommitMessage() = %q, want %q", pending.CommitMessage(), want)
	}
	if again := TakeBlockedRecheck(tx); !again.Empty() || len(again.Sources) != 0 {
		t.Fatalf("second Take returned %+v, want an empty scope", again)
	}
}

// TestBlockedRecheck_BoundsTheCommitMessage: one transaction can close or
// delete an unbounded number of issues, so the recheck's commit message names
// at most recheckSourceLimit writes and counts the rest — it keeps naming the
// ones it retained, because this commit is the only record of which write
// triggered the repair. A write that contributed no id to recheck is not named
// at all, since the recheck never touches anything on its behalf.
func TestBlockedRecheck_BoundsTheCommitMessage(t *testing.T) {
	tx := scopedRecheckTx(t)

	// A write whose only recomputed id is its own excluded row adds nothing.
	noteBlockedRecheck(tx, "close of rb-lonely", []string{"rb-lonely"}, []string{"rb-lonely"}, nil)
	if pending := TakeBlockedRecheck(tx); !pending.Empty() || len(pending.Sources) != 0 || pending.SourceCount != 0 {
		t.Fatalf("a write that recorded no id left %+v, want an untouched scope", pending)
	}

	for _, id := range []string{"rb-1", "rb-2", "rb-3", "rb-4"} {
		noteBlockedRecheck(tx, "close of blocker-"+id, nil, []string{id}, nil)
	}
	pending := TakeBlockedRecheck(tx)
	if pending.SourceCount != 4 || len(pending.Sources) != recheckSourceLimit {
		t.Fatalf("SourceCount = %d, Sources = %v, want 4 recorded writes and %d names", pending.SourceCount, pending.Sources, recheckSourceLimit)
	}
	want := "bd: recheck blocked after close of blocker-rb-1, close of blocker-rb-2, close of blocker-rb-3 and 1 more"
	if pending.CommitMessage() != want {
		t.Fatalf("CommitMessage() = %q, want %q", pending.CommitMessage(), want)
	}
}

// TestBlockedRecheck_CountsOnlyTheWritesItDidNotName: Sources is deduplicated
// and SourceCount is not, so two writes rendering one label (two equal-sized
// bulk deletes) leave the count ahead of the names. The message counts the
// writes it did not name rather than all of them, so that skew cannot make it
// claim more unnamed writes than exist — or drop the one name it has.
func TestBlockedRecheck_CountsOnlyTheWritesItDidNotName(t *testing.T) {
	tx := scopedRecheckTx(t)

	label := deleteRecheckLabel([]string{"rb-1", "rb-2", "rb-3", "rb-4"}, "")
	noteBlockedRecheck(tx, label, nil, []string{"rb-c"}, nil)
	noteBlockedRecheck(tx, label, nil, []string{"rb-d"}, nil)

	pending := TakeBlockedRecheck(tx)
	if pending.SourceCount != 2 || len(pending.Sources) != 1 {
		t.Fatalf("SourceCount = %d, Sources = %v, want 2 recorded writes deduplicated to 1 name", pending.SourceCount, pending.Sources)
	}
	if want := "bd: recheck blocked after delete of 4 issues and 1 more"; pending.CommitMessage() != want {
		t.Fatalf("CommitMessage() = %q, want %q", pending.CommitMessage(), want)
	}
}

// TestBlockedRecheck_UnscopedTransactionRecordsNothing: a transaction the
// store never scoped (or whose scope already ended) records and returns
// nothing, so the stores' Tx surfaces keep their pre-recheck behaviour.
func TestBlockedRecheck_UnscopedTransactionRecordsNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	noteBlockedRecheck(tx, "close of rb-a", []string{"rb-a"}, []string{"rb-c"}, nil)
	if pending := TakeBlockedRecheck(tx); !pending.Empty() {
		t.Fatalf("unscoped tx recorded %+v, want nothing", pending)
	}
	ScopeBlockedRecheckTransaction(nil)() // a nil tx is a no-op scope
}

func TestBlockedRecheck_Labels(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"single delete", deleteRecheckLabel([]string{"rb-b"}, ""), "delete of rb-b"},
		{"three named", deleteRecheckLabel([]string{"a", "b", "c"}, ""), "delete of a, b, c"},
		{"count beyond three", deleteRecheckLabel([]string{"a", "b", "c", "d"}, ""), "delete of 4 issues"},
		{"source repo scope", deleteRecheckLabel(make([]string, 40), "from github.com/x/y"), "delete of 40 issues from github.com/x/y"},
		{"close through update", statusChangeRecheckLabel("rb-b", "closed"), "close of rb-b"},
		{"pin through update", statusChangeRecheckLabel("rb-b", "pinned"), "status change of rb-b to pinned"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestBlockedRecheckContext_SurvivesItsCallerAndGuardsRecursion pins the two
// properties the stores' post-commit recheck depends on: it keeps running
// after the write's own context is cancelled (the write is already durable,
// so a cancelled caller must not skip the repair), and it marks itself so a
// recheck cannot start another recheck.
func TestBlockedRecheckContext_SurvivesItsCallerAndGuardsRecursion(t *testing.T) {
	caller, cancelCaller := context.WithCancel(t.Context())
	if InBlockedRecheck(caller) {
		t.Fatal("an ordinary write context reads as a recheck")
	}

	ctx, cancel := BlockedRecheckContext(caller)
	defer cancel()
	cancelCaller()

	if err := ctx.Err(); err != nil {
		t.Fatalf("recheck context died with its caller: %v", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("recheck context has no deadline of its own")
	}
	if !InBlockedRecheck(ctx) {
		t.Fatal("a recheck context does not report itself, so a recheck could start another")
	}
}

// TestBlockedRecheckFailed_KeepsSentinelAndCause: a store wraps a recheck
// failure so its log line tells a committed write whose recheck failed from a
// write that never landed, without losing the underlying cause.
func TestBlockedRecheckFailed_KeepsSentinelAndCause(t *testing.T) {
	cause := errors.New("dolt: connection reset")
	err := BlockedRecheckFailed(cause)
	if !errors.Is(err, ErrBlockedRecheckFailed) {
		t.Fatalf("errors.Is(err, ErrBlockedRecheckFailed) = false for %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false for %v", err)
	}
	if want := "blocked-state recheck after a committed write failed: dolt: connection reset"; err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
	if errors.Is(cause, ErrBlockedRecheckFailed) {
		t.Fatal("a bare cause must not read as a recheck failure")
	}
}

// TestBlockedRecheckFailureMessage_NamesTheStaleRowsAndTheRepair: the failure
// never reaches the write's caller, so this line plus the Dolt store's counter
// are its only trace. An operator reading it has to learn which rows went stale
// and what repairs them, and a bulk write must not turn one warning into an
// unbounded line.
func TestBlockedRecheckFailureMessage_NamesTheStaleRowsAndTheRepair(t *testing.T) {
	cause := BlockedRecheckFailed(errors.New("dolt: connection reset"))

	got := BlockedRecheckFailureMessage(BlockedRecheck{IssueIDs: []string{"rb-c"}, WispIDs: []string{"rb-w"}}, cause)
	want := "blocked-state recheck after a committed write failed: dolt: connection reset; " +
		"left unrechecked and possibly stale: rb-c, rb-w — repair with `bd recompute-blocked`"
	if got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}

	bulk := BlockedRecheck{IssueIDs: []string{"rb-1", "rb-2", "rb-3", "rb-4", "rb-5"}}
	got = BlockedRecheckFailureMessage(bulk, cause)
	if want := "left unrechecked and possibly stale: rb-1, rb-2, rb-3 and 2 more — repair with `bd recompute-blocked`"; !strings.HasSuffix(got, want) {
		t.Fatalf("message for %d ids = %q, want it to end with %q", len(bulk.IssueIDs), got, want)
	}
}
