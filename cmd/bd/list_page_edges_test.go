package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// edgeSourceStub answers EdgeReader from a fixed edge map keyed by source id,
// the shape GetAllDependencyRecords returns, and records each request's
// anchors so a test can assert which ids a rendering asked about.
type edgeSourceStub struct {
	edges     map[string][]*types.Dependency
	accessErr error
	readErr   error
	accessed  int
	requests  [][]string
}

func (s *edgeSourceStub) EdgeReader() (issueops.EdgeReader, error) {
	s.accessed++
	if s.accessErr != nil {
		return nil, s.accessErr
	}
	return edgeReaderStub{src: s}, nil
}

type edgeReaderStub struct{ src *edgeSourceStub }

func (r edgeReaderStub) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	r.src.requests = append(r.src.requests, slices.Clone(req.IDs))
	if r.src.readErr != nil {
		return issueops.EdgeReadResult{}, r.src.readErr
	}
	result := issueops.EdgeReadResult{Anchors: []issueops.AnchorEdges{}}
	for _, id := range req.IDs {
		edges := r.src.edges[id]
		if edges == nil {
			edges = []*types.Dependency{}
		}
		result.Anchors = append(result.Anchors, issueops.AnchorEdges{ID: id, Edges: edges})
	}
	return result, nil
}

func TestListDependencyRecordsReadsTheListedIssuesInOneRequest(t *testing.T) {
	a := &types.Issue{ID: "pe-a"}
	b := &types.Issue{ID: "pe-b"}
	c := &types.Issue{ID: "pe-c"}
	aToB := &types.Dependency{IssueID: "pe-a", DependsOnID: "pe-b", Type: types.DepBlocks}
	cToX := &types.Dependency{IssueID: "pe-c", DependsOnID: "pe-x", Type: types.DepParentChild}
	xToA := &types.Dependency{IssueID: "pe-x", DependsOnID: "pe-a", Type: types.DepBlocks}
	src := &edgeSourceStub{edges: map[string][]*types.Dependency{
		"pe-a": {aToB},
		"pe-c": {cToX},
		"pe-x": {xToA},
	}}

	got, err := listDependencyRecords(context.Background(), src, []*types.Issue{a, nil, b, c})
	if err != nil {
		t.Fatalf("listDependencyRecords: %v", err)
	}
	if want := [][]string{{"pe-a", "pe-b", "pe-c"}}; !reflect.DeepEqual(src.requests, want) {
		t.Errorf("ReadEdges requests = %v, want one naming the listed ids %v", src.requests, want)
	}
	// pe-b has no edges, so it has no key, as in GetAllDependencyRecords; the
	// unlisted pe-x's edge onto a listed issue is not read at all.
	want := map[string][]*types.Dependency{"pe-a": {aToB}, "pe-c": {cToX}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listDependencyRecords = %v, want %v", got, want)
	}
}

func TestListDependencyRecordsOfNoIssuesReadsNothing(t *testing.T) {
	src := &edgeSourceStub{accessErr: errors.New("must not be reached")}
	for _, issues := range [][]*types.Issue{nil, {}, {nil}} {
		got, err := listDependencyRecords(context.Background(), src, issues)
		if err != nil {
			t.Fatalf("listDependencyRecords(%v): %v", issues, err)
		}
		// Non-nil: the gate decoration takes a nil map to mean "nothing was
		// preloaded" and would read the edges a second time.
		if got == nil || len(got) != 0 {
			t.Errorf("listDependencyRecords(%v) = %#v, want an empty non-nil map", issues, got)
		}
	}
	if src.accessed != 0 {
		t.Errorf("an empty listing reached the edge reader %d times, want 0", src.accessed)
	}
}

func TestListDependencyRecordsReturnsTheReadError(t *testing.T) {
	issues := []*types.Issue{{ID: "pe-a"}}
	for name, src := range map[string]*edgeSourceStub{
		"accessor": {accessErr: errors.New("no edge reader")},
		"read":     {readErr: errors.New("read failed")},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := listDependencyRecords(context.Background(), src, issues)
			if err == nil {
				t.Fatalf("listDependencyRecords = %v, nil; want the %s error", got, name)
			}
			// nil, like a failed GetAllDependencyRecords: the tree then falls
			// back to dotted-id nesting and the gate decoration reads its own.
			if got != nil {
				t.Errorf("listDependencyRecords returned %v beside its error, want nil", got)
			}
		})
	}
}

// listEdgeFixture is a page of issues and the whole store's edge map around
// it. Besides an edge of every type the page itself owns, the store holds the
// edges a page-scoped read must be able to ignore: an unlisted child of a
// listed parent, unlisted issues that block or relate to listed ones, and a
// gate edge an unlisted issue owns. The page also leans on the renderers'
// other inputs: a parent that is not listed, a dotted child with no
// parent-child edge, a rooted parent-child cycle, open and closed gates, and a
// closed subject a gate does not decorate.
type listEdgeFixture struct {
	listed []*types.Issue
	all    map[string][]*types.Dependency
	stored map[string]*types.Issue // every issue in the store, for gate hydration
}

func newListEdgeFixture() listEdgeFixture {
	issue := func(id, title string, typ types.IssueType, status types.Status, priority int) *types.Issue {
		return &types.Issue{ID: id, Title: title, IssueType: typ, Status: status, Priority: priority}
	}
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	edge := func(from, to string, typ types.DependencyType) *types.Dependency {
		return &types.Dependency{IssueID: from, DependsOnID: to, Type: typ, CreatedAt: at, CreatedBy: "tester"}
	}

	listed := []*types.Issue{
		issue("pe-epic", "Epic", types.TypeEpic, types.StatusOpen, 0),
		issue("pe-a", "Alpha", types.TypeTask, types.StatusOpen, 1),
		issue("pe-b", "Bravo", types.TypeTask, types.StatusOpen, 1),
		issue("pe-c", "Charlie", types.TypeTask, types.StatusInProgress, 0),
		issue("pe-epic.1", "Dotted child", types.TypeTask, types.StatusOpen, 2),
		issue("pe-d", "Delta", types.TypeBug, types.StatusOpen, 2),
		issue("pe-gated", "Gated", types.TypeTask, types.StatusOpen, 1),
		issue("pe-shut", "Closed subject", types.TypeTask, types.StatusClosed, 1),
		issue("pe-r", "Cycle root", types.TypeEpic, types.StatusOpen, 3),
		issue("pe-cyc1", "Cycle one", types.TypeEpic, types.StatusOpen, 3),
		issue("pe-cyc2", "Cycle two", types.TypeEpic, types.StatusOpen, 3),
	}
	unlisted := []*types.Issue{
		issue("pe-x", "Unlisted blocker", types.TypeTask, types.StatusOpen, 2),
		issue("pe-far-parent", "Unlisted parent", types.TypeEpic, types.StatusOpen, 1),
		issue("pe-hidden-child", "Unlisted child", types.TypeTask, types.StatusClosed, 1),
		issue("pe-hidden", "Unlisted dependent", types.TypeTask, types.StatusClosed, 1),
		issue("pe-gate", "Open gate", types.TypeGate, types.StatusOpen, 1),
		issue("pe-gate-done", "Closed gate", types.TypeGate, types.StatusClosed, 1),
	}

	all := map[string][]*types.Dependency{
		// Owned by listed issues.
		"pe-a": {edge("pe-a", "pe-epic", types.DepParentChild)},
		"pe-b": {edge("pe-b", "pe-a", types.DepBlocks), edge("pe-b", "pe-epic", types.DepParentChild)},
		"pe-c": {edge("pe-c", "pe-b", types.DepWaitsFor), edge("pe-c", "pe-epic", types.DepParentChild), edge("pe-c", "pe-x", types.DepConditionalBlocks)},
		"pe-d": {
			edge("pe-d", "external:other:cap", types.DepBlocks),
			edge("pe-d", "pe-a", types.DepRelated),
			edge("pe-d", "pe-b", types.DepDuplicates),
			edge("pe-d", "pe-c", types.DepRepliesTo),
			edge("pe-d", "pe-far-parent", types.DepParentChild),
			edge("pe-d", "pe-gated", types.DependencyType("tracks")),
			edge("pe-d", "pe-old", types.DepSupersedes),
			edge("pe-d", "pe-x", types.DepDiscoveredFrom),
		},
		"pe-gated": {edge("pe-gated", "pe-gate", types.DepBlocks), edge("pe-gated", "pe-gate-done", types.DepBlocks)},
		"pe-shut":  {edge("pe-shut", "pe-gate", types.DepBlocks)},
		"pe-cyc1":  {edge("pe-cyc1", "pe-cyc2", types.DepParentChild), edge("pe-cyc1", "pe-r", types.DepParentChild)},
		"pe-cyc2":  {edge("pe-cyc2", "pe-cyc1", types.DepParentChild)},
		// Owned by unlisted issues, each pointing at a listed one.
		"pe-hidden-child": {edge("pe-hidden-child", "pe-epic", types.DepParentChild)},
		"pe-hidden":       {edge("pe-hidden", "pe-a", types.DepBlocks), edge("pe-hidden", "pe-d", types.DepRelated)},
		"pe-x":            {edge("pe-x", "pe-gate", types.DepBlocks), edge("pe-x", "pe-b", types.DepWaitsFor)},
		"pe-far-parent":   {edge("pe-far-parent", "pe-c", types.DepParentChild)},
	}

	stored := make(map[string]*types.Issue)
	for _, is := range append(slices.Clone(listed), unlisted...) {
		stored[is.ID] = is
	}
	return listEdgeFixture{listed: listed, all: all, stored: stored}
}

// hydrate is the gate decoration's candidate read against the fixture store.
func (f listEdgeFixture) hydrate(_ context.Context, ids []string) ([]*types.Issue, error) {
	var out []*types.Issue
	for _, id := range ids {
		if is := f.stored[id]; is != nil {
			out = append(out, is)
		}
	}
	return out, nil
}

// pick returns the listed issues with these ids, in this order.
func (f listEdgeFixture) pick(ids ...string) []*types.Issue {
	out := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		for _, is := range f.listed {
			if is.ID == id {
				out = append(out, is)
			}
		}
	}
	return out
}

// renderListTextOutputs renders every `bd list` text output that consumes an
// edge map, the way the direct route calls each one, and returns them by name.
// The gate decoration is computed from the same map, as the tree's caller does.
func renderListTextOutputs(t *testing.T, hydrate gateHydrator, issues []*types.Issue, deps map[string][]*types.Dependency) map[string]string {
	t.Helper()
	ctx := context.Background()
	noLoad := func(context.Context, []string) (map[string][]*types.Dependency, error) {
		t.Fatal("the gate decoration re-read edges although the caller preloaded them")
		return nil, nil
	}
	gated := gatesByIssueID(ctx, issues, deps, noLoad, hydrate)

	out := map[string]string{}
	for _, mode := range []string{"", "scheduling", "all"} {
		for _, sort := range []struct {
			by      string
			reverse bool
		}{{"", false}, {"", true}, {"title", false}, {"id", true}, {"created", false}} {
			for _, truncated := range []bool{false, true} {
				name := "tree deps=" + mode + " sort=" + sort.by + " truncated=" + map[bool]string{false: "no", true: "yes"}[truncated]
				if sort.reverse {
					name += " reverse"
				}
				out[name] = captureStdout(t, func() error {
					displayPrettyListWithDepsMode(issues, false, deps, mode, truncated, false, "", sort.by, sort.reverse, gated)
					return nil
				})
			}
		}
	}
	out["tree ready"] = captureStdout(t, func() error {
		displayPrettyListWithDepsMode(issues, false, deps, "scheduling", false, true, "open", "", false, gated)
		return nil
	})
	for _, format := range []string{"dot", "digraph", "{{.IssueID}} {{.Type}} {{.DependsOnID}} {{.Dependency.CreatedBy}} {{.Issue.Title}}"} {
		var buf bytes.Buffer
		if err := outputFormattedList(&buf, issues, deps, format); err != nil {
			t.Fatalf("outputFormattedList(%q): %v", format, err)
		}
		out["format "+format] = buf.String()
	}
	gatedIDs := make([]string, 0, len(gated))
	for id, gates := range gated {
		gatedIDs = append(gatedIDs, id+"<-"+strings.Join(gates, ","))
	}
	slices.Sort(gatedIDs)
	out["gated"] = strings.Join(gatedIDs, ";")
	return out
}

// assertSameListTextOutputs fails on each output the two edge maps rendered
// differently.
func assertSameListTextOutputs(t *testing.T, wholeStore, pageOnly map[string]string) {
	t.Helper()
	for output, want := range wholeStore {
		if got := pageOnly[output]; got != want {
			t.Errorf("%s differs between the page's edges and the whole store's.\nwhole store:\n%s\npage only:\n%s", output, want, got)
		}
	}
}

// TestListRenderingsReadOnlyTheListedIssuesEdges is the parity proof for
// listDependencyRecords: every `bd list` text rendering prints the same bytes
// from the listed issues' own edges as from the whole store's edge map, so
// reading only the former loses nothing. It covers the tree in each --deps
// mode and sort, a page cut by --limit, the --parent tree's shape (the parent
// listed with its descendants), --ready's footer, the gate decoration and
// every --format.
//
// It also guards the renderers: one that started reading an edge an UNLISTED
// issue owns (an incoming edge, say) would print something here it does not
// print from the page's edges, and this test would say which output changed.
func TestListRenderingsReadOnlyTheListedIssuesEdges(t *testing.T) {
	f := newListEdgeFixture()
	pages := map[string][]*types.Issue{
		"full page": f.listed,
		// A --limit cut: the parent stays, two of its children and the
		// dotted child go, the gate subject goes and the cycle loses its root.
		"truncated page": f.pick("pe-epic", "pe-c", "pe-d", "pe-shut", "pe-cyc1", "pe-cyc2"),
		"parent tree":    f.pick("pe-epic", "pe-a", "pe-b", "pe-c", "pe-epic.1"),
		"single row":     f.pick("pe-gated"),
		"no edges":       f.pick("pe-epic.1"),
	}
	for name, page := range pages {
		t.Run(name, func(t *testing.T) {
			src := &edgeSourceStub{edges: f.all}
			pageDeps, err := listDependencyRecords(context.Background(), src, page)
			if err != nil {
				t.Fatalf("listDependencyRecords: %v", err)
			}
			assertSameListTextOutputs(t,
				renderListTextOutputs(t, f.hydrate, page, f.all),
				renderListTextOutputs(t, f.hydrate, page, pageDeps))
		})
	}
}

// TestDisplayWatchedIssueListReadsTheShownIssuesEdges pins the --watch redraw
// to the shown issues' edges (it used to reload GetAllDependencyRecords on
// every change), with the tree it prints unchanged.
func TestDisplayWatchedIssueListReadsTheShownIssuesEdges(t *testing.T) {
	f := newListEdgeFixture()
	page := f.pick("pe-epic", "pe-a", "pe-b", "pe-c", "pe-d", "pe-epic.1")
	src := &edgeSourceStub{edges: f.all}

	// The header line carries the wall-clock time, so it is dropped before the
	// two renders are compared.
	stripClock := func(s string) string {
		var lines []string
		for _, line := range strings.Split(s, "\n") {
			if !strings.HasPrefix(line, "Beads - Open & In Progress (") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	got := stripClock(captureStdout(t, func() error {
		displayWatchedIssueList(context.Background(), src, page, false, false, "", "", false)
		return nil
	}))
	want := stripClock(captureStdout(t, func() error {
		displayPrettyListWithDepsMode(page, true, f.all, "", false, false, "", "", false, nil)
		return nil
	}))
	if got != want {
		t.Errorf("--watch redraw differs from the whole-store render.\nwhole store:\n%s\nwatch:\n%s", want, got)
	}
	wantIDs := [][]string{{"pe-epic", "pe-a", "pe-b", "pe-c", "pe-d", "pe-epic.1"}}
	if !reflect.DeepEqual(src.requests, wantIDs) {
		t.Errorf("--watch read the edges of %v, want only the shown issues %v", src.requests, wantIDs)
	}
}
