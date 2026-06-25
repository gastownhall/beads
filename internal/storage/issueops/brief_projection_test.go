package issueops

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// splitBriefCols returns the brief projection's column names in order, parsed
// from the derived IssueSelectColumnsBrief so the test rows can never drift from
// the actual projection / ScanIssueBriefFrom scan order.
func splitBriefCols() []string {
	parts := strings.Split(IssueSelectColumnsBrief, ",")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

// briefIssueRowValues returns a value slice aligned to splitBriefCols(): non-null
// values for the directly-scanned non-nullable fields (id, title, status,
// priority, issue_type, compaction_level) + a routing metadata blob; everything
// else nil. The 7 omitted body columns are absent by construction.
func briefIssueRowValues(cols []string) []driver.Value {
	set := map[string]driver.Value{
		"id":               "brief-1",
		"content_hash":     "hash",
		"title":            "Brief Title",
		"status":           "open",
		"priority":         int64(2),
		"issue_type":       "bug",
		"compaction_level": int64(0),
		"metadata":         `{"gc.routed_to":"beads/voxist.reviewer"}`,
	}
	vals := make([]driver.Value, len(cols))
	for i, c := range cols {
		if v, ok := set[c]; ok {
			vals[i] = v
		} else {
			vals[i] = nil
		}
	}
	return vals
}

// TestIssueSelectColumnsBrief locks the derived brief column set against drift:
// it must drop exactly the 7 body columns and retain routing-critical columns.
func TestIssueSelectColumnsBrief(t *testing.T) {
	t.Parallel()
	for _, dropped := range bodyColumnsOmittedInBrief {
		for _, c := range splitBriefCols() {
			if c == dropped {
				t.Errorf("IssueSelectColumnsBrief must not contain body column %q", dropped)
			}
		}
	}
	keep := map[string]bool{"metadata": false, "title": false, "id": false, "status": false}
	for _, c := range splitBriefCols() {
		if _, ok := keep[c]; ok {
			keep[c] = true
		}
	}
	for c, present := range keep {
		if !present {
			t.Errorf("IssueSelectColumnsBrief must retain %q", c)
		}
	}
}

// TestScanIssueBrief round-trips a brief row through ScanIssueBriefFrom: the kept
// fields (id/title/status/metadata) populate, and the omitted body fields stay
// zero-valued.
func TestScanIssueBrief(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	cols := splitBriefCols()
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows(cols).AddRow(briefIssueRowValues(cols)...))

	rows, err := db.Query("SELECT brief")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatalf("expected one row")
	}
	issue, err := ScanIssueBriefFrom(rows)
	if err != nil {
		t.Fatalf("ScanIssueBriefFrom: %v", err)
	}

	if issue.ID != "brief-1" || issue.Title != "Brief Title" || string(issue.Status) != "open" {
		t.Errorf("kept fields wrong: id=%q title=%q status=%q", issue.ID, issue.Title, issue.Status)
	}
	if len(issue.Metadata) == 0 || !strings.Contains(string(issue.Metadata), "gc.routed_to") {
		t.Errorf("metadata (routing) must survive the brief scan, got %q", string(issue.Metadata))
	}
	if issue.Description != "" || issue.Design != "" || issue.AcceptanceCriteria != "" ||
		issue.Notes != "" || issue.CloseReason != "" || issue.Payload != "" || len(issue.Waiters) != 0 {
		t.Errorf("brief scan must leave body fields zero-valued, got desc=%q design=%q payload=%q waiters=%v",
			issue.Description, issue.Design, issue.Payload, issue.Waiters)
	}
}

// TestReadyWorkBriefKeepsMetadata drives the actual work-probe executor
// (runSearchQueryInTx, brief=true) with a fully-populated brief row + the 6
// composite count extras, and asserts the returned IssueWithCounts retains
// routing metadata + counts while the body fields are empty.
func TestReadyWorkBriefKeepsMetadata(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	cols := splitBriefCols()
	vals := briefIssueRowValues(cols)
	// Append the composite extras in the order scanReadyWorkRowWithScanner expects.
	extraCols := []string{"labels_json", "dep_count", "rdep_count", "comment_count", "parent_id", "deps_json"}
	extraVals := []driver.Value{nil, int64(3), int64(1), int64(2), nil, nil}
	cols = append(cols, extraCols...)
	vals = append(vals, extraVals...)

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows(cols).AddRow(vals...))

	out, err := runSearchQueryInTx(
		context.Background(), tx, IssuesFilterTables,
		"", "", "", nil, false, false, true, /*brief*/
	)
	if err != nil {
		t.Fatalf("runSearchQueryInTx(brief): %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 row, got %d", len(out))
	}
	iwc := out[0]
	if iwc.Issue.ID != "brief-1" || string(iwc.Issue.Status) != "open" {
		t.Errorf("issue identity wrong: id=%q status=%q", iwc.Issue.ID, iwc.Issue.Status)
	}
	if len(iwc.Issue.Metadata) == 0 || !strings.Contains(string(iwc.Issue.Metadata), "gc.routed_to") {
		t.Errorf("routing metadata must survive the brief work-probe, got %q", string(iwc.Issue.Metadata))
	}
	if iwc.DependencyCount != 3 || iwc.DependentCount != 1 || iwc.CommentCount != 2 {
		t.Errorf("composite counts wrong: dep=%d rdep=%d comment=%d", iwc.DependencyCount, iwc.DependentCount, iwc.CommentCount)
	}
	if iwc.Issue.Description != "" || iwc.Issue.Payload != "" || len(iwc.Issue.Waiters) != 0 {
		t.Errorf("brief work-probe must leave bodies empty, got desc=%q payload=%q", iwc.Issue.Description, iwc.Issue.Payload)
	}
}

// TestReadyWorkBriefProjection asserts that the work-probe projection builder,
// driven with brief=true, emits a SELECT that omits the 7 heavy free-text/blob
// body columns (the measured 7–12x cost driver on the high-frequency poll path)
// while still projecting the columns the probe consumes — crucially i.metadata
// (carries gc.routed_to for pool routing) and i.title (be-yvci).
func TestReadyWorkBriefProjection(t *testing.T) {
	t.Parallel()

	var captured string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(
		func(_ string, actual string) error {
			captured = actual
			return nil // capture-and-match-all; assertions run on `captured`
		},
	)))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Empty result set → the scan loop never runs, so we only exercise the
	// projection the builder emits.
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"id"}))

	if _, err := runSearchQueryInTx(
		context.Background(), tx, IssuesFilterTables,
		"", "", "", nil,
		false /*includeWispReverseDeps*/, false /*skipLabels*/, true, /*brief*/
	); err != nil {
		t.Fatalf("runSearchQueryInTx(brief): %v", err)
	}

	for _, dropped := range bodyColumnsOmittedInBrief {
		if strings.Contains(captured, "i."+dropped) {
			t.Errorf("brief work-probe SELECT must not project i.%s\nSQL:\n%s", dropped, captured)
		}
	}
	for _, kept := range []string{"i.metadata", "i.title", "i.id", "i.status"} {
		if !strings.Contains(captured, kept) {
			t.Errorf("brief work-probe SELECT must project %s\nSQL:\n%s", kept, captured)
		}
	}
}
