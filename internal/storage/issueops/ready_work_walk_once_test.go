package issueops

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
)

// TestFilterReadyWispsInTxUsesTheHoistedDescendantSet pins that the wisp leg
// of `bd ready --parent` consumes the descendant set the issues leg already
// computed instead of walking the parent's descendants again. The walk is the
// dominant cost of a scoped ready call, and this leg used to run it a second
// time on every call (be-qfm). The mock scripts exactly the queries the leg
// is allowed to run for a scoped filter — the parented-ID probes and the
// blocked-wisp filter — so a `WITH RECURSIVE` descendant walk here is an
// unexpected query and fails the test.
func TestFilterReadyWispsInTxUsesTheHoistedDescendantSet(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	parent := "rw-parent"
	inside := &types.Issue{ID: "rw-child-wisp", Title: "in scope"}
	outside := &types.Issue{ID: "rw-stray-wisp", Title: "out of scope"}

	mock.ExpectQuery(`SELECT issue_id FROM dependencies\s+WHERE type = 'parent-child' AND issue_id IN`).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	mock.ExpectQuery(`SELECT issue_id FROM wisp_dependencies\s+WHERE type = 'parent-child' AND issue_id IN`).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	mock.ExpectQuery(`SELECT id FROM wisps WHERE id IN \(\?,\?\) AND is_blocked = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	filter := types.WorkFilter{ParentID: &parent, IncludeDeferred: true}
	got, err := filterReadyWispsInTx(context.Background(), tx, filter, []*types.Issue{inside, outside}, nil, []string{inside.ID})
	if err != nil {
		t.Fatalf("filterReadyWispsInTx: %v", err)
	}
	if len(got) != 1 || got[0].ID != inside.ID {
		t.Fatalf("kept %v, want only %s (the hoisted descendant set decides scope)", ids(got), inside.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected query sequence: %v", err)
	}
}

func ids(issues []*types.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, issue.ID)
	}
	return out
}
