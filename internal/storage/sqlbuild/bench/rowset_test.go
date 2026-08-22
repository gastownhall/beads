package bench_test

import (
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlbuild/bench"
)

func TestCompareRowSets_IdenticalReorderedRows_NoDiffs(t *testing.T) {
	got := []bench.Row{
		{IssueID: "b", DepCount: 2, LabelsJSON: `["y","x"]`},
		{IssueID: "a", DepCount: 1, LabelsJSON: `["x","y"]`},
	}
	want := []bench.Row{
		{IssueID: "a", DepCount: 1, LabelsJSON: `["y","x"]`},
		{IssueID: "b", DepCount: 2, LabelsJSON: `["x","y"]`},
	}
	diffs := bench.CompareRowSets(got, want)
	if len(diffs) != 0 {
		t.Fatalf("expected no diffs for row-order- and label-order-insensitive identical sets, got %v", diffs)
	}
}

// TestCompareRowSets_ReorderedDepsJSON_NoDiff pins JSON-aware comparison for
// deps_json: SQL aggregation (JSON_ARRAYAGG/JSON_OBJECT) gives no ordering
// guarantee over element or key order, so a byte-for-byte string compare
// would report false mismatches between two SQL shapes that are actually
// equivalent.
func TestCompareRowSets_ReorderedDepsJSON_NoDiff(t *testing.T) {
	got := []bench.Row{
		{IssueID: "a", DepsJSON: `[{"issue_id":"a","depends_on_id":"x","type":"blocks"},{"issue_id":"a","depends_on_id":"y","type":"blocks"}]`},
	}
	want := []bench.Row{
		{IssueID: "a", DepsJSON: `[{"depends_on_id":"y","issue_id":"a","type":"blocks"},{"type":"blocks","depends_on_id":"x","issue_id":"a"}]`},
	}
	diffs := bench.CompareRowSets(got, want)
	if len(diffs) != 0 {
		t.Fatalf("expected deps_json element-order and key-order to be insensitive, got %v", diffs)
	}
}

func TestCompareRowSets_MissingRow(t *testing.T) {
	got := []bench.Row{{IssueID: "a"}}
	want := []bench.Row{{IssueID: "a"}, {IssueID: "b"}}
	diffs := bench.CompareRowSets(got, want)
	if len(diffs) != 1 || diffs[0].Kind != bench.DiffMissing || diffs[0].IssueID != "b" {
		t.Fatalf("got %v, want single missing diff for issue b", diffs)
	}
}

func TestCompareRowSets_ExtraRow(t *testing.T) {
	got := []bench.Row{{IssueID: "a"}, {IssueID: "c"}}
	want := []bench.Row{{IssueID: "a"}}
	diffs := bench.CompareRowSets(got, want)
	if len(diffs) != 1 || diffs[0].Kind != bench.DiffExtra || diffs[0].IssueID != "c" {
		t.Fatalf("got %v, want single extra diff for issue c", diffs)
	}
}

func TestCompareRowSets_FieldMismatch(t *testing.T) {
	cases := []struct {
		name      string
		got, want bench.Row
	}{
		{"dep_count", bench.Row{IssueID: "a", DepCount: 1}, bench.Row{IssueID: "a", DepCount: 2}},
		{"rdep_count", bench.Row{IssueID: "a", RDepCount: 1}, bench.Row{IssueID: "a", RDepCount: 2}},
		{"comment_count", bench.Row{IssueID: "a", CommentCount: 1}, bench.Row{IssueID: "a", CommentCount: 2}},
		{"parent_id", bench.Row{IssueID: "a", ParentID: "p1"}, bench.Row{IssueID: "a", ParentID: "p2"}},
		{"labels_json", bench.Row{IssueID: "a", LabelsJSON: `["x"]`}, bench.Row{IssueID: "a", LabelsJSON: `["y"]`}},
		{"deps_json", bench.Row{IssueID: "a", DepsJSON: `[{"issue_id":"a","depends_on_id":"x"}]`}, bench.Row{IssueID: "a", DepsJSON: `[{"issue_id":"a","depends_on_id":"y"}]`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diffs := bench.CompareRowSets([]bench.Row{tc.got}, []bench.Row{tc.want})
			if len(diffs) != 1 || diffs[0].Kind != bench.DiffMismatch || diffs[0].IssueID != "a" {
				t.Fatalf("got %v, want single mismatch diff for issue a (field %s)", diffs, tc.name)
			}
		})
	}
}

func TestCompareRowSets_EmptyBothSides(t *testing.T) {
	diffs := bench.CompareRowSets(nil, nil)
	if len(diffs) != 0 {
		t.Fatalf("expected no diffs for empty/empty, got %v", diffs)
	}
}

// TestCompareRowSets_DeterministicOrder pins stable output ordering so
// harness reports and test assertions don't flake on map iteration order.
func TestCompareRowSets_DeterministicOrder(t *testing.T) {
	got := []bench.Row{{IssueID: "z"}, {IssueID: "a"}, {IssueID: "m"}}
	diffs := bench.CompareRowSets(got, nil)
	var ids []string
	for _, d := range diffs {
		ids = append(ids, d.IssueID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("diffs not sorted by IssueID: %v", ids)
	}
}
