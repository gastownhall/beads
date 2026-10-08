//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Adaptive I/O for bd_events_journal (BEADS-JOURNAL-PLAN.md §4.2a/§4.3, PR
// A1). These tests reproduce gas-city-inc's exact shape — a table built
// before `comment_json` and `actor` ever existed — directly on an
// otherwise-ordinary embedded store, and prove the writer and reader adapt to
// it instead of failing every journaled write (T2.1/T2.2), that adaptation
// probes the shape once per activation rather than once per write (T2.3),
// and that a table missing a REQUIRED column refuses at open rather than
// silently disabling the journal or failing per write (T2.4).

// newGciShapeStore returns a store whose bd_events_journal has had
// comment_json and actor dropped, reproducing gas-city-inc's shape
// (BEADS-JOURNAL-PLAN.md §1.1: "gci.bd_events_journal has the columns seq,
// ts, op, issue_id, issue_json, dep_json" — no actor, no comment_json). The
// journal is NOT activated; callers that want to observe activation itself
// (the probe, its warning, its cached result) call SetEventsJournalEnabled
// themselves.
func newGciShapeStore(t *testing.T, prefix string) *testEnv {
	t.Helper()
	te := newTestEnv(t, prefix)
	ctx := context.Background()
	te.exec(t, ctx, "ALTER TABLE bd_events_journal DROP COLUMN comment_json")
	te.exec(t, ctx, "ALTER TABLE bd_events_journal DROP COLUMN actor")
	return te
}

// TestAdaptiveInsert_GciShape is T2.1: on the gci shape, create, update and
// comment all succeed, the rows land, and the comment payload is dropped with
// exactly one warning. A fixed INSERT column list (the pre-A1 behavior) fails
// every one of these three writes, because each one names either actor or
// comment_json.
func TestAdaptiveInsert_GciShape(t *testing.T) {
	te := newGciShapeStore(t, "ai")
	ctx := context.Background()

	stderr := captureStderr(t, func() {
		te.store.SetEventsJournalEnabled(true)
	})
	if err := te.store.EventsJournalActivationError(); err != nil {
		t.Fatalf("activation on the gci shape (missing only OPTIONAL columns) must succeed, got: %v", err)
	}
	if n := strings.Count(stderr, "events journal:"); n != 1 {
		t.Fatalf("activation warning count = %d, want exactly 1 (one warning per store, not per write):\n%s", n, stderr)
	}
	for _, want := range []string{"actor", "comment_json"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("activation warning does not name missing column %q:\n%s", want, stderr)
		}
	}

	issue := &types.Issue{Title: "gci create", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue on gci shape: %v", err)
	}
	if err := te.store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "gci updated"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue on gci shape: %v", err)
	}
	if _, err := te.store.AddIssueComment(ctx, issue.ID, "tester", "gci comment"); err != nil {
		t.Fatalf("AddIssueComment on gci shape: %v", err)
	}

	rows, err := te.store.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("ReadEventsJournal on gci shape: %v", err)
	}
	var ops []string
	for _, r := range rows {
		if r.IssueID != issue.ID {
			continue
		}
		ops = append(ops, r.Op)
		// Dropped, not merely blank: the column does not exist on this shape,
		// so the adaptive reader must never have tried to select it.
		if r.Actor != "" {
			t.Errorf("row op=%s: Actor = %q, want \"\" (column does not exist on this shape)", r.Op, r.Actor)
		}
		if r.CommentJSON != "" {
			t.Errorf("row op=%s: CommentJSON = %q, want \"\" (column does not exist on this shape)", r.Op, r.CommentJSON)
		}
	}
	wantOps := []string{"create", "update", "comment"}
	if len(ops) != len(wantOps) {
		t.Fatalf("journaled ops for %s = %v, want %v (a fixed INSERT column list fails every one of these on the gci shape)", issue.ID, ops, wantOps)
	}
	for i, want := range wantOps {
		if ops[i] != want {
			t.Errorf("journaled op[%d] = %q, want %q", i, ops[i], want)
		}
	}
}

// TestAdaptiveRead_GciShape is T2.2: a page read on the gci shape succeeds,
// and actor/comment come back empty rather than failing the SELECT. A fixed
// SELECT list (the pre-A1 behavior) fails the read outright, because it names
// both actor and comment_json.
func TestAdaptiveRead_GciShape(t *testing.T) {
	te := newGciShapeStore(t, "ar")
	ctx := context.Background()
	te.store.SetEventsJournalEnabled(true)
	if err := te.store.EventsJournalActivationError(); err != nil {
		t.Fatalf("activation on the gci shape: %v", err)
	}

	issue := &types.Issue{ID: "ar-1", Title: "gci read", Status: types.StatusOpen, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue on gci shape: %v", err)
	}

	page, err := te.store.ReadEventsJournalPage(ctx, 0, 0)
	if err != nil {
		t.Fatalf("ReadEventsJournalPage on gci shape: %v", err)
	}
	if len(page.Rows) == 0 {
		t.Fatal("ReadEventsJournalPage on gci shape returned no rows")
	}
	for _, r := range page.Rows {
		if r.Actor != "" {
			t.Errorf("row op=%s: Actor = %q, want \"\" on a shape with no actor column", r.Op, r.Actor)
		}
		if r.CommentJSON != "" {
			t.Errorf("row op=%s: CommentJSON = %q, want \"\" on a shape with no comment_json column", r.Op, r.CommentJSON)
		}
	}
}

// TestJournalShapeProbeOncePerOpen is T2.3: 100 writes against one already-
// activated store issue no INFORMATION_SCHEMA probes of their own — only the
// ONE SetEventsJournalEnabled ran. A per-write probe would show up here as a
// nonzero delta across the 100 writes.
func TestJournalShapeProbeOncePerOpen(t *testing.T) {
	te := newTestEnv(t, "pr")
	ctx := context.Background()

	before := issueops.JournalShapeProbeCountForTest()
	te.store.SetEventsJournalEnabled(true)
	if err := te.store.EventsJournalActivationError(); err != nil {
		t.Fatalf("activation on canonical shape: %v", err)
	}
	afterActivate := issueops.JournalShapeProbeCountForTest()
	if delta := afterActivate - before; delta != 1 {
		t.Fatalf("probes issued by SetEventsJournalEnabled = %d, want exactly 1", delta)
	}

	for i := 0; i < 100; i++ {
		issue := &types.Issue{
			Title: fmt.Sprintf("probe-once %d", i), Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %d: %v", i, err)
		}
	}

	after := issueops.JournalShapeProbeCountForTest()
	if delta := after - afterActivate; delta != 0 {
		t.Fatalf("probes issued by 100 writes against an already-activated store = %d, want 0 (a per-write probe, not a per-open probe)", delta)
	}
}

// TestMissingRequiredColumnRefusesAtOpen is T2.4: a table missing a REQUIRED
// column (op) makes activation fail with a typed error when the journal is
// enabled, but leaves it succeeding when disabled — a disabled workspace
// accepts any shape. It also pins that a caller who enables the journal
// anyway (ignoring the returned activation error) still never attempts the
// INSERT: the mutation itself must not fail, which is the whole point of
// moving this failure to open time instead of per write.
func TestMissingRequiredColumnRefusesAtOpen(t *testing.T) {
	te := newTestEnv(t, "mr")
	ctx := context.Background()
	te.exec(t, ctx, "ALTER TABLE bd_events_journal DROP COLUMN op")

	te.store.SetEventsJournalEnabled(true)
	err := te.store.EventsJournalActivationError()
	if err == nil {
		t.Fatal("activation with the journal enabled against a table missing a required column (op) succeeded; want a typed error")
	}
	if !errors.Is(err, issueops.ErrJournalShapeUnsupported) {
		t.Errorf("activation error = %v, does not match issueops.ErrJournalShapeUnsupported", err)
	}
	var shapeErr *issueops.JournalShapeError
	if !errors.As(err, &shapeErr) {
		t.Fatalf("activation error is not a *issueops.JournalShapeError: %v (%T)", err, err)
	}
	if len(shapeErr.Missing) != 1 || shapeErr.Missing[0] != "op" {
		t.Errorf("JournalShapeError.Missing = %v, want [op]", shapeErr.Missing)
	}

	// Disabling must still succeed: a disabled workspace accepts any shape, and
	// never runs the probe that would otherwise fail it.
	te.store.SetEventsJournalEnabled(false)
	if err := te.store.EventsJournalActivationError(); err != nil {
		t.Errorf("activation with the journal disabled against the same broken shape failed: %v", err)
	}

	// Re-enable (probe fails again, same cached nil shape) and prove the
	// mutation itself still lands: this is the gci failure mode A1 exists to
	// close, where a per-write INSERT against a missing column rolled back the
	// user's own write.
	te.store.SetEventsJournalEnabled(true)
	issue := &types.Issue{Title: "no op column", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue must still succeed even though the journal cannot activate on this shape: %v", err)
	}
}
