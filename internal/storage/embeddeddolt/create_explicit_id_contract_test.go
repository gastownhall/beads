//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	publicops "github.com/steveyegge/beads/issueops"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// The explicit-ID half of the bd-t4h0n7 contract (PR #6754): a caller that
// NAMES its id keeps every existing option behavior. The auto-mint fix only
// tightens creates whose id the create itself generated; these tests pin —
// unchanged — what an explicit id meets: the default UPSERT updates an
// existing row, ConflictSkip leaves it untouched, a retired id is refused
// (or skipped under ConflictSkip), a sibling-plane id is refused (or skipped
// under ConflictSkip), and the RejectStaleUpserts guard keeps a strictly
// newer stored row. All of these run through the SAME shared create code the
// auto-minted path uses, so they are the non-regression guard for AC-1..AC-5.

func explicitIssue(id, title string) *types.Issue {
	return &types.Issue{ID: id, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

// TestExplicitIDUpsertUpdatesAnExistingRow: default options keep UPSERT
// semantics — the row is updated, not duplicated, and no second created
// event appears for a row that already existed.
func TestExplicitIDUpsertUpdatesAnExistingRow(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()

	if err := te.store.CreateIssue(ctx, explicitIssue("cl-live", "original title"), "tester"); err != nil {
		t.Fatalf("CreateIssue(original): %v", err)
	}
	if err := te.store.CreateIssue(ctx, explicitIssue("cl-live", "updated title"), "tester"); err != nil {
		t.Fatalf("CreateIssue(upsert): %v", err)
	}
	te.assertIssueTitle(t, ctx, "issues", "cl-live", "updated title")
	te.assertEventCount(t, ctx, "events", "cl-live", "created", 1)
}

// TestExplicitIDConflictSkipLeavesAnExistingRow: ConflictSkip is
// insert-if-new — an existing row is left untouched, in silence (nil error).
func TestExplicitIDConflictSkipLeavesAnExistingRow(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()

	if err := te.store.CreateIssue(ctx, explicitIssue("cl-skip", "original title"), "tester"); err != nil {
		t.Fatalf("CreateIssue(original): %v", err)
	}
	err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{explicitIssue("cl-skip", "skipped update")},
		"tester", storage.BatchCreateOptions{ConflictSkip: true})
	if err != nil {
		t.Fatalf("CreateIssuesWithFullOptions(ConflictSkip over existing): %v, want nil (skip)", err)
	}
	te.assertIssueTitle(t, ctx, "issues", "cl-skip", "original title")
	te.assertEventCount(t, ctx, "events", "cl-skip", "created", 1)
}

// TestExplicitIDRefusesARetiredID: a retaining sweep's tombstone makes the
// id expired — the default UPSERT is refused with *IdempotencyExpiredError
// before anything is written, and ConflictSkip skips it silently instead.
func TestExplicitIDRefusesARetiredID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()

	te.exec(t, ctx, "INSERT INTO id_tombstones (id, closed_at) VALUES ('cl-gone', '2026-01-01 00:00:00')")

	err := te.store.CreateIssue(ctx, explicitIssue("cl-gone", "resurrection attempt"), "tester")
	if err == nil {
		t.Fatal("CreateIssue over a retired id = nil error, want *IdempotencyExpiredError")
	}
	var expired *publicops.IdempotencyExpiredError
	if !errors.As(err, &expired) || expired.ID != "cl-gone" {
		t.Fatalf("CreateIssue over a retired id = %v, want *IdempotencyExpiredError for cl-gone", err)
	}
	te.assertRowNotExists(t, ctx, "issues", "cl-gone")

	err = te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{explicitIssue("cl-gone", "resurrection attempt")},
		"tester", storage.BatchCreateOptions{ConflictSkip: true})
	if err != nil {
		t.Fatalf("CreateIssuesWithFullOptions(ConflictSkip over retired): %v, want nil (skip)", err)
	}
	te.assertRowNotExists(t, ctx, "issues", "cl-gone")
}

// TestExplicitIDRefusesASiblingPlaneID: issues and wisps share one ID space;
// an id held by a live wisp row is refused for a durable create (nothing is
// written beside the wisp), and ConflictSkip skips it silently instead.
func TestExplicitIDRefusesASiblingPlaneID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()

	twin := explicitIssue("cl-twin", "wisp occupant")
	twin.Ephemeral = true
	if err := te.store.CreateIssue(ctx, twin, "tester"); err != nil {
		t.Fatalf("CreateIssue(wisp occupant): %v", err)
	}

	err := te.store.CreateIssue(ctx, explicitIssue("cl-twin", "durable intruder"), "tester")
	if err == nil || !strings.Contains(err.Error(), "wisps table") {
		t.Fatalf("CreateIssue(durable over a wisp-held id) = %v, want the sibling-plane refusal", err)
	}
	te.assertRowNotExists(t, ctx, "issues", "cl-twin")
	te.assertIssueTitle(t, ctx, "wisps", "cl-twin", "wisp occupant")

	err = te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{explicitIssue("cl-twin", "durable intruder")},
		"tester", storage.BatchCreateOptions{ConflictSkip: true})
	if err != nil {
		t.Fatalf("CreateIssuesWithFullOptions(ConflictSkip over sibling): %v, want nil (skip)", err)
	}
	te.assertRowNotExists(t, ctx, "issues", "cl-twin")
	te.assertIssueTitle(t, ctx, "wisps", "cl-twin", "wisp occupant")
}

// TestExplicitIDRejectsAStaleUpsert: under RejectStaleUpserts, a stored row
// strictly newer than the incoming snapshot keeps everything — nothing is
// written, no error is returned, and OnStaleRejected reports the id.
func TestExplicitIDRejectsAStaleUpsert(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()

	if err := te.store.CreateIssue(ctx, explicitIssue("cl-stale", "stored title"), "tester"); err != nil {
		t.Fatalf("CreateIssue(original): %v", err)
	}
	// Push the stored row's updated_at ahead of any snapshot this test can
	// carry: strictly newer is what the guard rejects on.
	te.exec(t, ctx, "UPDATE issues SET updated_at = '2030-01-01 00:00:00' WHERE id = 'cl-stale'")

	var rejected []string
	stale := explicitIssue("cl-stale", "stale snapshot")
	stale.UpdatedAt = time.Now()
	err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{stale}, "tester",
		storage.BatchCreateOptions{
			RejectStaleUpserts: true,
			OnStaleRejected:    func(issueID string) { rejected = append(rejected, issueID) },
		})
	if err != nil {
		t.Fatalf("CreateIssuesWithFullOptions(stale snapshot): %v, want nil (the guard rejects, not errors)", err)
	}
	if len(rejected) != 1 || rejected[0] != "cl-stale" {
		t.Errorf("OnStaleRejected = %v, want exactly [cl-stale]", rejected)
	}
	te.assertIssueTitle(t, ctx, "issues", "cl-stale", "stored title")
}
