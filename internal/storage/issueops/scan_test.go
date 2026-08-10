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

// TestIssueSelectColumnsLite_IsFullMinusHeavyInOrder is the ORDER half of the
// guard above, and the set comparison cannot stand in for it. Both lists are
// scanned POSITIONALLY — ScanIssueFrom and ScanIssueLiteFrom bind destinations
// by index, and sqlbuild.SearchCountsSQL now renders a qualified variant of
// each into the counts mega-query — so two columns of the same SQL type can be
// transposed with no membership change at all: the set test stays green, the
// scan succeeds, and every row silently carries one column's value in the
// other's field. Nothing downstream can detect that, which is why it is pinned
// here rather than left to a case that would have to guess which pair moved.
//
// The oracle is the full list itself: deleting the heavy columns from
// IssueSelectColumns, in place, must reproduce IssueSelectColumnsLite exactly.
// That makes the lite list a DERIVATION rather than a second hand-maintained
// copy, so a column added to the full list in the middle cannot be appended to
// the end of the lite one.
func TestIssueSelectColumnsLite_IsFullMinusHeavyInOrder(t *testing.T) {
	t.Parallel()

	dropSet := columnSet(HeavyDropList)
	var want []string
	for _, col := range parseSelectColumns(IssueSelectColumns) {
		if _, heavy := dropSet[col]; !heavy {
			want = append(want, col)
		}
	}

	got := parseSelectColumns(IssueSelectColumnsLite)
	if len(got) != len(want) {
		t.Fatalf("IssueSelectColumnsLite has %d columns, want %d (IssueSelectColumns minus HeavyDropList)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IssueSelectColumnsLite[%d] = %q, want %q.\n"+
				"The lite list must be IssueSelectColumns with the heavy columns removed IN PLACE: both are scanned by\n"+
				"position, so a transposition of two same-typed columns swaps their values on every row without failing\n"+
				"any membership check.\n got: %v\nwant: %v", i, got[i], want[i], got, want)
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
