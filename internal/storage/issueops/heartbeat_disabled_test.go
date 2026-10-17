package issueops

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file pins the DISABLED path of the lease-heartbeat machinery
// (heartbeat.enabled=false, gastownhall/beads#7506), which a deployment
// reaches through config.yaml or BD_HEARTBEAT_ENABLED=0. The machinery is ON
// by default, so every case here says what it tests with an explicit
// WithHeartbeatEnabled(ctx, false) instead of inheriting anything — and in
// doing so each one also pins that the per-call override beats an ENABLED
// configuration. The disabled path must:
//
//   - claim still records assignee/status but arms NO lease,
//   - `bd heartbeat` keep its error contract — a missing issue, a closed or
//     released claim, or one held by someone else still fails — and only a
//     LIVE holder gets the successful no-op, so a worker's timer loop keeps
//     exiting 0 while its unit is alive and stops as soon as it is not,
//   - `bd reclaim` revert nothing.
//
// The ENABLED-path contracts live in public_claim_test.go,
// lease_reclaimer_test.go and the backend/conformance suite, all of which run
// on the default.

// disabledCtx is heartbeat.enabled=false for one call chain.
func disabledCtx() context.Context {
	return WithHeartbeatEnabled(context.Background(), false)
}

// TestHeartbeatDisabledClaimArmsNoLease: with the feature off, a winning claim
// CAS must NOT touch the ephemeral leases table. The mock expects the issues
// UPDATE but no INSERT INTO leases — any lease write would fail
// ExpectationsWereMet.
func TestHeartbeatDisabledClaimArmsNoLease(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectClaimableRead(mock, id, "", types.StatusOpen)
	mock.ExpectExec(`(?s)UPDATE issues\s+SET assignee`).WillReturnResult(sqlmock.NewResult(0, 1))
	// Deliberately NO lease INSERT expectation here.
	mock.ExpectQuery(`(?s)SELECT id FROM events`).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO events`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(claimIssueRow(id, claimActor, types.StatusInProgress))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"label"}))

	result, tables, err := ExecuteClaim(disabledCtx(), tx, publicops.ClaimRequest{Actor: claimActor, IssueID: id})
	if err != nil {
		t.Fatalf("ExecuteClaim: %v", err)
	}
	if !result.Changed {
		t.Error("a won CAS reported no persisted mutation")
	}
	if !tables["issues"] || !tables["events"] {
		t.Errorf("staged %v, want the issues row and the claim event", tables)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a disabled claim still touched the database unexpectedly: %v", err)
	}
}

// expectDisabledHeartbeatLook mocks the two reads a disabled heartbeat still
// makes: the wisp routing probe and the issue row the holder check reads. The
// row EXISTS with the given assignee and status — an empty assignee is a real
// value here (an unassigned open issue, what a reclaim or release leaves
// behind), not a missing row. Use expectDisabledHeartbeatNoRow for an id that
// resolves to nothing at all.
func expectDisabledHeartbeatLook(mock sqlmock.Sqlmock, id, assignee string, status types.Status) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(assignee, ''), status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"assignee", "status"}).AddRow(assignee, string(status)))
}

// expectDisabledHeartbeatRefusalReads mocks the one read the REFUSAL path
// still makes after the issue row: RenewLeaseInTx reads the caller's own
// lease row so a refusal can delete it when it survives as an orphan on an
// issue the caller no longer holds a live claim on (main's invariant that a
// lease row exists only while its claim is live). A disabled deployment
// arms no lease, so the row is absent and the read answers ErrNoRows: the
// refusal comes back unchanged and nothing is deleted. Callers that expect
// a refusal need this; the successful no-op does not reach it.
func expectDisabledHeartbeatRefusalReads(mock sqlmock.Sqlmock, id string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT holder FROM leases WHERE issue_id = ?")).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)
}

// expectDisabledHeartbeatNoRow mocks the same two reads for an id that
// resolves to no issue row at all (deleted, or never created).
func expectDisabledHeartbeatNoRow(mock sqlmock.Sqlmock, id string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(assignee, ''), status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"assignee", "status"}))
}

// TestHeartbeatDisabledHolderHeartbeatIsSuccessfulNoOp: with the feature off,
// the HOLDER's heartbeat returns nil. It still reads the issue row — that is
// what makes the answer mean "nothing to refresh" rather than "did not look" —
// and it issues no lease write at all. This is the exit-0 contract workers'
// heartbeat timer loops depend on.
func TestHeartbeatDisabledHolderHeartbeatIsSuccessfulNoOp(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectDisabledHeartbeatLook(mock, id, claimActor, types.StatusInProgress)

	if err := HeartbeatIssueInTx(disabledCtx(), tx, id, claimActor); err != nil {
		t.Fatalf("a disabled heartbeat by the holder must succeed as a no-op, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a disabled heartbeat did not run the reads it owes its caller: %v", err)
	}
}

// TestHeartbeatDisabledRefusesANonexistentIssue: turning the machinery off
// does not turn off the verb's error contract. An id that resolves to nothing
// still fails, so a worker that heartbeats a deleted or never-created issue
// stops instead of looping on success forever.
func TestHeartbeatDisabledRefusesANonexistentIssue(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-ghost"
	expectDisabledHeartbeatNoRow(mock, id)

	err := HeartbeatIssueInTx(disabledCtx(), tx, id, claimActor)
	if !errors.Is(err, storage.ErrNotClaimable) {
		t.Fatalf("err = %v, want ErrNotClaimable for an id that resolves to nothing", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHeartbeatDisabledRefusesANonHolder: likewise a heartbeat from anyone
// other than the holder still names the holder and fails, so the losing
// worker's loop stops. Without this the disabled path would report success to
// a process that does not hold the claim.
func TestHeartbeatDisabledRefusesANonHolder(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectDisabledHeartbeatLook(mock, id, "bob", types.StatusInProgress)
	expectDisabledHeartbeatRefusalReads(mock, id)

	err := HeartbeatIssueInTx(disabledCtx(), tx, id, claimActor)
	if !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("err = %v, want ErrAlreadyClaimed for a non-holder", err)
	}
	if !strings.Contains(err.Error(), "by bob") {
		t.Errorf("err = %q, want the holder named", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHeartbeatDisabledRefusesAnUnassignedOpenIssue: with the feature off, an
// issue that exists but is assigned to nobody (assignee empty, status open)
// is NOT a successful no-op — it is exactly the state a reclaimed, released,
// or hand-unclaimed claim leaves behind, and a worker hammering it must stop
// rather than loop at exit 0 forever. The disabled branch used to sit ABOVE
// the holder test, which reported every state between "missing" and "held" as
// success; this pins the reorder that closes it.
func TestHeartbeatDisabledRefusesAnUnassignedOpenIssue(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectDisabledHeartbeatLook(mock, id, "", types.StatusOpen)
	expectDisabledHeartbeatRefusalReads(mock, id)

	err := HeartbeatIssueInTx(disabledCtx(), tx, id, claimActor)
	if !errors.Is(err, storage.ErrNotClaimable) {
		t.Fatalf("err = %v, want ErrNotClaimable for an unassigned open issue", err)
	}
	if !strings.Contains(err.Error(), "status open") {
		t.Errorf("err = %q, want the status named", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHeartbeatDisabledRefusesAClosedClaim: with the feature off, a claim that
// was closed under the worker (assignee still names it, status closed) still
// fails with the status named — same answer the enabled path gives — so the
// worker's loop stops when its unit is finished instead of heartbeating a
// closed issue at exit 0 forever.
func TestHeartbeatDisabledRefusesAClosedClaim(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectDisabledHeartbeatLook(mock, id, claimActor, types.StatusClosed)
	expectDisabledHeartbeatRefusalReads(mock, id)

	err := HeartbeatIssueInTx(disabledCtx(), tx, id, claimActor)
	if !errors.Is(err, storage.ErrNotClaimable) {
		t.Fatalf("err = %v, want ErrNotClaimable for a closed claim", err)
	}
	if !strings.Contains(err.Error(), "status closed") {
		t.Errorf("err = %q, want the status named", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHeartbeatDisabledRefusalDeletesTheCallersOrphanedLease: the disabled
// path keeps the refusal's ONE write — deleting the caller's own lease row
// when it survives on an issue the caller no longer holds live. That cleanup
// is the same invariant the enabled path enforces, not a lease write the
// gate forbids: the gate forbids minting or refreshing one, and here the row
// is going away. Without the row the refusal is returned untouched, which is
// what expectDisabledHeartbeatRefusalReads' ErrNoRows pins above.
func TestHeartbeatDisabledRefusalDeletesTheCallersOrphanedLease(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectDisabledHeartbeatLook(mock, id, claimActor, types.StatusClosed)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT holder FROM leases WHERE issue_id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"holder"}).AddRow(claimActor))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM leases WHERE issue_id = ? AND holder = ?")).
		WithArgs(id, claimActor).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := HeartbeatIssueInTx(disabledCtx(), tx, id, claimActor)
	if !errors.Is(err, storage.ErrNotClaimable) {
		t.Fatalf("err = %v, want ErrNotClaimable for a closed claim", err)
	}
	var refusal *LeaseRenewalRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %T %v, want a LeaseRenewalRefusal", err, err)
	}
	if !refusal.OrphanDeleted {
		t.Error("the caller's orphaned lease row was not deleted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHeartbeatDisabledReclaimRevertsNothing: with the feature off,
// ReclaimExpiredLeasesInTx returns an empty result WITHOUT issuing any SQL, so
// `bd reclaim` on a supervisor timer exits 0 having reverted nothing.
func TestHeartbeatDisabledReclaimRevertsNothing(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	cutoff := time.Now().UTC()
	got, err := ReclaimExpiredLeasesInTx(disabledCtx(), tx, cutoff, types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("a disabled reclaim must succeed with an empty result, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a disabled reclaim reverted %d issues, want 0", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a disabled reclaim issued SQL: %v", err)
	}
}

// TestHeartbeatEnabledByDefaultArmsLease: the other half of the same pin.
// With no override and nothing configured — a bare context, exactly what a
// deployment that never heard of the key gets — a winning claim arms a lease.
// The mock expects the lease INSERT that only the enabled path performs.
func TestHeartbeatEnabledByDefaultArmsLease(t *testing.T) {
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

	_, _, err := ExecuteClaim(context.Background(), tx, publicops.ClaimRequest{Actor: claimActor, IssueID: id})
	if err != nil {
		t.Fatalf("ExecuteClaim with the default configuration: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a default claim did not arm the lease the enabled path owes it: %v", err)
	}
}

// TestHeartbeatDisabledConflictCopyTellsTheTruth: with the feature off, a
// claim refusal on an OPEN issue held by someone else must NOT promise lease
// expiry - no lease was armed at claim time, so nothing will ever surface it
// for bd reclaim. The copy is composed by storage.ClaimHolderSteeringRefusal,
// shared with the domain-stack twin, so this pins both producers.
func TestHeartbeatDisabledConflictCopyTellsTheTruth(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	// "held by another actor": the pre-image says the CAS cannot win, so no
	// UPDATE is attempted (ga-v2k49) - only the claimable read and the two
	// state re-reads the refusal carries.
	expectClaimableRead(mock, id, "bob", types.StatusOpen)
	expectClaimStateRead(mock, id, "bob", types.StatusOpen)
	expectClaimStateRead(mock, id, "bob", types.StatusOpen)

	_, _, err := ExecuteClaim(disabledCtx(), tx, publicops.ClaimRequest{Actor: claimActor, IssueID: id})
	var conflict *publicops.ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a *ClaimConflictError", err)
	}
	msg := conflict.Err.Error()
	if !strings.Contains(msg, "heartbeat.enabled=false") {
		t.Errorf("refusal = %q, want it to name the disabled flag", msg)
	}
	for _, banned := range []string{"lease expiry", "reclaim"} {
		if strings.Contains(msg, banned) {
			t.Errorf("refusal = %q, must not promise %q while no lease is armed", msg, banned)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHeartbeatEnabledConflictCopyPromisesLeaseRecovery: the same refusal with
// the machinery on keeps the lease-expiry sentence - an armed lease really
// will go stale and surface the claim for bd reclaim.
func TestHeartbeatEnabledConflictCopyPromisesLeaseRecovery(t *testing.T) {
	_, mock, tx := beginMockTx(t)

	const id = "bd-1"
	expectClaimableRead(mock, id, "bob", types.StatusOpen)
	expectClaimStateRead(mock, id, "bob", types.StatusOpen)
	expectClaimStateRead(mock, id, "bob", types.StatusOpen)

	ctx := WithHeartbeatEnabled(context.Background(), true)
	_, _, err := ExecuteClaim(ctx, tx, publicops.ClaimRequest{Actor: claimActor, IssueID: id})
	var conflict *publicops.ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a *ClaimConflictError", err)
	}
	if msg := conflict.Err.Error(); !strings.Contains(msg, "lease expiry will surface it for bd reclaim") {
		t.Errorf("refusal = %q, want the lease-expiry recovery sentence", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
