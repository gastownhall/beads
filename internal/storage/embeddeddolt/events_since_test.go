//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEventsSinceEmbedded mirrors the dolt-suite EventsSince coverage on the
// embedded backend: the durable keyset feed returns durable events in
// (created_at, id) order, excludes wisp events, and honors the per-issue filter.
// Events are driven through store operations (CreateIssue/ClaimIssue) rather than
// raw INSERTs, since the external test package shares no connection with the
// store's working set.
func TestEventsSinceEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "es")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "es-a", Title: "durable a", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "es-b", Title: "durable b", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "es-w", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	// A durable claim adds a second durable event on es-a.
	if err := te.store.ClaimIssue(ctx, "es-a", "worker"); err != nil {
		t.Fatalf("ClaimIssue es-a: %v", err)
	}

	// Unfiltered durable feed: es-a and es-b events present, wisp excluded,
	// created_at non-decreasing.
	all, err := te.store.EventsSince(ctx, storage.EventCursor{}, "", 500)
	if err != nil {
		t.Fatalf("EventsSince(all): %v", err)
	}
	var sawA, sawB bool
	for i, e := range all {
		switch e.IssueID {
		case "es-a":
			sawA = true
		case "es-b":
			sawB = true
		case "es-w":
			t.Fatalf("durable-only feed returned a wisp event for es-w")
		}
		if i > 0 && e.CreatedAt.Before(all[i-1].CreatedAt) {
			t.Fatalf("feed not ordered by created_at ASC at index %d", i)
		}
	}
	if !sawA || !sawB {
		t.Fatalf("durable feed missing events: sawA=%v sawB=%v", sawA, sawB)
	}

	// Per-issue filter: only es-a's events.
	onlyA, err := te.store.EventsSince(ctx, storage.EventCursor{}, "es-a", 500)
	if err != nil {
		t.Fatalf("EventsSince(issue=es-a): %v", err)
	}
	if len(onlyA) == 0 {
		t.Fatalf("filtered es-a feed is empty")
	}
	for _, e := range onlyA {
		if e.IssueID != "es-a" {
			t.Fatalf("filtered es-a feed returned event for %s", e.IssueID)
		}
	}
}
