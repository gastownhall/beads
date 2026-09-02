package issueops

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

type recentUTCWholeSecond struct{}

func (recentUTCWholeSecond) Match(value driver.Value) bool {
	stamp, ok := value.(time.Time)
	if !ok || stamp.Location() != time.UTC || stamp.Nanosecond() != 0 {
		return false
	}
	now := time.Now().UTC()
	return !stamp.Before(now.Add(-5*time.Second)) && !stamp.After(now.Add(5*time.Second))
}

func TestRecordProvenanceBindsExplicitUTCIngestTime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	ref := "0123456789abcdef0123456789abcdef01234567"
	refKind := "git-sha"
	event := types.ProvenanceEvent{
		IssueID: "bd-provenance-utc",
		Kind:    types.ProvLand,
		Source:  "utc-regression",
		Ref:     &ref,
		RefKind: &refKind,
	}
	id := ProvenanceEventID(event)

	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT IGNORE INTO provenance_events
			(id, issue_id, kind, actor, ref, ref_kind, payload, source, occurred_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)).WithArgs(
		id, event.IssueID, string(event.Kind), nil, ref, refKind, nil, event.Source, nil,
		recentUTCWholeSecond{},
	).WillReturnResult(sqlmock.NewResult(0, 1))

	gotID, inserted, err := RecordProvenanceEventInTx(context.Background(), tx, event)
	if err != nil {
		t.Fatalf("record provenance: %v", err)
	}
	if !inserted || gotID != id {
		t.Fatalf("record result = (%q, %v), want (%q, true)", gotID, inserted, id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

func TestRecordProvenancePreservesCallerOccurredAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	location := time.FixedZone("EDT", -4*60*60)
	callerTime := time.Date(2026, time.August, 25, 13, 48, 59, 987654321, location)
	wantOccurredAt := callerTime.UTC().Truncate(time.Second)
	ref := "fedcba9876543210fedcba9876543210fedcba98"
	refKind := "git-sha"
	event := types.ProvenanceEvent{
		IssueID:    "bd-provenance-at",
		Kind:       types.ProvCommit,
		Source:     "utc-regression",
		Ref:        &ref,
		RefKind:    &refKind,
		OccurredAt: &callerTime,
	}
	id := ProvenanceEventID(event)

	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT IGNORE INTO provenance_events
			(id, issue_id, kind, actor, ref, ref_kind, payload, source, occurred_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)).WithArgs(
		id, event.IssueID, string(event.Kind), nil, ref, refKind, nil, event.Source,
		wantOccurredAt, recentUTCWholeSecond{},
	).WillReturnResult(sqlmock.NewResult(0, 1))

	if _, _, err := RecordProvenanceEventInTx(context.Background(), tx, event); err != nil {
		t.Fatalf("record provenance: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}
