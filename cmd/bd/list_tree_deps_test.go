package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

func schedDep(from, to string) *types.Dependency {
	return &types.Dependency{IssueID: from, DependsOnID: to, Type: types.DepBlocks}
}

func idsOf(issues []*types.Issue) []string {
	out := make([]string, len(issues))
	for i, is := range issues {
		out[i] = is.ID
	}
	return out
}

func indexOf(ids []string, id string) int {
	for i, v := range ids {
		if v == id {
			return i
		}
	}
	return -1
}

func TestOrderSiblingsByDeps_DependencyBeatsPriority(t *testing.T) {
	// B is higher priority (P0) than A (P2) but B depends on A, so A must sort
	// first — dependency order overrides priority.
	a := &types.Issue{ID: "pa-a", Priority: 2}
	b := &types.Issue{ID: "pa-b", Priority: 0}
	deps := map[string][]*types.Dependency{"pa-b": {schedDep("pa-b", "pa-a")}}

	got := idsOf(orderSiblingsByDeps([]*types.Issue{b, a}, deps))
	if indexOf(got, "pa-a") > indexOf(got, "pa-b") {
		t.Fatalf("expected pa-a before pa-b (dependency order), got %v", got)
	}
}

func TestOrderSiblingsByDeps_ChainIsTopological(t *testing.T) {
	// c -> b -> a (c depends on b depends on a). Expect a, b, c.
	a := &types.Issue{ID: "pa-a", Priority: 1}
	b := &types.Issue{ID: "pa-b", Priority: 1}
	c := &types.Issue{ID: "pa-c", Priority: 1}
	deps := map[string][]*types.Dependency{
		"pa-c": {schedDep("pa-c", "pa-b")},
		"pa-b": {schedDep("pa-b", "pa-a")},
	}
	got := idsOf(orderSiblingsByDeps([]*types.Issue{c, b, a}, deps))
	if indexOf(got, "pa-a") > indexOf(got, "pa-b") || indexOf(got, "pa-b") > indexOf(got, "pa-c") {
		t.Fatalf("expected topological order a,b,c, got %v", got)
	}
}

func TestOrderSiblingsByDeps_CycleFallsBackWithoutDropping(t *testing.T) {
	// a <-> b cycle: must not hang and must not drop either node.
	a := &types.Issue{ID: "pa-a", Priority: 1}
	b := &types.Issue{ID: "pa-b", Priority: 1}
	deps := map[string][]*types.Dependency{
		"pa-a": {schedDep("pa-a", "pa-b")},
		"pa-b": {schedDep("pa-b", "pa-a")},
	}
	got := idsOf(orderSiblingsByDeps([]*types.Issue{b, a}, deps))
	if len(got) != 2 || indexOf(got, "pa-a") < 0 || indexOf(got, "pa-b") < 0 {
		t.Fatalf("cycle must preserve all nodes, got %v", got)
	}
}

func TestOrderSiblingsByDeps_OutOfGroupEdgeIgnored(t *testing.T) {
	// A depends on something outside the sibling group: no reordering, no panic.
	a := &types.Issue{ID: "pa-a", Priority: 2}
	b := &types.Issue{ID: "pa-b", Priority: 1}
	deps := map[string][]*types.Dependency{"pa-a": {schedDep("pa-a", "pa-external")}}
	got := idsOf(orderSiblingsByDeps([]*types.Issue{a, b}, deps))
	// Falls back to priority: b (P1) before a (P2).
	if indexOf(got, "pa-b") > indexOf(got, "pa-a") {
		t.Fatalf("expected priority order b,a for out-of-group edge, got %v", got)
	}
}

func TestDepEdgeDisplay(t *testing.T) {
	cases := []struct {
		name       string
		t          types.DependencyType
		label      string
		scheduling bool
		ok         bool
	}{
		{"parent-child", types.DepParentChild, "", false, false},
		{"blocks", types.DepBlocks, "depends-on", true, true},
		{"conditional-blocks", types.DepConditionalBlocks, "conditional-blocks", true, true},
		{"waits-for", types.DepWaitsFor, "waits-for", true, true},
		{"related", types.DepRelated, "related", false, true},
		{"relates-to", types.DepRelatesTo, "related", false, true},
		{"discovered-from", types.DepDiscoveredFrom, "discovered-from", false, true},
		{"duplicates", types.DepDuplicates, "duplicates", false, true},
		{"supersedes", types.DepSupersedes, "supersedes", false, true},
		{"replies-to", types.DepRepliesTo, "replies-to", false, true},
		{"custom-type", types.DependencyType("some-custom-type"), "some-custom-type", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			label, sched, ok := depEdgeDisplay(c.t)
			if label != c.label || sched != c.scheduling || ok != c.ok {
				t.Errorf("depEdgeDisplay(%q) = (%q,%v,%v), want (%q,%v,%v)",
					c.t, label, sched, ok, c.label, c.scheduling, c.ok)
			}
		})
	}
}

func TestAnnotationsFor_InViewRowsAndOutOfViewSummary(t *testing.T) {
	// pa-x: one in-view dep (pa-a) + five out-of-view deps. The in-view edge is a
	// full row; the five out-of-view collapse to one summary naming four + "1 more".
	inView := map[string]*types.Issue{
		"pa-x": {ID: "pa-x"},
		"pa-a": {ID: "pa-a", Title: "Alpha"},
	}
	deps := map[string][]*types.Dependency{"pa-x": {
		schedDep("pa-x", "pa-a"),
		schedDep("pa-x", "pa-o1"), schedDep("pa-x", "pa-o2"),
		schedDep("pa-x", "pa-o3"), schedDep("pa-x", "pa-o4"), schedDep("pa-x", "pa-o5"),
	}}
	dr := &depRender{mode: "scheduling", allDeps: deps, inView: inView}

	out := captureStdout(t, func() error { dr.annotationsFor("pa-x", ""); return nil })

	if !strings.Contains(out, "pa-a") || !strings.Contains(out, "Alpha") {
		t.Errorf("expected in-view row for pa-a Alpha, got:\n%s", out)
	}
	if !strings.Contains(out, "depends-on") {
		t.Errorf("expected [depends-on] label, got:\n%s", out)
	}
	if !strings.Contains(out, "5 outside this view") || !strings.Contains(out, "+1 more") {
		t.Errorf("expected collapsed out-of-view summary with +1 more, got:\n%s", out)
	}
	// The out-of-view targets must NOT appear as their own full rows.
	if strings.Count(out, "\n") > 2 {
		t.Errorf("expected 2 lines (1 in-view + 1 summary), got:\n%s", out)
	}
}

func TestAnnotationsFor_ModeFiltersKnowledgeGraph(t *testing.T) {
	inView := map[string]*types.Issue{
		"pa-x": {ID: "pa-x"},
		"pa-r": {ID: "pa-r", Title: "Related thing"},
	}
	deps := map[string][]*types.Dependency{"pa-x": {
		{IssueID: "pa-x", DependsOnID: "pa-r", Type: types.DepRelated},
	}}

	sched := captureStdout(t, func() error {
		(&depRender{mode: "scheduling", allDeps: deps, inView: inView}).annotationsFor("pa-x", "")
		return nil
	})
	if strings.Contains(sched, "pa-r") {
		t.Errorf("scheduling mode must hide 'related' edge, got:\n%s", sched)
	}

	all := captureStdout(t, func() error {
		(&depRender{mode: "all", allDeps: deps, inView: inView}).annotationsFor("pa-x", "")
		return nil
	})
	if !strings.Contains(all, "related") || !strings.Contains(all, "pa-r") {
		t.Errorf("all mode must show 'related' edge, got:\n%s", all)
	}
}

func TestAnnotationsFor_NilReceiverIsNoOp(t *testing.T) {
	var dr *depRender
	out := captureStdout(t, func() error { dr.annotationsFor("pa-x", ""); return nil })
	if out != "" {
		t.Errorf("nil depRender must print nothing, got:\n%s", out)
	}
}

// newListInputTestCmd builds a cobra command carrying only the flags the --deps
// combination checks read. gatherListInput reads many other flags, but GetString
// / GetBool on an unregistered flag return zero values, so this stays minimal.
func newListInputTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "list"}
	f := cmd.Flags()
	f.String("deps", "", "")
	f.String("format", "", "")
	f.Bool("flat", false, "")
	f.Bool("watch", false, "")
	f.Bool("tree", false, "")
	f.Bool("pretty", false, "")
	return cmd
}

// TestGatherListInput_DepsRejectsNonTreeOutputs guards the accept-and-ignore
// regression: --deps annotates the tree, so pairing it with a non-tree output
// mode must error rather than be silently dropped.
func TestGatherListInput_DepsRejectsNonTreeOutputs(t *testing.T) {
	cases := []struct {
		name string
		flag string
		val  string
	}{
		{"json", "format", "json"},
		{"format", "format", "{{.ID}}"},
		{"flat", "flat", "true"},
		{"watch", "watch", "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func(prev bool) { jsonOutput = prev }(jsonOutput)
			jsonOutput = false
			cmd := newListInputTestCmd()
			if err := cmd.Flags().Set("deps", "scheduling"); err != nil {
				t.Fatalf("set deps: %v", err)
			}
			if err := cmd.Flags().Set(c.flag, c.val); err != nil {
				t.Fatalf("set %s: %v", c.flag, err)
			}
			if _, err := gatherListInput(cmd); err == nil {
				t.Fatalf("--deps with --%s must be rejected, got nil error", c.name)
			}
		})
	}
}

// TestGatherListInput_DepsImpliesTree confirms a bare --deps is accepted and
// turns on the tree view (so it is not silently ignored on the plain-list path).
func TestGatherListInput_DepsImpliesTree(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false
	cmd := newListInputTestCmd()
	if err := cmd.Flags().Set("deps", "scheduling"); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	in, err := gatherListInput(cmd)
	if err != nil {
		t.Fatalf("bare --deps must be accepted, got error: %v", err)
	}
	if in.depsMode != "scheduling" {
		t.Errorf("depsMode = %q, want scheduling", in.depsMode)
	}
	if !in.prettyFormat {
		t.Error("--deps must imply the tree view (prettyFormat), got false")
	}
}

// TestGatherListInput_DepsRejectsInvalidValue keeps the value validation covered
// alongside the new combination checks.
func TestGatherListInput_DepsRejectsInvalidValue(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false
	cmd := newListInputTestCmd()
	if err := cmd.Flags().Set("deps", "bogus"); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	if _, err := gatherListInput(cmd); err == nil {
		t.Fatal("--deps=bogus must be rejected, got nil error")
	}
}
