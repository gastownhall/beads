package issueops

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// A single `bd update --claim --assignee=bob` run by alice: ClaimIssueInTx
// arms the lease for alice, then the same call's generic patch overrides
// assignee to bob — inside the SAME transaction, so the patch's own oldIssue
// read sees the claim's uncommitted write (status=in_progress,
// assignee=alice). The lease invariant (lease.go:163-167) says a leases row
// must exist iff the issue is a live claim; bob ending up holding the issue
// with no lease row is the bug (be-z4ows).
const (
	transferIssueID     = "bd-1"
	transferClaimant    = "alice"
	transferFinalHolder = "bob"
)

// expectIssuePreImage mocks the wisp probe, issue-row, and labels read that
// updateIssueInTx's own readIssueAndResolveMergeOps issues for its oldIssue —
// the lighter subset of expectClaimableRead's sequence, without the two
// config lookups that only widen claim's own CAS predicate.
func expectIssuePreImage(mock sqlmock.Sqlmock, id, assignee string, status types.Status) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(claimIssueRow(id, assignee, status))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"label"}))
}

// armViaClaimThenAssigneeOverride drives ClaimIssueInTx(alice) followed by
// UpdateClaimedIssueInTx({"assignee": "bob"}, alice) in one transaction, and
// asserts the resulting lease arm is an INSERT for bob rather than the
// pre-fix DELETE. This is itself the core regression test (spec point 1):
// pinning holder="bob" on the arm INSERT means it cannot pass against the
// unfixed code, which issues DELETE FROM leases at this point instead.
func armViaClaimThenAssigneeOverride(t *testing.T, mock sqlmock.Sqlmock, tx *sql.Tx) *UpdateResult {
	t.Helper()
	ctx := context.Background()

	expectClaimableRead(mock, transferIssueID, "", types.StatusOpen)
	mock.ExpectExec(`(?s)UPDATE issues\s+SET assignee`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO leases`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id FROM events`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO events`).WillReturnResult(sqlmock.NewResult(0, 1))

	if _, err := ClaimIssueInTx(ctx, tx, transferIssueID, transferClaimant); err != nil {
		t.Fatalf("ClaimIssueInTx: %v", err)
	}

	expectIssuePreImage(mock, transferIssueID, transferClaimant, types.StatusInProgress)
	mock.ExpectExec(`(?s)UPDATE issues SET updated_at`).WillReturnResult(sqlmock.NewResult(0, 1))
	// The fix under test: a claim's own assignee override must re-arm the
	// lease for the final holder, not delete it (be-z4ows). Pinning holder
	// here is what makes this fail pre-fix — the unfixed clearLease path
	// issues `DELETE FROM leases` instead, which sqlmock reports as an
	// unexpected call against this ordered expectation.
	mock.ExpectExec(`(?s)INSERT INTO leases`).
		WithArgs(transferIssueID, transferFinalHolder, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id FROM events`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO events`).WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := UpdateClaimedIssueInTx(ctx, tx, transferIssueID, map[string]interface{}{"assignee": transferFinalHolder}, transferClaimant)
	if err != nil {
		t.Fatalf("UpdateClaimedIssueInTx: %v", err)
	}
	return result
}

// TestClaimWithAssigneeOverrideArmsLeaseForFinalHolder is verifying-test
// point 1: a claim with an assignee override in one call must leave a
// leases row held by the final assignee, not the claimant.
func TestClaimWithAssigneeOverrideArmsLeaseForFinalHolder(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	result := armViaClaimThenAssigneeOverride(t, mock, tx)
	if !result.Changed {
		t.Error("assignee override reported no persisted mutation")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHeartbeatAfterClaimAssigneeOverrideUsesNormalPath is verifying-test
// point 2: HeartbeatIssueInTx for the final holder must succeed via the
// normal UPDATE path (rows affected == 1). Only that UPDATE is mocked here;
// if the lease row were missing (the pre-fix bug), HeartbeatIssueInTx would
// fall into its rows==0 self-heal branch and issue a wisp probe plus an
// issue-table read that this test never registers, failing on the
// unexpected call.
func TestHeartbeatAfterClaimAssigneeOverrideUsesNormalPath(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	armViaClaimThenAssigneeOverride(t, mock, tx)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE leases SET lease_expires_at = ?, heartbeat_at = ?,")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), transferIssueID, transferFinalHolder).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := HeartbeatIssueInTx(context.Background(), tx, transferIssueID, transferFinalHolder); err != nil {
		t.Fatalf("HeartbeatIssueInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestReclaimSeesIssueAfterClaimAssigneeOverride is verifying-test point 3:
// once the re-armed lease goes stale, the issue must be visible to
// ReclaimExpiredLeasesInTx (its snapshot query INNER JOINs leases, so a
// leaseless in_progress issue — the pre-fix bug — can never be reclaimed).
func TestReclaimSeesIssueAfterClaimAssigneeOverride(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	armViaClaimThenAssigneeOverride(t, mock, tx)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT l.issue_id, COALESCE(i.assignee, ''), COALESCE(l.granted_node, '') FROM leases l")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "assignee", "granted_node"}).
			AddRow(transferIssueID, transferFinalHolder, ""))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM leases WHERE issue_id = ? AND lease_expires_at < ?")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE issues\s+SET status = 'open'`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id FROM events`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO events`).WillReturnResult(sqlmock.NewResult(0, 1))

	reclaimed, err := ReclaimExpiredLeasesInTx(context.Background(), tx, time.Now().UTC().Add(time.Hour), types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeasesInTx: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != transferIssueID || reclaimed[0].PreviousOwner != transferFinalHolder {
		t.Errorf("reclaimed = %+v, want one entry for %s held by %s", reclaimed, transferIssueID, transferFinalHolder)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestPlainAssigneeUpdateWithoutClaimStillClearsLease is verifying-test
// point 4, the regression guard: a hand-doled claim (`bd update --status
// in_progress --assignee=X` with no --claim) must still leave NO lease row.
// This drives plain UpdateIssueInTx — never UpdateClaimedIssueInTx — so the
// fix's new arm branch must not fire for it.
func TestPlainAssigneeUpdateWithoutClaimStillClearsLease(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-2"
	expectIssuePreImage(mock, id, "", types.StatusOpen)
	mock.ExpectExec(`(?s)UPDATE issues SET updated_at`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM leases WHERE issue_id = ?")).
		WithArgs(id).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id FROM events`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO events`).WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := UpdateIssueInTx(context.Background(), tx, id, map[string]interface{}{
		"status":   string(types.StatusInProgress),
		"assignee": "carol",
	}, "carol")
	if err != nil {
		t.Fatalf("UpdateIssueInTx: %v", err)
	}
	if !result.Changed {
		t.Error("hand-doled claim reported no persisted mutation")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
