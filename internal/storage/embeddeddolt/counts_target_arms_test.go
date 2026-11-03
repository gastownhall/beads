//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// eqTargetExpr resolves a dependency row's target id the way every count path
// did before the guarded per-column target arms (gascity#7666): one COALESCE
// over the three typed columns, in stored precedence order.
const eqTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

// TestCountPathsMatchCoalesceReference is the embedded-engine equivalence
// guard for gascity#7666. Every dependency count path whose target predicate
// was split into guarded UNION ALL arms — CountDependents/CountDependencies,
// CountDependentsByStatus, GetDependencyCounts, the GraphCounter's inbound
// tally, and the by-IDs reverse-blocker count behind ready work — must answer
// exactly what the pre-split COALESCE queries answered, on a real engine.
//
// The fixture plants, next to ordinary store-created edges:
//
//   - an edge held in BOTH planes at once (the durable-first far-end rule),
//   - a row naming the anchor in TWO typed target columns, one per plane —
//     only a raw INSERT can produce one, the ck_*_one_target CHECK
//     constraints are dropped for the moment — and exactly the row unguarded
//     arms would count twice under its second column,
//   - an external-target row whose external id collides with a local
//     anchor's id (arm 3 with a real match),
//   - an anchor whose id exists on both planes (issue and wisp rows).
//
// Each API answer is compared against a reference query that re-states the
// OLD COALESCE shape directly, so the arms are pinned to the semantics they
// replaced rather than to hand-computed totals. This change is a
// preservation: the test passes at the base by construction, and its value
// is holding the rewritten paths to that reference from here on.
func TestCountPathsMatchCoalesceReference(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "ea")
	ctx := t.Context()

	// Store-created rows. ea-target is the counted anchor and is ready (no
	// outgoing edges), so the ready-work page hydrates its counts through
	// the by-IDs form of the counts mega-query. ea-amb is a durable issue
	// whose id also exists as a wisp row (promoted below) — the state a
	// wisp promotion leaves behind, and the anchor whose multi-target rows
	// discriminate guarded arms from unguarded ones.
	for _, issue := range []*types.Issue{
		{ID: "ea-target", Title: "target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ea-amb", Title: "present on both planes", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ea-dep-open", Title: "open dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ea-dep-closed", Title: "closed dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ea-dep-weak", Title: "related dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ea-dup", Title: "both-plane dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ea-wisp-dep", Title: "wisp dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	if err := te.store.CloseIssue(ctx, "ea-dep-closed", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue ea-dep-closed: %v", err)
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "ea-dep-open", DependsOnID: "ea-target", Type: types.DepBlocks},
		{IssueID: "ea-dep-closed", DependsOnID: "ea-target", Type: types.DepBlocks},
		{IssueID: "ea-dep-weak", DependsOnID: "ea-target", Type: types.DepRelated},
		{IssueID: "ea-dup", DependsOnID: "ea-target", Type: types.DepBlocks},
		// Ephemeral source: the edge lands in wisp_dependencies.
		{IssueID: "ea-wisp-dep", DependsOnID: "ea-target", Type: types.DepBlocks},
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	// ea-amb and ea-dup as wisp rows: wisp_dependencies.issue_id is FK'd to
	// wisps(id), and the both-plane duplicate below needs ea-dup there.
	// wisps has defaults for everything but title.
	te.exec(t, ctx, "INSERT INTO wisps (id, title) VALUES ('ea-amb', 'promoted copy'), ('ea-dup', 'promoted copy')")

	// The same ea-dup -> ea-target edge as a wisp-plane row beside the
	// durable one: an edge in both planes at once. Every raw INSERT spells
	// the row id out: the embedded engine does not evaluate the tables'
	// DEFAULT (UUID()) and would refuse the insert without one.
	te.exec(t, ctx, "INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type) VALUES ('ea000000-0000-0000-0000-000000000001', 'ea-dup', 'ea-target', 'blocks')")

	// The one-target CHECK constraints keep store-created rows honest; the
	// arms still have to resolve a multi-target row the way COALESCE does,
	// so drop the checks and plant one per plane. Durable: ea-dep-open names
	// ea-amb in both typed id columns (COALESCE precedence: the issue
	// column wins). Wisp: ea-amb names itself in both.
	te.exec(t, ctx, "ALTER TABLE dependencies DROP CHECK ck_dep_one_target")
	te.exec(t, ctx, "ALTER TABLE wisp_dependencies DROP CHECK ck_wisp_dep_one_target")
	te.exec(t, ctx, "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_by) VALUES ('ea000000-0000-0000-0000-000000000002', 'ea-dep-open', 'ea-amb', 'ea-amb', 'blocks', 'tester')")
	te.exec(t, ctx, "INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type) VALUES ('ea000000-0000-0000-0000-000000000003', 'ea-amb', 'ea-amb', 'ea-amb', 'blocks')")

	// An external-target row whose external id collides with ea-target's
	// local id: arm 3 with a real match. depends_on_external has no FK, so
	// the collision is legal data.
	te.exec(t, ctx, "INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_by) VALUES ('ea000000-0000-0000-0000-000000000004', 'ea-dep-open', 'ea-target', 'related', 'tester')")

	countScalar := func(query string, args ...any) int64 {
		var n int64
		te.queryScalar(t, ctx, query, args, &n)
		return n
	}

	// inboundDistinct is the pre-split CountDependents shape: durable rows
	// whose COALESCE target is the anchor, plus wisp rows the durable plane
	// does not already hold an edge for — unfiltered by type, the
	// durable-first far-end rule.
	inboundDistinct := func(anchor string) int64 {
		durable := countScalar("SELECT COUNT(*) FROM dependencies WHERE "+eqTargetExpr+" = ?", anchor)
		wisp := countScalar("SELECT COUNT(*) FROM wisp_dependencies WHERE "+eqTargetExpr+
			" = ? AND issue_id NOT IN (SELECT issue_id FROM dependencies WHERE "+eqTargetExpr+" = ?)", anchor, anchor)
		return durable + wisp
	}
	// outboundDistinct is the pre-split CountDependencies shape: the source
	// row count per plane, minus wisp rows the durable plane already holds.
	outboundDistinct := func(source string) int64 {
		durable := countScalar("SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", source)
		wisp := countScalar("SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND "+eqTargetExpr+
			" NOT IN (SELECT "+eqTargetExpr+" FROM dependencies WHERE issue_id = ? AND "+eqTargetExpr+" IS NOT NULL)", source, source)
		return durable + wisp
	}
	// byStatus is the pre-split CountDependentsByStatus shape: per-plane
	// rows joined to each source's home issue table.
	byStatus := func(anchor, status string) int64 {
		durable := countScalar("SELECT COUNT(*) FROM dependencies d JOIN issues s ON s.id = d.issue_id WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND s.status = ?", anchor, status)
		wisp := countScalar("SELECT COUNT(*) FROM wisp_dependencies d JOIN wisps s ON s.id = d.issue_id WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND s.status = ?", anchor, status)
		return durable + wisp
	}
	// blocksInbound is the GetDependencyCounts DependentCount shape and the
	// ready-work reverse-blocker (rdep) shape: blocks rows per plane,
	// summed without a dedupe. blocksOutbound is its DependencyCount twin.
	blocksInbound := func(anchor string) int64 {
		durable := countScalar("SELECT COUNT(*) FROM dependencies WHERE type = 'blocks' AND "+eqTargetExpr+" = ?", anchor)
		wisp := countScalar("SELECT COUNT(*) FROM wisp_dependencies WHERE type = 'blocks' AND "+eqTargetExpr+" = ?", anchor)
		return durable + wisp
	}
	blocksOutbound := func(source string) int64 {
		durable := countScalar("SELECT COUNT(*) FROM dependencies WHERE type = 'blocks' AND issue_id = ?", source)
		wisp := countScalar("SELECT COUNT(*) FROM wisp_dependencies WHERE type = 'blocks' AND issue_id = ?", source)
		return durable + wisp
	}
	// edgeInbound is the GraphCounter's inbound tally: rows per plane,
	// summed, optionally narrowed by the dependent's status.
	edgeInbound := func(anchor, status string) int64 {
		durable := "SELECT COUNT(*) FROM dependencies d WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ?"
		wisp := "SELECT COUNT(*) FROM wisp_dependencies d WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ?"
		if status != "" {
			durable = "SELECT COUNT(*) FROM dependencies d JOIN issues s ON s.id = d.issue_id WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND s.status = ?"
			wisp = "SELECT COUNT(*) FROM wisp_dependencies d JOIN wisps s ON s.id = d.issue_id WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND s.status = ?"
		}
		var args []any
		if status != "" {
			args = []any{anchor, status}
		} else {
			args = []any{anchor}
		}
		return countScalar(durable, args...) + countScalar(wisp, args...)
	}

	// Fixture liveness: the comparisons below are only meaningful while the
	// references are nonzero — an anchor with no edges makes them vacuous.
	live := map[string]int64{
		"inbound(ea-target)":    inboundDistinct("ea-target"),
		"inbound(ea-amb)":       inboundDistinct("ea-amb"),
		"outbound(ea-dep-open)": outboundDistinct("ea-dep-open"),
		"byStatus(open)":        byStatus("ea-target", string(types.StatusOpen)),
		"byStatus(closed)":      byStatus("ea-target", string(types.StatusClosed)),
		"blocksIn(ea-target)":   blocksInbound("ea-target"),
		"blocksIn(ea-amb)":      blocksInbound("ea-amb"),
		"edgeIn(ea-target)":     edgeInbound("ea-target", ""),
		"edgeIn(open)":          edgeInbound("ea-target", string(types.StatusOpen)),
	}
	for name, want := range live {
		if want == 0 {
			t.Fatalf("fixture drift: reference %s computed 0; the comparisons below would be vacuous", name)
		}
	}

	// CountDependents / CountDependencies: one per distinct far end, the
	// durable-first rule included.
	for _, anchor := range []string{"ea-target", "ea-amb"} {
		want := inboundDistinct(anchor)
		got, err := te.store.CountDependents(ctx, anchor)
		if err != nil {
			t.Fatalf("CountDependents(%s): %v", anchor, err)
		}
		if got != want {
			t.Errorf("CountDependents(%s) = %d, want %d (COALESCE reference; unguarded arms would double-count the multi-target rows)", anchor, got, want)
		}
	}
	{
		want := outboundDistinct("ea-dep-open")
		got, err := te.store.CountDependencies(ctx, "ea-dep-open")
		if err != nil {
			t.Fatalf("CountDependencies(ea-dep-open): %v", err)
		}
		if got != want {
			t.Errorf("CountDependencies(ea-dep-open) = %d, want %d (COALESCE reference)", got, want)
		}
	}

	// CountDependentsByStatus: both statuses of the anchor whose dependents
	// span planes, target kinds, and types.
	for _, status := range []types.Status{types.StatusOpen, types.StatusClosed} {
		want := byStatus("ea-target", string(status))
		got, err := te.store.CountDependentsByStatus(ctx, "ea-target", status)
		if err != nil {
			t.Fatalf("CountDependentsByStatus(ea-target, %s): %v", status, err)
		}
		if got != want {
			t.Errorf("CountDependentsByStatus(ea-target, %s) = %d, want %d (COALESCE reference)", status, got, want)
		}
	}

	// GetDependencyCounts: blocks-only cardinalities in both directions.
	counts, err := te.store.GetDependencyCounts(ctx, []string{"ea-target", "ea-amb"})
	if err != nil {
		t.Fatalf("GetDependencyCounts: %v", err)
	}
	for _, anchor := range []string{"ea-target", "ea-amb"} {
		c, ok := counts[anchor]
		if !ok {
			t.Fatalf("GetDependencyCounts missing %s", anchor)
		}
		if want := blocksInbound(anchor); c.DependentCount != int(want) {
			t.Errorf("GetDependencyCounts(%s).DependentCount = %d, want %d (COALESCE reference)", anchor, c.DependentCount, want)
		}
		if want := blocksOutbound(anchor); c.DependencyCount != int(want) {
			t.Errorf("GetDependencyCounts(%s).DependencyCount = %d, want %d (COALESCE reference)", anchor, c.DependencyCount, want)
		}
	}

	// The GraphCounter's inbound tally: raw edge rows per plane, summed,
	// with and without a status narrowing.
	counter, err := te.store.GraphCounter()
	if err != nil {
		t.Fatalf("GraphCounter: %v", err)
	}
	for _, req := range []publicops.EdgeCountRequest{
		{Direction: publicops.EdgeDirectionIn, IDs: []string{"ea-target", "ea-amb"}},
		{Direction: publicops.EdgeDirectionIn, IDs: []string{"ea-target"}, Status: string(types.StatusOpen)},
	} {
		res, err := counter.CountEdges(ctx, req)
		if err != nil {
			t.Fatalf("CountEdges(%v): %v", req, err)
		}
		for _, entry := range res.Anchors {
			want := edgeInbound(entry.ID, req.Status)
			if entry.Count != want {
				t.Errorf("CountEdges(in, %s, status=%q).Count = %d, want %d (COALESCE reference)", entry.ID, req.Status, entry.Count, want)
			}
			if entry.Missing {
				t.Errorf("CountEdges(in, %s, status=%q): anchor exists but reported missing", entry.ID, req.Status)
			}
		}
	}

	// Ready work: a bounded page forces the by-IDs form of the counts
	// mega-query, whose reverse-blocker count was rewritten into the same
	// guarded arms. Both anchors are ready (no outgoing blocks edges on
	// the durable plane), so both should be on the page with the reference
	// rdep.
	ready, err := te.store.GetReadyWorkWithCounts(ctx, types.WorkFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetReadyWorkWithCounts: %v", err)
	}
	byID := make(map[string]*types.IssueWithCounts, len(ready))
	for _, row := range ready {
		if row != nil && row.Issue != nil {
			byID[row.Issue.ID] = row
		}
	}
	for _, anchor := range []string{"ea-target", "ea-amb"} {
		row, ok := byID[anchor]
		if !ok {
			t.Errorf("ready page missing %s — fixture drift; the by-IDs rdep comparison needs it (page: %d rows)", anchor, len(ready))
			continue
		}
		if want := blocksInbound(anchor); row.DependentCount != int(want) {
			t.Errorf("ready-work DependentCount(%s) = %d, want %d (COALESCE reference)", anchor, row.DependentCount, want)
		}
	}
}
