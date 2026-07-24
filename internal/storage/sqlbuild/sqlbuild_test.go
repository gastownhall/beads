package sqlbuild

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestOrderByKnownKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		sortBy   string
		sortDesc bool
		table    string
		want     string
	}{
		{"", false, "", "ORDER BY priority ASC, created_at DESC, id ASC"},
		{"priority", true, "", "ORDER BY priority DESC, created_at DESC, id ASC"},
		{"created", false, "", "ORDER BY created_at DESC, id ASC"},
		{"created", true, "", "ORDER BY created_at ASC, id ASC"},
		{"title", false, "i", "ORDER BY LOWER(i.title) ASC, i.id ASC"},
		{"updated", false, "i", "ORDER BY i.updated_at DESC, i.id ASC"},
		{"bogus-key", false, "", "ORDER BY priority ASC, created_at DESC, id ASC"},
		{"id", false, "", ""}, // Go-side sort
	}
	for _, tc := range cases {
		if got := OrderBy(tc.sortBy, tc.sortDesc, tc.table); got != tc.want {
			t.Errorf("OrderBy(%q, %v, %q) = %q, want %q", tc.sortBy, tc.sortDesc, tc.table, got, tc.want)
		}
	}
}

// TestUnionSortColumnsCoverSortDefs pins that every SQL-side sort key has a
// sort_* alias in UnionSortColumnsSQL, so UNION consumers can order by any
// key OrderByForColumns may emit.
func TestUnionSortColumnsCoverSortDefs(t *testing.T) {
	t.Parallel()

	for key := range SortDefs {
		alias := "sort_" + key
		if key == "" {
			alias = "sort_priority"
		}
		if !strings.Contains(UnionSortColumnsSQL, alias) {
			t.Errorf("UnionSortColumnsSQL missing alias %q for sort key %q", alias, key)
		}
	}
}

// TestLessMirrorsOrderBy spot-checks that the Go-side comparator agrees with
// the SQL default ordering on the documented tie-break chain: priority ASC,
// then created_at DESC, then id ASC.
func TestLessMirrorsOrderBy(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	older := now.Add(-time.Hour)
	a := &types.Issue{ID: "a", Priority: 1, CreatedAt: now}
	b := &types.Issue{ID: "b", Priority: 2, CreatedAt: now}
	if !Less(a, b, "", false) || Less(b, a, "", false) {
		t.Error("default sort must order priority 1 before priority 2")
	}
	c := &types.Issue{ID: "c", Priority: 1, CreatedAt: older}
	if !Less(a, c, "", false) {
		t.Error("equal priority must order newer created_at first (created_at DESC)")
	}
	d := &types.Issue{ID: "d", Priority: 1, CreatedAt: now}
	if !Less(a, d, "", false) || Less(d, a, "", false) {
		t.Error("full tie must break by id ASC")
	}
}

func TestBuildReadyWorkOrderPriorityFIFO(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		policy types.SortPolicy
		want   string
	}{
		{
			name:   "priority",
			policy: types.SortPolicyPriority,
			want:   "ORDER BY priority ASC, created_at ASC, id ASC",
		},
		{
			name:   "fallback",
			policy: types.SortPolicy("unknown"),
			want:   "ORDER BY priority ASC, created_at ASC, id ASC",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := BuildReadyWorkOrder(tc.policy, "created_at", "priority")
			if got.SQL != tc.want {
				t.Fatalf("BuildReadyWorkOrder(%q).SQL = %q, want %q", tc.policy, got.SQL, tc.want)
			}
		})
	}
}

func TestReadyWorkExcludeTypes(t *testing.T) {
	t.Parallel()

	base := ReadyWorkExcludeTypes(nil)
	seen := make(map[types.IssueType]bool, len(base))
	for _, typ := range base {
		if seen[typ] {
			t.Errorf("duplicate type %q in default exclude list", typ)
		}
		seen[typ] = true
	}
	for _, want := range []types.IssueType{"merge-request", types.TypeGate, types.TypeMolecule, "agent", "rig", "role", "message"} {
		if !seen[want] {
			t.Errorf("default exclude list missing %q", want)
		}
	}

	extended := ReadyWorkExcludeTypes([]types.IssueType{"custom", "", types.TypeGate})
	if got, want := len(extended), len(base)+1; got != want {
		t.Errorf("extras must dedupe and drop empties: len = %d, want %d", got, want)
	}
}

func TestBuildReadyWorkWhereBatchesIDSets(t *testing.T) {
	t.Parallel()

	ids := make([]string, QueryBatchSize+1)
	for i := range ids {
		ids[i] = "x-" + strings.Repeat("a", 3)
	}
	where, args, err := BuildReadyWorkWhere(types.WorkFilter{}, IssuesFilterTables, ReadyWorkWhereInputs{DeferredChildIDs: ids})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Count(where, "id NOT IN ("); got != 2 {
		t.Errorf("expected 2 batched NOT IN clauses for %d IDs, got %d", len(ids), got)
	}
	wantArgs := len(ids) + len(ReadyWorkExcludeTypes(nil))
	if len(args) != wantArgs {
		t.Errorf("args = %d, want %d", len(args), wantArgs)
	}
}

// wy-jpd3.2: --label-any was silently dropped on the ready/claim path (with or
// without --parent). BuildReadyWorkWhere must now emit an OR-set membership
// clause for LabelsAny that AND-combines with the AND-set Labels and the parent
// filter, so an atomic claim is actually fenced by the label filter it names.
func TestBuildReadyWorkWhereLabelsAny(t *testing.T) {
	t.Parallel()

	parent := "wy-jpd3"
	filter := types.WorkFilter{
		Labels:    []string{"tier:opus"},
		LabelsAny: []string{"lane-a", "lane-c"},
		ParentID:  &parent,
	}
	where, args, err := BuildReadyWorkWhere(filter, IssuesFilterTables, ReadyWorkWhereInputs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// OR-set: one subquery with an IN over all --label-any values.
	wantAny := "id IN (SELECT issue_id FROM " + IssuesFilterTables.Labels + " WHERE label IN (?, ?))"
	if !strings.Contains(where, wantAny) {
		t.Errorf("LabelsAny OR clause missing.\n where = %s\n want substring = %s", where, wantAny)
	}
	// AND-set still emits its own per-label subquery (equality, not IN).
	wantAnd := "id IN (SELECT issue_id FROM " + IssuesFilterTables.Labels + " WHERE label = ?)"
	if !strings.Contains(where, wantAnd) {
		t.Errorf("Labels AND clause missing.\n where = %s\n want substring = %s", where, wantAnd)
	}
	// Parent filter must survive alongside the label clauses.
	if !strings.Contains(where, "LIKE CONCAT(?, '.%')") {
		t.Errorf("parent filter clause missing when combined with labels.\n where = %s", where)
	}
	// The label + parent args land in filter order (AND labels, then LabelsAny
	// values, then the parent LIKE arg) as the tail of the arg list — the
	// default issue_type exclusion prepends its own args.
	wantTail := []interface{}{"tier:opus", "lane-a", "lane-c", parent}
	if len(args) < len(wantTail) {
		t.Fatalf("args = %v, want at least %d trailing values %v", args, len(wantTail), wantTail)
	}
	tail := args[len(args)-len(wantTail):]
	for i := range wantTail {
		if tail[i] != wantTail[i] {
			t.Errorf("tail[%d] = %v, want %v (full args: %v)", i, tail[i], wantTail[i], args)
		}
	}
}

func TestSearchCountsSQLShape(t *testing.T) {
	t.Parallel()

	sql, args := SearchCountsSQL(WispsFilterTables, nil, "WHERE x = ?", "ORDER BY y", "LIMIT 5", true, false)
	if args != nil {
		t.Errorf("predicate form must not return generated args, got %d", len(args))
	}
	for _, want := range []string{
		"FROM wisps i",
		"FROM wisp_dependencies",
		"FROM wisp_comments",
		"FROM wisp_labels",
		"UNION ALL", // wisp reverse deps included
		"WHERE x = ?",
		"ORDER BY y",
		"LIMIT 5",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("counts SQL missing %q", want)
		}
	}

	noWispDeps, _ := SearchCountsSQL(IssuesFilterTables, nil, "", "", "", false, true)
	if strings.Contains(noWispDeps, "UNION ALL") {
		t.Error("counts SQL must not union wisp reverse deps when probe says absent")
	}
	if strings.Contains(noWispDeps, "JSON_ARRAYAGG(label)") {
		t.Error("counts SQL must skip the labels join when skipLabels is set")
	}
	if !strings.Contains(noWispDeps, "NULL AS labels_json") {
		t.Error("counts SQL must project NULL labels_json when skipLabels is set")
	}

	// By-IDs form: driver and every subquery are constrained to the ids, and the
	// arg count matches the placeholder injection points (labels, dc, rc-deps,
	// rc-wisp, cc, pc, d, driver = 8 for the wisp family with labels).
	byIDs, idArgs := SearchCountsSQL(WispsFilterTables, []string{"a", "b"}, "", "", "", true, false)
	if !strings.Contains(byIDs, "WHERE i.id IN (?,?)") {
		t.Errorf("by-IDs counts SQL missing driver id filter:\n%s", byIDs)
	}
	if strings.Contains(byIDs, "ORDER BY") || strings.Contains(byIDs, "LIMIT") {
		t.Error("by-IDs counts SQL must not carry ORDER BY / LIMIT (order restored in Go)")
	}
	if len(idArgs) != 8*2 {
		t.Errorf("by-IDs args = %d, want %d", len(idArgs), 8*2)
	}

	// skipLabels drops the labels point and !includeWispReverseDeps drops the
	// rc-wisp point, leaving 6 injection points (dc, rc-deps, cc, pc, d, driver).
	_, idArgsNoLabels := SearchCountsSQL(IssuesFilterTables, []string{"a", "b"}, "", "", "", false, true)
	if len(idArgsNoLabels) != 6*2 {
		t.Errorf("by-IDs args (skipLabels, no wisp deps) = %d, want %d", len(idArgsNoLabels), 6*2)
	}
}
