package issueops

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// stubScanner is a test double that assigns deterministic non-zero values to
// every dest pointer it sees. It supports the pointer types used by
// ScanIssueFrom and ScanIssueLiteFrom — *string, *int, string-aliased types
// (via reflection), and the sql.Null* variants — without needing a real DB.
type stubScanner struct{}

func (stubScanner) Scan(dest ...any) error {
	for _, d := range dest {
		switch v := d.(type) {
		case *sql.NullString:
			// "stub" as a JSON array so ParseJSONStringArray on the
			// waiters slot yields ["a"]; benign for other slots.
			*v = sql.NullString{Valid: true, String: `["a"]`}
		case *sql.NullInt64:
			*v = sql.NullInt64{Valid: false}
		case *sql.NullTime:
			*v = sql.NullTime{Valid: false}
		default:
			rv := reflect.ValueOf(d).Elem()
			switch rv.Kind() {
			case reflect.String:
				rv.SetString("stub")
			case reflect.Int, reflect.Int64:
				rv.SetInt(0)
			}
		}
	}
	return nil
}

func parseSelectColumns(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		col := strings.TrimSpace(p)
		if col == "" {
			continue
		}
		out = append(out, col)
	}
	return out
}

func columnSet(cols []string) map[string]struct{} {
	set := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		set[c] = struct{}{}
	}
	return set
}

// TestIssueSelectColumns_LitePlusHeavyEqualsFull is a structural guard: every
// column in IssueSelectColumns must appear in exactly one of
// IssueSelectColumnsLite or HeavyDropList. Adding a column to
// IssueSelectColumns without classifying it into one of those two lists will
// fail this test with an actionable error message.
func TestIssueSelectColumns_LitePlusHeavyEqualsFull(t *testing.T) {
	t.Parallel()

	fullCols := parseSelectColumns(IssueSelectColumns)
	liteCols := parseSelectColumns(IssueSelectColumnsLite)

	fullSet := columnSet(fullCols)
	liteSet := columnSet(liteCols)
	dropSet := columnSet(HeavyDropList)

	reconstructed := make(map[string]struct{}, len(fullCols))
	for c := range liteSet {
		reconstructed[c] = struct{}{}
	}
	for c := range dropSet {
		reconstructed[c] = struct{}{}
	}

	var missing []string
	for c := range fullSet {
		if _, ok := reconstructed[c]; !ok {
			missing = append(missing, c)
		}
	}
	var extra []string
	for c := range reconstructed {
		if _, ok := fullSet[c]; !ok {
			extra = append(extra, c)
		}
	}

	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf(
			"IssueSelectColumnsLite + HeavyDropList != IssueSelectColumns.\n"+
				"Columns in full but not in (lite ∪ drop): %v\n"+
				"Columns in (lite ∪ drop) but not in full: %v\n"+
				"Action: classify each missing column into IssueSelectColumnsLite "+
				"(small, routing/listing reads it) or HeavyDropList (heavy body, "+
				"fetched via GetIssue when needed).",
			missing, extra,
		)
	}

	for col := range dropSet {
		if _, ok := liteSet[col]; ok {
			t.Errorf("IssueSelectColumnsLite contains heavy-drop column %q; classify it into exactly one list", col)
		}
	}
}

// TestScanIssueLiteFrom_LeavesHeavyFieldsBlank verifies the happy path for the
// lite scan helper: identity/metadata hydrate, the six heavy text columns
// remain zero-valued, and IsLitePartial is set so downstream code can detect
// the partial hydration.
func TestScanIssueLiteFrom_LeavesHeavyFieldsBlank(t *testing.T) {
	t.Parallel()

	issue, err := ScanIssueLiteFrom(stubScanner{})
	if err != nil {
		t.Fatalf("ScanIssueLiteFrom: %v", err)
	}

	if !issue.IsLitePartial {
		t.Error("ScanIssueLiteFrom: IsLitePartial = false, want true")
	}

	if issue.Description != "" {
		t.Errorf("ScanIssueLiteFrom: Description = %q, want blank", issue.Description)
	}
	if issue.Design != "" {
		t.Errorf("ScanIssueLiteFrom: Design = %q, want blank", issue.Design)
	}
	if issue.AcceptanceCriteria != "" {
		t.Errorf("ScanIssueLiteFrom: AcceptanceCriteria = %q, want blank", issue.AcceptanceCriteria)
	}
	if issue.Notes != "" {
		t.Errorf("ScanIssueLiteFrom: Notes = %q, want blank", issue.Notes)
	}
	if issue.Payload != "" {
		t.Errorf("ScanIssueLiteFrom: Payload = %q, want blank", issue.Payload)
	}
	if len(issue.Waiters) != 0 {
		t.Errorf("ScanIssueLiteFrom: Waiters = %v, want empty", issue.Waiters)
	}

	if issue.ID != "stub" {
		t.Errorf("ScanIssueLiteFrom: ID = %q, want %q (identity must still hydrate)", issue.ID, "stub")
	}
	if issue.Title != "stub" {
		t.Errorf("ScanIssueLiteFrom: Title = %q, want %q (lite must still hydrate)", issue.Title, "stub")
	}
}

// TestScanIssueFrom_PopulatesHeavyFields is the inverse: ScanIssueFrom must
// hydrate every heavy text column and must leave IsLitePartial false.
func TestScanIssueFrom_PopulatesHeavyFields(t *testing.T) {
	t.Parallel()

	issue, err := ScanIssueFrom(stubScanner{})
	if err != nil {
		t.Fatalf("ScanIssueFrom: %v", err)
	}

	if issue.IsLitePartial {
		t.Error("ScanIssueFrom: IsLitePartial = true, want false")
	}

	if issue.Description != "stub" {
		t.Errorf("ScanIssueFrom: Description = %q, want %q", issue.Description, "stub")
	}
	if issue.Design != "stub" {
		t.Errorf("ScanIssueFrom: Design = %q, want %q", issue.Design, "stub")
	}
	if issue.AcceptanceCriteria != "stub" {
		t.Errorf("ScanIssueFrom: AcceptanceCriteria = %q, want %q", issue.AcceptanceCriteria, "stub")
	}
	if issue.Notes != "stub" {
		t.Errorf("ScanIssueFrom: Notes = %q, want %q", issue.Notes, "stub")
	}
	// payload and waiters arrive via sql.NullString; the stub returns `["a"]`.
	if issue.Payload != `["a"]` {
		t.Errorf("ScanIssueFrom: Payload = %q, want %q", issue.Payload, `["a"]`)
	}
	if len(issue.Waiters) != 1 || issue.Waiters[0] != "a" {
		t.Errorf("ScanIssueFrom: Waiters = %v, want [\"a\"]", issue.Waiters)
	}
}
