//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedUpdateIssueCheckedVersionCAS proves the ExpectedVersion CAS wires
// through the EmbeddedDoltStore's withConn wrapper: a matching version updates, a
// stale version is refused atomically with storage.ErrVersionMismatch (field
// unchanged, no updated event), a concurrent write invalidates a captured
// version, and a nil ExpectedVersion is unchanged behavior. The compare-and-swap
// core is the shared issueops.UpdateIssueInTx/CheckVersionInTx already covered
// against a real engine by the dolt package; this test proves the embedded
// wrapper threads it and rolls back.
func TestEmbeddedUpdateIssueCheckedVersionCAS(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ucv")
	ctx := t.Context()
	ptr := func(v int64) *int64 { return &v }

	create := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	get := func(id string) *types.Issue {
		iss, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		return iss
	}

	// Matching version updates.
	create("ucv-match")
	if err := te.store.UpdateIssueChecked(ctx, "ucv-match",
		map[string]interface{}{"title": "matched"}, "tester",
		storage.UpdateIssueOptions{ExpectedVersion: ptr(get("ucv-match").RowVersion)}); err != nil {
		t.Fatalf("matching-version update err = %v, want nil", err)
	}
	if got := get("ucv-match").Title; got != "matched" {
		t.Fatalf("ucv-match title = %q, want %q", got, "matched")
	}

	// Stale version refuses atomically: field unchanged, no updated event.
	create("ucv-stale")
	before := get("ucv-stale").Title
	stale := get("ucv-stale").RowVersion + 1
	err := te.store.UpdateIssueChecked(ctx, "ucv-stale",
		map[string]interface{}{"title": "should-not-apply"}, "tester",
		storage.UpdateIssueOptions{ExpectedVersion: ptr(stale)})
	if !errors.Is(err, storage.ErrVersionMismatch) {
		t.Fatalf("stale update err = %v, want errors.Is(_, ErrVersionMismatch)", err)
	}
	if got := get("ucv-stale").Title; got != before {
		t.Fatalf("ucv-stale title = %q after refusal, want unchanged %q (withConn did not roll back)", got, before)
	}
	events, err := te.store.GetEvents(ctx, "ucv-stale", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	for _, e := range events {
		if e.EventType == types.EventUpdated {
			t.Fatalf("updated event recorded despite version mismatch (tx must roll back)")
		}
	}

	// A concurrent plain UpdateIssue invalidates a captured version.
	create("ucv-concurrent")
	v1 := get("ucv-concurrent").RowVersion
	if err := te.store.UpdateIssue(ctx, "ucv-concurrent",
		map[string]interface{}{"priority": 1}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if err := te.store.UpdateIssueChecked(ctx, "ucv-concurrent",
		map[string]interface{}{"title": "should-not-apply"}, "tester",
		storage.UpdateIssueOptions{ExpectedVersion: ptr(v1)}); !errors.Is(err, storage.ErrVersionMismatch) {
		t.Fatalf("stale-after-concurrent update err = %v, want errors.Is(_, ErrVersionMismatch)", err)
	}

	// nil ExpectedVersion is unchanged behavior.
	if err := te.store.UpdateIssueChecked(ctx, "ucv-stale",
		map[string]interface{}{"title": "nil-check"}, "tester",
		storage.UpdateIssueOptions{}); err != nil {
		t.Fatalf("nil-version update err = %v, want nil (back-compat)", err)
	}
	if got := get("ucv-stale").Title; got != "nil-check" {
		t.Fatalf("ucv-stale title = %q after nil-version update, want %q", got, "nil-check")
	}
}
