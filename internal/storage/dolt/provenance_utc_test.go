package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestRecordProvenanceCreatedAtUsesUTC proves that provenance ingest time does
// not inherit the database session's local wall clock. DATETIME has no zone,
// so the producer must bind an explicit UTC value rather than rely on
// DEFAULT CURRENT_TIMESTAMP.
func TestRecordProvenanceCreatedAtUsesUTC(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	// Keep SET @@session.time_zone and the write on the same connection so the
	// regression would fail if the insert fell back to CURRENT_TIMESTAMP.
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := store.db.ExecContext(ctx, "SET @@session.time_zone = '-04:00'"); err != nil {
		t.Fatalf("set DST-equivalent non-UTC session time zone: %v", err)
	}

	issue := &types.Issue{
		ID:        "prov-utc-subject",
		Title:     "UTC provenance subject",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	ref := "0123456789abcdef0123456789abcdef01234567"
	refKind := "git-sha"
	event := types.ProvenanceEvent{
		IssueID: issue.ID,
		Kind:    types.ProvLand,
		Source:  "utc-regression",
		Ref:     &ref,
		RefKind: &refKind,
	}

	var utcBefore time.Time
	if err := store.db.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP()").Scan(&utcBefore); err != nil {
		t.Fatalf("read UTC time before provenance creation: %v", err)
	}

	id1, inserted1, err := store.RecordProvenanceEvent(ctx, event)
	if err != nil {
		t.Fatalf("record provenance: %v", err)
	}
	id2, inserted2, err := store.RecordProvenanceEvent(ctx, event)
	if err != nil {
		t.Fatalf("replay provenance: %v", err)
	}
	if !inserted1 || inserted2 || id1 != id2 {
		t.Fatalf("replay not idempotent: ids=%q/%q inserted=%v/%v", id1, id2, inserted1, inserted2)
	}

	var createdAt, sessionNow, utcAfter time.Time
	var rowCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT MIN(created_at), NOW(), UTC_TIMESTAMP(), COUNT(*)
		FROM provenance_events
		WHERE id = ?
	`, id1).Scan(&createdAt, &sessionNow, &utcAfter, &rowCount); err != nil {
		t.Fatalf("read provenance timestamp: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("idempotent replay left %d rows, want 1", rowCount)
	}
	if offset := utcAfter.Truncate(time.Second).Sub(sessionNow); offset != 4*time.Hour {
		t.Fatalf("session time zone offset = %v, want 4h", offset)
	}
	if createdAt.Nanosecond() != 0 {
		t.Fatalf("created_at nanoseconds = %d, want whole seconds", createdAt.Nanosecond())
	}
	if createdAt.Before(utcBefore.Add(-time.Second)) || createdAt.After(utcAfter.Add(time.Second)) {
		t.Fatalf("created_at = %v, want UTC ingest time between %v and %v", createdAt, utcBefore, utcAfter)
	}

	logged, err := store.GetProvenanceEvents(ctx, issue.ID, "")
	if err != nil {
		t.Fatalf("read provenance log: %v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("provenance log returned %d rows, want 1", len(logged))
	}
	if !logged[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at round-trip = %v, want %v", logged[0].CreatedAt, createdAt)
	}
	if logged[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at location = %v, want UTC", logged[0].CreatedAt.Location())
	}
	if logged[0].OccurredAt != nil {
		t.Fatalf("missing caller event-time was fabricated as %v", logged[0].OccurredAt)
	}
}
