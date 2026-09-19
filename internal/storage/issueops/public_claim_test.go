package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

const claimActor = "alice"

// claimIssueRow builds one mocked issues row carrying the coordination fields
// the claim CAS reads back.
func claimIssueRow(id, assignee string, status types.Status) *sqlmock.Rows {
	values := make([]driver.Value, 0, len(issueColumns()))
	for _, col := range issueColumns() {
		switch col {
		case "id":
			values = append(values, id)
		case "title":
			values = append(values, "claim me")
		case "description", "design", "acceptance_criteria", "notes":
			values = append(values, "")
		case "status":
			values = append(values, string(status))
		case "assignee":
			values = append(values, assignee)
		case "priority":
			values = append(values, 1)
		case "issue_type":
			values = append(values, string(types.TypeTask))
		case "compaction_level":
			values = append(values, 0)
		default:
			values = append(values, nil)
		}
	}
	return issueRows().AddRow(values...)
}

// expectClaimableRead mocks everything ClaimIssueInTx reads before its
// conditional UPDATE: the wisp routing probe, the pre-image, and the two
// config lookups that widen the CAS predicate.
func expectClaimableRead(mock sqlmock.Sqlmock, id, assignee string, status types.Status) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(claimIssueRow(id, assignee, status))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"label"}))
	// One row is enough to make custom-status resolution authoritative, so it
	// never falls through to the legacy config string.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("archived", string(types.CategoryDone)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("claim.pools").
		WillReturnError(sql.ErrNoRows)
}

// expectClaimStateRead mocks the same-transaction read of the coordination
// columns — the one ClaimIssueInTx makes when the CAS matches no row.
func expectClaimStateRead(mock sqlmock.Sqlmock, id, assignee string, status types.Status) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT assignee, status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"assignee", "status"}).AddRow(assignee, string(status)))
}

// TestExecuteClaimRefusesWispIDsBeforeAnyWork pins the deliberate inversion of
// ClaimIssueInTx, which routes a wisp id to the wisp tables and claims it. The
// role does not: the wisp plane is not claimable through it, and the refusal
// lands on the routing probe, before a pre-image is even read.
func TestExecuteClaimRefusesWispIDsBeforeAnyWork(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-wisp"
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	result, tables, err := ExecuteClaim(context.Background(), tx, publicops.ClaimRequest{Actor: claimActor, IssueID: id})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for a wisp id", err)
	}
	if result.Issue != nil || result.Changed || len(tables) != 0 {
		t.Errorf("a refused wisp claim reported result %+v and tables %v", result, tables)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a wisp id reached further than the routing probe: %v", err)
	}
}

// TestExecuteClaimReportsTheStateThatLostTheCAS: the refusal keeps its sentinel
// and its exact prose, and gains the assignee and status read inside the same
// transaction — so a caller classifies the conflict from typed fields instead
// of matching substrings in the message.
func TestExecuteClaimReportsTheStateThatLostTheCAS(t *testing.T) {
	for _, tc := range []struct {
		name     string
		assignee string
		status   types.Status
		sentinel error
		// wantAssignee is what the conflict must publish. A status refusal
		// still carries the holder it read; only the wire decides whether to
		// publish it.
		wantAssignee string
		// attemptsUpdate: ClaimIssueInTx only issues the UPDATE when the
		// pre-image says the actor could possibly win the CAS (ga-v2k49 —
		// same early-skip shape as unclaim.go's ownership precheck). "held by
		// another actor" never could, so no UPDATE is attempted at all; "not
		// in a claimable state" has an empty (claimable-by-anyone) assignee,
		// so the UPDATE is attempted and loses on the status predicate.
		attemptsUpdate bool
	}{
		{
			name: "held by another actor", assignee: "bob", status: types.StatusOpen,
			sentinel: storage.ErrAlreadyClaimed, wantAssignee: "bob", attemptsUpdate: false,
		},
		{
			name: "not in a claimable state", assignee: "", status: types.StatusClosed,
			sentinel: storage.ErrNotClaimable, attemptsUpdate: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)

			const id = "bd-1"
			expectClaimableRead(mock, id, tc.assignee, tc.status)
			if tc.attemptsUpdate {
				mock.ExpectExec(`(?s)UPDATE issues\s+SET assignee`).WillReturnResult(sqlmock.NewResult(0, 0))
			}
			expectClaimStateRead(mock, id, tc.assignee, tc.status)
			// The role's own re-read, which is what carries the state out.
			expectClaimStateRead(mock, id, tc.assignee, tc.status)

			_, tables, err := ExecuteClaim(context.Background(), tx, publicops.ClaimRequest{Actor: claimActor, IssueID: id})
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("err = %v, want it to match %v", err, tc.sentinel)
			}
			var conflict *publicops.ClaimConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("err = %v, want a *ClaimConflictError carrying the losing state", err)
			}
			if conflict.IssueID != id || conflict.Assignee != tc.wantAssignee || conflict.Status != tc.status {
				t.Errorf("conflict = %#v, want assignee %q status %q", conflict, tc.wantAssignee, tc.status)
			}
			if got := conflict.Error(); got != conflict.Err.Error() {
				t.Errorf("Error() = %q, want the refusal unedited (%q)", got, conflict.Err.Error())
			}
			if len(tables) != 0 {
				t.Errorf("a refused claim staged %v", tables)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestExecuteClaimIdempotentReclaimStagesNothing: the holder re-claiming its
// own in-progress issue is a success that wrote nothing, so it must stage no
// table — an empty set is what tells the store to skip the version-control
// commit, and a polling client must not be able to mint empty ones.
func TestExecuteClaimIdempotentReclaimStagesNothing(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectClaimableRead(mock, id, claimActor, types.StatusInProgress)
	mock.ExpectExec(`(?s)UPDATE issues\s+SET assignee`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectClaimStateRead(mock, id, claimActor, types.StatusInProgress)
	// The post-state snapshot the result carries.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(claimIssueRow(id, claimActor, types.StatusInProgress))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"label"}))

	result, tables, err := ExecuteClaim(context.Background(), tx, publicops.ClaimRequest{Actor: claimActor, IssueID: id})
	if err != nil {
		t.Fatalf("ExecuteClaim: %v", err)
	}
	if result.Changed {
		t.Error("a re-claim by the holder reported a persisted mutation")
	}
	if len(tables) != 0 {
		t.Errorf("a re-claim by the holder staged %v; nothing changed, so nothing may be committed", tables)
	}
	if result.Issue == nil || result.Issue.Assignee != claimActor || result.Issue.Status != types.StatusInProgress {
		t.Errorf("result issue = %+v, want the post-state row", result.Issue)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestExecuteClaimIdempotentReclaimAcrossSpellingStagesNothing pins ga-v2k49
// (the ga-wzl83/ga-5ksp5 follow-up steveyegge's #5438 review enumerated): the
// idempotent re-claim above must also hold when the caller's actor and the
// stored assignee are the same identity spelled under two different layers'
// separator conventions (dot vs sanitized double-underscore — the ga-wzl83
// repro shape), not just byte-identical.
func TestExecuteClaimIdempotentReclaimAcrossSpellingStagesNothing(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	const storedAssignee = "gastown__mayor" // sanitized form, as persisted
	const actor = "gastown.mayor"           // dotted form, as this caller spells itself
	expectClaimableRead(mock, id, storedAssignee, types.StatusInProgress)
	mock.ExpectExec(`(?s)UPDATE issues\s+SET assignee`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectClaimStateRead(mock, id, storedAssignee, types.StatusInProgress)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(claimIssueRow(id, storedAssignee, types.StatusInProgress))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"label"}))

	result, tables, err := ExecuteClaim(context.Background(), tx, publicops.ClaimRequest{Actor: actor, IssueID: id})
	if err != nil {
		t.Fatalf("ExecuteClaim: %v, want success (same identity, different separator spelling)", err)
	}
	if result.Changed {
		t.Error("a cross-spelling re-claim by the holder reported a persisted mutation")
	}
	if len(tables) != 0 {
		t.Errorf("a cross-spelling re-claim by the holder staged %v; nothing changed, so nothing may be committed", tables)
	}
	if result.Issue == nil || result.Issue.Assignee != storedAssignee || result.Issue.Status != types.StatusInProgress {
		t.Errorf("result issue = %+v, want the post-state row (assignee stays in its stored spelling %q)", result.Issue, storedAssignee)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestExecuteClaimRefusesGenuinelyDifferentIdentityDespiteSimilarSpelling is
// the not-a-no-op control for the cross-spelling fix above (steveyegge's
// #5438 review flagged exactly this shape as the right guard): the pair named
// in canonicalActor's own doc comment as one that must stay distinct despite
// sharing a separator style. Without this, a canonicalization broad enough to
// match everyone would pass the idempotent test above for the wrong reason.
func TestExecuteClaimRefusesGenuinelyDifferentIdentityDespiteSimilarSpelling(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	const storedAssignee = "gastown.mayor"
	const actor = "gastown.dog-3"
	expectClaimableRead(mock, id, storedAssignee, types.StatusInProgress)
	// No ExpectExec: a genuinely different identity never had a chance to win
	// the CAS, so ClaimIssueInTx skips the UPDATE attempt entirely (ga-v2k49
	// — see attemptsUpdate above).
	expectClaimStateRead(mock, id, storedAssignee, types.StatusInProgress)

	_, tables, err := ExecuteClaim(context.Background(), tx, publicops.ClaimRequest{Actor: actor, IssueID: id})
	if !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("err = %v, want ErrAlreadyClaimed for a genuinely different identity", err)
	}
	var conflict *publicops.ClaimConflictError
	if !errors.As(err, &conflict) || conflict.Assignee != storedAssignee {
		t.Fatalf("conflict = %+v, want it to name the real holder %q", conflict, storedAssignee)
	}
	if len(tables) != 0 {
		t.Errorf("a refused claim staged %v", tables)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestExecuteClaimStagesTheRowAndEventItWrote: a claim that wins the CAS is a
// history-worthy mutation, so it stages the issues and events tables — the
// lease grant rides the ephemeral table and is deliberately not staged.
func TestExecuteClaimStagesTheRowAndEventItWrote(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectClaimableRead(mock, id, "", types.StatusOpen)
	mock.ExpectExec(`(?s)UPDATE issues\s+SET assignee`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO leases`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id FROM events`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO events`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(claimIssueRow(id, claimActor, types.StatusInProgress))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"label"}))

	result, tables, err := ExecuteClaim(context.Background(), tx, publicops.ClaimRequest{Actor: claimActor, IssueID: id})
	if err != nil {
		t.Fatalf("ExecuteClaim: %v", err)
	}
	if !result.Changed {
		t.Error("a won CAS reported no persisted mutation")
	}
	if !tables["issues"] || !tables["events"] {
		t.Errorf("staged %v, want the issues row and the claim event", tables)
	}
	if tables["leases"] {
		t.Error("the ephemeral lease grant was staged for the version-control commit")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestExecuteClaimRefusesIncompleteRequestsBeforeAnySQL: a request missing an
// actor or an id is a deterministic validation failure, and it must not cost a
// round trip to discover.
func TestExecuteClaimRefusesIncompleteRequestsBeforeAnySQL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request publicops.ClaimRequest
	}{
		{"no actor", publicops.ClaimRequest{IssueID: "bd-1"}},
		{"no issue", publicops.ClaimRequest{Actor: claimActor}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)

			_, tables, err := ExecuteClaim(context.Background(), tx, tc.request)
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}
			if len(tables) != 0 {
				t.Errorf("a refused request staged %v", tables)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("an invalid request reached the database: %v", err)
			}
		})
	}
}
