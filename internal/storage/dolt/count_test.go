package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCountIssues_FilterParity is the be-nu4 §11.7 hard gate: for every
// meaningful filter field, CountIssues(filter) must equal
// len(SearchIssues("", filter)) at 1K rows. Divergence signals filter-clause
// drift between the two paths — which is the silent-count-drift UX hazard
// called out in the be-nu4.1 designer audit §4.
func TestCountIssues_FilterParity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSummaryParityFixture(t, store, 1000)

	open := types.StatusOpen
	inProgress := types.StatusInProgress
	closed := types.StatusClosed
	priority1 := 1
	pinnedTrue := true
	ephemeralTrue := true
	ephemeralFalse := false
	task := types.TypeTask
	bug := types.TypeBug
	assignee3 := "user-3"

	cases := []struct {
		name   string
		filter types.IssueFilter
	}{
		{"no_filter", types.IssueFilter{}},
		{"status_open", types.IssueFilter{Status: &open}},
		{"status_in_progress", types.IssueFilter{Status: &inProgress}},
		{"status_closed", types.IssueFilter{Status: &closed}},
		{"priority_1", types.IssueFilter{Priority: &priority1}},
		{"type_task", types.IssueFilter{IssueType: &task}},
		{"type_bug", types.IssueFilter{IssueType: &bug}},
		{"assignee", types.IssueFilter{Assignee: &assignee3}},
		{"label_all", types.IssueFilter{Labels: []string{"perf"}}},
		{"label_any", types.IssueFilter{LabelsAny: []string{"perf", "storage"}}},
		{"no_labels", types.IssueFilter{NoLabels: true}},
		{"no_assignee", types.IssueFilter{NoAssignee: true}},
		{"pinned_only", types.IssueFilter{Pinned: &pinnedTrue}},
		{"ephemeral_true", types.IssueFilter{Ephemeral: &ephemeralTrue}},
		{"ephemeral_false", types.IssueFilter{Ephemeral: &ephemeralFalse}},
		{"title_contains", types.IssueFilter{TitleContains: "summary"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues, err := store.SearchIssues(ctx, "", tc.filter)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			count, err := store.CountIssues(ctx, tc.filter)
			if err != nil {
				t.Fatalf("CountIssues: %v", err)
			}
			if count != len(issues) {
				t.Errorf("parity mismatch: CountIssues=%d, len(SearchIssues)=%d",
					count, len(issues))
			}
		})
	}
}

// TestCountIssuesGroupedBy_Parity asserts that the storage-layer group-by
// maps agree with a Go-side aggregation over SearchIssues results for every
// allowlisted field. Uses the summary parity fixture so label hydration
// exercises the two-phase label path plus the D2 wisp-set helper.
//
// Keys here are the raw storage keys ("1", "", "bug", …). The CLI layer
// post-processes them into display form ("P1", "(unassigned)", …) — that
// translation is exercised separately in cmd/bd/count_test.go.
func TestCountIssuesGroupedBy_Parity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSummaryParityFixture(t, store, 1000)

	filters := []struct {
		name   string
		filter types.IssueFilter
	}{
		{"no_filter", types.IssueFilter{}},
		{"status_open_filter", types.IssueFilter{Status: ptr(types.StatusOpen)}},
		{"type_task_filter", types.IssueFilter{IssueType: ptr(types.TypeTask)}},
	}

	fields := []string{"status", "priority", "issue_type", "assignee", "label"}

	for _, f := range filters {
		for _, field := range fields {
			name := f.name + "_" + field
			t.Run(name, func(t *testing.T) {
				got, err := store.CountIssuesGroupedBy(ctx, f.filter, field)
				if err != nil {
					t.Fatalf("CountIssuesGroupedBy(%s): %v", field, err)
				}

				want := goSideGroupBy(t, ctx, store, f.filter, field)
				if len(got) != len(want) {
					t.Errorf("group count mismatch for %s: got %d groups, want %d (got=%v want=%v)",
						field, len(got), len(want), got, want)
				}
				for k, wantN := range want {
					if got[k] != wantN {
						t.Errorf("group %q: got=%d want=%d (field=%s, filter=%s)",
							k, got[k], wantN, field, f.name)
					}
				}
				for k := range got {
					if _, ok := want[k]; !ok {
						t.Errorf("unexpected group %q in got (field=%s, filter=%s, count=%d)",
							k, field, f.name, got[k])
					}
				}
			})
		}
	}
}

// TestCountIssuesGroupedBy_AllowlistRejects verifies that unknown fields are
// rejected with a named-allowlist error. Per the designer audit (be-nu4.1
// §3), the error string must name all valid values so a future caller or
// CLI flag sees an actionable message, not a vague "validation failed".
func TestCountIssuesGroupedBy_AllowlistRejects(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for _, field := range []string{"", "bogus", "type", "id", "created_at", "labels"} {
		t.Run("field="+field, func(t *testing.T) {
			_, err := store.CountIssuesGroupedBy(ctx, types.IssueFilter{}, field)
			if err == nil {
				t.Fatalf("expected error for invalid field %q", field)
			}
			msg := err.Error()
			if !strings.Contains(msg, "invalid group-by field") {
				t.Errorf("error message should flag the allowlist violation; got %q", msg)
			}
			for _, valid := range []string{"status", "priority", "issue_type", "assignee", "label"} {
				if !strings.Contains(msg, valid) {
					t.Errorf("error message should name the valid field %q; got %q", valid, msg)
				}
			}
		})
	}
}

// goSideGroupBy computes the same map[string]int a SearchIssues-based loop
// would produce, using the raw storage keys that CountIssuesGroupedBy is
// contracted to return ("" for no-assignee / no-labels, stringified integer
// for priority, etc.). This is the oracle the parity test compares against.
func goSideGroupBy(t *testing.T, ctx context.Context, store *DoltStore, filter types.IssueFilter, field string) map[string]int {
	t.Helper()
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		t.Fatalf("oracle SearchIssues: %v", err)
	}
	want := make(map[string]int)
	for _, iss := range issues {
		switch field {
		case "status":
			want[string(iss.Status)]++
		case "priority":
			want[itoa(iss.Priority)]++
		case "issue_type":
			want[string(iss.IssueType)]++
		case "assignee":
			want[iss.Assignee]++ // "" bucket for no-assignee
		case "label":
			if len(iss.Labels) == 0 {
				want[""]++
				continue
			}
			for _, lb := range iss.Labels {
				want[lb]++
			}
		default:
			t.Fatalf("unknown field %q", field)
		}
	}
	return want
}

// itoa is a tiny allocation-free int→ASCII helper kept local to the test to
// avoid pulling in strconv just for a base-10 conversion that covers the
// 0..4 priority range the fixture exercises.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ptr is a pointer-from-value helper local to these tests; Go 1.21+ has
// similar one-liners but we keep this explicit for readability in table
// cases above.
func ptr[T any](v T) *T { return &v }
