//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestGetDependentRecordsEmbedded mirrors the dolt-suite target-keyed dependents
// coverage on the embedded backend: direction, two-table span (durable + wisp
// sources), the type filter, row-id keyset paging with no drop/dup, and
// CountDependentRecords totals.
func TestGetDependentRecordsEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "dr")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "dr-target", Title: "target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-s1", Title: "s1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-s2", Title: "s2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dr-w", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "dr-s1", DependsOnID: "dr-target", Type: types.DepBlocks},
		{IssueID: "dr-s2", DependsOnID: "dr-target", Type: types.DepParentChild},
		{IssueID: "dr-w", DependsOnID: "dr-target", Type: types.DepBlocks}, // wisp source -> wisp_dependencies
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	set := func(deps []*types.Dependency) map[string]bool {
		m := map[string]bool{}
		for _, d := range deps {
			if d.ID == "" {
				t.Fatalf("dependent row missing ID (keyset cursor): %+v", d)
			}
			m[d.IssueID] = true
		}
		return m
	}

	all, err := te.store.GetDependentRecords(ctx, "dr-target", "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords: %v", err)
	}
	got := set(all)
	if len(got) != 3 || !got["dr-s1"] || !got["dr-s2"] || !got["dr-w"] {
		t.Fatalf("dependents = %v, want {dr-s1, dr-s2, dr-w} (spanning both tables)", got)
	}

	// Row-id keyset paging across the two-table boundary.
	seen := map[string]bool{}
	after := ""
	for i := 0; i < 10; i++ {
		page, err := te.store.GetDependentRecords(ctx, "dr-target", "", 1, after)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if seen[page[0].IssueID] {
			t.Fatalf("duplicate source %q across pages", page[0].IssueID)
		}
		seen[page[0].IssueID] = true
		after = page[0].ID
	}
	if len(seen) != 3 {
		t.Fatalf("paged sources = %v, want 3 distinct", seen)
	}

	// CountDependentRecords totals span both tables and honor the type filter.
	if n, err := te.store.CountDependentRecords(ctx, "dr-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 3 {
		t.Fatalf("CountDependentRecords(all) = %d, want 3", n)
	}
	if n, err := te.store.CountDependentRecords(ctx, "dr-target", string(types.DepBlocks)); err != nil {
		t.Fatalf("CountDependentRecords(blocks): %v", err)
	} else if n != 2 {
		t.Fatalf("CountDependentRecords(blocks) = %d, want 2 (dr-s1 + dr-w)", n)
	}
}

// perIssueComponentWalk is the walk storage.ComponentReader replaces: one read
// per issue and direction.
func perIssueComponentWalk(t *testing.T, te *testEnv, root *types.Issue) []string {
	t.Helper()
	ctx := t.Context()
	order := []string{root.ID}
	visited := map[string]bool{root.ID: true}
	for queue := []string{root.ID}; len(queue) > 0; {
		id := queue[0]
		queue = queue[1:]
		dependents, err := te.store.GetDependents(ctx, id)
		if err != nil {
			t.Fatalf("GetDependents(%s): %v", id, err)
		}
		dependencies, err := te.store.GetDependencies(ctx, id)
		if err != nil {
			t.Fatalf("GetDependencies(%s): %v", id, err)
		}
		for _, issue := range append(dependents, dependencies...) {
			if !visited[issue.ID] {
				visited[issue.ID] = true
				order = append(order, issue.ID)
				queue = append(queue, issue.ID)
			}
		}
	}
	return order
}

func componentIDs(component *storage.Component) []string {
	ids := make([]string, 0, len(component.Issues))
	for _, issue := range component.Issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

func dependencyKeys(deps []*types.Dependency) []string {
	keys := make([]string, 0, len(deps))
	for _, dep := range deps {
		keys = append(keys, dep.IssueID+">"+dep.DependsOnID+":"+string(dep.Type))
	}
	sort.Strings(keys)
	return keys
}

// TestReadComponentMatchesAPerIssueWalkEmbedded compares the batched walk with a
// copy of the per-issue walk over a random graph of durable issues and wisps,
// with cycles, diamonds, edges of every kind the walk follows and edges that name
// no issue. Each issue's edges must be what GetDependencyRecords returns.
func TestReadComponentMatchesAPerIssueWalkEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "rc")
	ctx := t.Context()

	const durable, wisps = 30, 8
	ids := make([]string, 0, durable+wisps)
	for i := 0; i < durable+wisps; i++ {
		id := fmt.Sprintf("rc-%02d", i)
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: i >= durable}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
		ids = append(ids, id)
	}
	rnd := rand.New(rand.NewSource(8240))
	depTypes := []types.DependencyType{types.DepRelated, types.DepRelatesTo, types.DepDiscoveredFrom}
	seen := make(map[[2]string]bool)
	for added := 0; added < 70; {
		from, to := ids[rnd.Intn(len(ids))], ids[rnd.Intn(len(ids))]
		if from == to || seen[[2]string{from, to}] {
			continue
		}
		seen[[2]string{from, to}] = true
		dep := &types.Dependency{IssueID: from, DependsOnID: to, Type: depTypes[rnd.Intn(len(depTypes))]}
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", from, to, err)
		}
		added++
	}

	for _, rootID := range []string{"rc-00", "rc-07", "rc-19", "rc-29", "rc-30", "rc-37"} {
		root, err := te.store.GetIssue(ctx, rootID)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", rootID, err)
		}
		component, err := te.store.ReadComponent(ctx, root)
		if err != nil {
			t.Fatalf("ReadComponent(%s): %v", rootID, err)
		}
		got, want := componentIDs(component), perIssueComponentWalk(t, te, root)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("walk from %s:\n got %v\nwant %v", rootID, got, want)
		}
		for _, id := range got {
			records, err := te.store.GetDependencyRecords(ctx, id)
			if err != nil {
				t.Fatalf("GetDependencyRecords(%s): %v", id, err)
			}
			if g, w := dependencyKeys(component.Outgoing[id]), dependencyKeys(records); !reflect.DeepEqual(g, w) {
				t.Errorf("edges of %s:\n got %v\nwant %v", id, g, w)
			}
		}
	}
}

// An id can exist in both planes after a pull brings another clone's promoted
// issue next to a local wisp copy. The walk follows the durable row; the edge
// list stays with the plane GetDependencyRecords reads, the wisp one.
func TestReadComponentFollowsBothPlanesOfADualIDEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "rd")
	ctx := t.Context()
	for _, id := range []string{"rd-dual", "rd-target"} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "rd-dual", DependsOnID: "rd-target", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	te.exec(t, ctx, "INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) "+
		"VALUES ('rd-dual', 'dual', '', '', '', '', 'open', 2, 'task', 1)")

	root, err := te.store.GetIssue(ctx, "rd-dual")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	component, err := te.store.ReadComponent(ctx, root)
	if err != nil {
		t.Fatalf("ReadComponent: %v", err)
	}
	if got, want := componentIDs(component), perIssueComponentWalk(t, te, root); !reflect.DeepEqual(got, want) || len(got) != 2 {
		t.Errorf("walk = %v, want %v, two issues", got, want)
	}
	if deps := component.Outgoing["rd-dual"]; len(deps) != 0 {
		t.Errorf("edges of the dual id = %v, want none from the wisp plane", deps)
	}
}

func TestReadComponentToleratesAMissingWispDependencyTableEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "rm")
	ctx := t.Context()
	for _, id := range []string{"rm-a", "rm-b", "rm-c"} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	for _, edge := range [][2]string{{"rm-b", "rm-a"}, {"rm-a", "rm-c"}} {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: edge[0], DependsOnID: edge[1], Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
	}
	te.exec(t, ctx, "RENAME TABLE wisp_dependencies TO wisp_dependencies_renamed_away")

	root, err := te.store.GetIssue(ctx, "rm-a")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	component, err := te.store.ReadComponent(ctx, root)
	if err != nil {
		t.Fatalf("ReadComponent: %v", err)
	}
	if got, want := componentIDs(component), []string{"rm-a", "rm-b", "rm-c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("walk = %v, want %v", got, want)
	}
}

// TestGetDependentRecordsForIssuesEmbedded mirrors the dolt-suite batched
// target-keyed coverage on the embedded backend: inbound edges keyed by target
// across a SET of targets in one call, spanning both tables (durable + wisp
// sources), with the FULL dep-type set (blocks, waits-for, conditional-blocks,
// parent-child) and real dep_type preserved, and a decoy (source==id) excluded.
func TestGetDependentRecordsForIssuesEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "bt")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "bt-x", Title: "x", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-y", Title: "y", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-blk", Title: "blk", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-wait", Title: "wait", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-cond", Title: "cond", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "bt-child", Title: "child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-yblk", Title: "yblk", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bt-z", Title: "z", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "bt-blk", DependsOnID: "bt-x", Type: types.DepBlocks},
		{IssueID: "bt-wait", DependsOnID: "bt-x", Type: types.DepWaitsFor},
		{IssueID: "bt-cond", DependsOnID: "bt-x", Type: types.DepConditionalBlocks}, // wisp source
		{IssueID: "bt-child", DependsOnID: "bt-x", Type: types.DepParentChild},
		{IssueID: "bt-yblk", DependsOnID: "bt-y", Type: types.DepBlocks},
		{IssueID: "bt-x", DependsOnID: "bt-z", Type: types.DepBlocks}, // decoy: bt-x is the SOURCE
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	typesBySrc := func(deps []*types.Dependency, target string) map[string]types.DependencyType {
		out := map[string]types.DependencyType{}
		for _, d := range deps {
			if d.DependsOnID != target {
				t.Fatalf("row keyed under %s has target %s", target, d.DependsOnID)
			}
			if d.ID == "" {
				t.Fatalf("dependent row missing ID: %+v", d)
			}
			out[d.IssueID] = d.Type
		}
		return out
	}

	byTarget, err := te.store.GetDependentRecordsForIssues(ctx, []string{"bt-x", "bt-y"})
	if err != nil {
		t.Fatalf("GetDependentRecordsForIssues: %v", err)
	}
	x := typesBySrc(byTarget["bt-x"], "bt-x")
	if len(x) != 4 || x["bt-blk"] != types.DepBlocks || x["bt-wait"] != types.DepWaitsFor ||
		x["bt-cond"] != types.DepConditionalBlocks || x["bt-child"] != types.DepParentChild {
		t.Fatalf("X dependents = %v, want the full 4-type set with real dep_type", x)
	}
	if _, bad := x["bt-z"]; bad {
		t.Fatalf("decoy edge X->Z surfaced as an inbound edge of X: %v", x)
	}
	y := typesBySrc(byTarget["bt-y"], "bt-y")
	if len(y) != 1 || y["bt-yblk"] != types.DepBlocks {
		t.Fatalf("Y dependents = %v, want {bt-yblk: blocks}", y)
	}
}
