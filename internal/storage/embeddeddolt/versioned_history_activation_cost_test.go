//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/versionedhistory"
)

// What versioned-history activation costs an embedded store whose history is
// off.
//
// Every embedded transaction opens the engine: OpenSQL, BEGIN, the statements,
// ROLLBACK or COMMIT, close. Activation, which runs on every store a factory
// opens (`bd list` and `bd show` included), used to read the switch's row
// through one of those of its own, so each command paid one extra engine open
// and three statements to learn that history was off. The read now rides the
// store's first WRITE transaction; a store that only reads never makes it.

// TestEmbeddedActivationDoesNotOpenTheEngine holds the engine on a handle of
// its own while it activates a store. A second handle waits for the first
// (OpenSQL's backoff has no elapsed-time limit), so an activation that read the
// row would wait out the deadline below. One that hands the read to the first
// write returns without touching the engine.
func TestEmbeddedActivationDoesNotOpenTheEngine(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "vhe")
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := versionedhistory.ActivateStore(ctx, te.store, nil); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("ActivateStore waited for the embedded engine until %v: activation opened a handle to read %s",
			ctx.Err(), versionedhistory.ConfigKey)
	}
}

// TestEmbeddedDeferredActivationRecordsFromTheFirstWrite is the semantics half:
// a store whose own row says on, activated without a read, records the very
// first write it makes, and a store with no row records nothing.
func TestEmbeddedDeferredActivationRecordsFromTheFirstWrite(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, tc := range []struct {
		name string
		row  string
		want int
	}{
		{name: "row on", row: "true", want: 1},
		{name: "no row", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t, "vhd")
			ctx := t.Context()
			if tc.row != "" {
				if err := te.store.SetConfig(ctx, versionedhistory.ConfigKey, tc.row); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := versionedhistory.ActivateStore(ctx, te.store, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := te.store.SearchIssues(ctx, "", types.IssueFilter{}); err != nil {
				t.Fatal(err)
			}
			issue := &types.Issue{Title: "first write", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatal(err)
			}
			var n int
			te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{issue.ID}, &n)
			if n != tc.want {
				t.Errorf("the first write minted %d version row(s), want %d", n, tc.want)
			}
		})
	}
}

// TestEmbeddedDeferredSwitchReadErrorFailsTheWrite: a write whose switch read
// fails writes nothing, rather than committing a create with no participation
// stamp, and the next write asks again and records.
func TestEmbeddedDeferredSwitchReadErrorFailsTheWrite(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "vhf")
	ctx := t.Context()
	readErr := errors.New("switch read lost")
	var calls atomic.Int64
	te.store.DeferVersionedHistory(func(ctx context.Context, readConfig func(context.Context, string) (string, error)) (bool, error) {
		if calls.Add(1) == 1 {
			return false, readErr
		}
		return true, nil
	})
	first := &types.Issue{Title: "first write", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, first, "tester"); !errors.Is(err, readErr) {
		t.Fatalf("CreateIssue = %v, want the switch read's error", err)
	}
	var n int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues", nil, &n)
	if n != 0 {
		t.Fatalf("a write whose switch read failed left %d issue(s) behind", n)
	}
	second := &types.Issue{Title: "second write", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, second, "tester"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("the switch was asked %d time(s) over the two writes, want 2", calls.Load())
	}
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{second.ID}, &n)
	if n != 1 {
		t.Errorf("the write after a failed switch read minted %d version row(s), want 1", n)
	}
}

// TestEmbeddedOnlyAWriteAnswersTheSwitch pins where the deferred read happens:
// never in a read transaction, once in the first write, never again.
func TestEmbeddedOnlyAWriteAnswersTheSwitch(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "vhw")
	ctx := t.Context()
	var calls atomic.Int64
	var read string
	te.store.DeferVersionedHistory(func(ctx context.Context, readConfig func(context.Context, string) (string, error)) (bool, error) {
		calls.Add(1)
		v, err := readConfig(ctx, "issue_prefix")
		read = v
		return false, err
	})
	if _, err := te.store.SearchIssues(ctx, "", types.IssueFilter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := te.store.GetConfig(ctx, "issue_prefix"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("reads answered the switch %d time(s), want 0", calls.Load())
	}
	for i := 0; i < 2; i++ {
		if err := te.store.CreateIssue(ctx, &types.Issue{Title: "write", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("two writes answered the switch %d time(s), want 1", calls.Load())
	}
	if read != "vhw" {
		t.Errorf("the deferred read saw issue_prefix %q through the write's transaction, want %q", read, "vhw")
	}
}
