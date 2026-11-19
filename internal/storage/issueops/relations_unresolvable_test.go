package issueops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	publicops "github.com/steveyegge/beads/issueops"
)

// bd-uschhs (#5850) AC-6: Relations answers with ISSUES and can be nothing
// else (see RelatedIssue's contract in issueops/relations.go), so an anchor
// carrying an edge whose target has no row in this database must see only
// its real neighbors — no placeholder row for the missing far end, no error
// naming it, and no follow-up query reaching for it. The metadata reader's
// drop is the seam that guarantees this; this test drives the full
// ExecuteRelated body so the anchor-existence probe, the edge read and the
// hydration miss all run, pinning that the drop survives the whole role
// rather than just the reader it delegates to.
func TestExecuteRelatedOutSeesOnlyRealRows(t *testing.T) {
	t.Parallel()

	const anchor = "ud-1"
	const local = "ud-2"
	const external = "external:liveop:ghost"

	_, mock, tx := beginMockTx(t)
	// The anchor-existence probe: found on the issues plane, so the wisps
	// probe is skipped (RequireIssueOrWispInTx returns on the first hit).
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)")).
		WithArgs(anchor).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	expectOutgoingAnchorEdges(mock, anchor,
		[]anchorEdgeRow{{local, "blocks"}, {external, "tracks"}}, nil)
	// The hydration batch carries BOTH far ids; only the local one hydrates.
	// The external id's empty half is the drop under test.
	expectEdgeHydration(mock, []string{local, external}, map[string]string{local: "Local"})

	items, err := ExecuteRelated(context.Background(), tx,
		publicops.RelatedRequest{ID: anchor, Direction: publicops.RelationOut})
	if err != nil {
		t.Fatalf("ExecuteRelated: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d neighbors, want 1: %+v (a missing far end leaked or a real one was lost)", len(items), items)
	}
	if items[0].ID != local || items[0].DependencyType != "blocks" {
		t.Fatalf("neighbor = %+v, want the one real far end %q", items[0], local)
	}
}

// The inbound direction is deliberately untested here: a stored edge's
// issue_id is foreign-keyed to a real row, so a dependent's near end cannot
// be missing the way an outgoing target can — the outbound one above is the
// only direction an unresolvable far end reaches.
