//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// openEmbeddedListEdgeStore opens a fresh embedded-Dolt store for
// TestEmbeddedListPageEdges, configured the way newTestStoreWithPrefix
// configures a server store.
func openEmbeddedListEdgeStore(t *testing.T) storage.DoltStorage {
	t.Helper()
	ctx := t.Context()
	s, err := embeddeddolt.Open(ctx, t.TempDir(), "beads", "main")
	if err != nil {
		t.Fatalf("open embedded store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SetConfig(ctx, "issue_prefix", "le"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := s.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		t.Fatalf("set types.custom: %v", err)
	}
	return s
}

// listEdgeStoreFixture is a seeded store and the ids it minted, by name.
type listEdgeStoreFixture struct {
	store storage.DoltStorage
	ids   map[string]string
}

func (f listEdgeStoreFixture) issues(t *testing.T, names ...string) []*types.Issue {
	t.Helper()
	ids := make([]string, len(names))
	for i, name := range names {
		ids[i] = f.ids[name]
	}
	got, err := f.store.GetIssuesByIDs(t.Context(), ids)
	if err != nil {
		t.Fatalf("GetIssuesByIDs(%v): %v", names, err)
	}
	byID := make(map[string]*types.Issue, len(got))
	for _, is := range got {
		byID[is.ID] = is
	}
	out := make([]*types.Issue, 0, len(ids))
	for i, id := range ids {
		if byID[id] == nil {
			t.Fatalf("issue %s (%s) not found", names[i], id)
		}
		out = append(out, byID[id])
	}
	return out
}

func (f listEdgeStoreFixture) idSet(names ...string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = f.ids[name]
	}
	slices.Sort(out)
	return out
}

// seedListEdgeStore builds, in both planes, the graph the page-scoped read has
// to answer exactly as the store-wide one does: parent-child children (one of
// them closed, so a default listing hides it), every edge type, an external
// reference, open and closed gates, a closed dependent pointing at listed
// issues, and wisps whose edges live in wisp_dependencies, one of them a child
// of a durable epic.
func seedListEdgeStore(t *testing.T, s storage.DoltStorage) listEdgeStoreFixture {
	t.Helper()
	ctx := t.Context()
	f := listEdgeStoreFixture{store: s, ids: map[string]string{}}
	create := func(name string, issue *types.Issue) {
		t.Helper()
		issue.Title = name
		issue.Status = types.StatusOpen
		if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		f.ids[name] = issue.ID
	}
	edge := func(from, to string, typ types.DependencyType) {
		t.Helper()
		target := f.ids[to]
		if target == "" {
			target = to // external or foreign references are stored verbatim
		}
		if err := s.AddDependency(ctx, &types.Dependency{IssueID: f.ids[from], DependsOnID: target, Type: typ}, "tester"); err != nil {
			t.Fatalf("add %s -[%s]-> %s: %v", from, typ, to, err)
		}
	}

	create("epic", &types.Issue{IssueType: types.TypeEpic, Priority: 0})
	create("alpha", &types.Issue{IssueType: types.TypeTask, Priority: 1})
	create("bravo", &types.Issue{IssueType: types.TypeTask, Priority: 2})
	create("delta", &types.Issue{IssueType: types.TypeBug, Priority: 3})
	create("other", &types.Issue{IssueType: types.TypeTask, Priority: 4})
	create("gate", &types.Issue{IssueType: types.TypeGate, Priority: 2})
	create("hidden", &types.Issue{IssueType: types.TypeTask, Priority: 1})
	create("gate done", &types.Issue{IssueType: types.TypeGate, Priority: 2})
	create("wisp", &types.Issue{IssueType: types.TypeTask, Priority: 2, Ephemeral: true})
	create("wisp child", &types.Issue{IssueType: types.TypeTask, Priority: 2, Ephemeral: true})

	edge("alpha", "epic", types.DepParentChild)
	edge("bravo", "epic", types.DepParentChild)
	edge("bravo", "alpha", types.DepBlocks)
	edge("bravo", "gate", types.DepBlocks)
	edge("bravo", "gate done", types.DepBlocks)
	edge("delta", "alpha", types.DepRelated)
	edge("delta", "bravo", types.DepWaitsFor)
	edge("delta", "other", types.DepDiscoveredFrom)
	edge("delta", "epic", types.DepConditionalBlocks)
	edge("delta", "external:other:cap", types.DepBlocks)
	edge("other", "delta", types.DepDuplicates)
	edge("hidden", "epic", types.DepParentChild)
	edge("hidden", "delta", types.DepBlocks)
	edge("hidden", "alpha", types.DepRelated)
	edge("wisp", "epic", types.DepParentChild)
	edge("wisp child", "wisp", types.DepParentChild)
	edge("wisp child", "alpha", types.DepBlocks)

	for _, name := range []string{"hidden", "gate done"} {
		if err := s.CloseIssue(ctx, f.ids[name], "done", "tester", ""); err != nil {
			t.Fatalf("close %s: %v", name, err)
		}
	}
	return f
}

// newListEdgeServerStore is a fresh store on the shared Dolt test server
// (newTestStoreWithPrefix skips when there is none).
func newListEdgeServerStore(t *testing.T) storage.DoltStorage {
	t.Helper()
	return newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), ".beads", "beads.db"), "le")
}

// TestListDependencyRecordsMatchesTheStoreWideRead runs
// checkListDependencyRecordsParity on the Dolt server; TestEmbeddedListPageEdges
// runs it on embedded Dolt.
func TestListDependencyRecordsMatchesTheStoreWideRead(t *testing.T) {
	checkListDependencyRecordsParity(t, seedListEdgeStore(t, newListEdgeServerStore(t)))
}

// checkListDependencyRecordsParity is the parity proof on a real store: for
// every listed issue, listDependencyRecords answers exactly the records
// GetAllDependencyRecords holds for it (same rows, same fields, same order,
// both planes), it answers nothing for an issue it was not asked about, and
// every `bd list` text rendering prints the same bytes from either map.
func checkListDependencyRecordsParity(t *testing.T, f listEdgeStoreFixture) {
	t.Helper()
	ctx := t.Context()
	all, err := f.store.GetAllDependencyRecords(ctx)
	if err != nil {
		t.Fatalf("GetAllDependencyRecords: %v", err)
	}

	pages := map[string][]string{
		"default listing": {"epic", "alpha", "bravo", "delta", "other"},
		"truncated page":  {"epic", "alpha"},
		"parent tree":     {"epic", "alpha", "bravo"},
		"with closed":     {"epic", "alpha", "bravo", "delta", "other", "hidden", "gate", "gate done"},
		"with wisps":      {"epic", "alpha", "wisp", "wisp child"},
		"wisps only":      {"wisp", "wisp child"},
	}
	for name, names := range pages {
		t.Run(name, func(t *testing.T) {
			page := f.issues(t, names...)
			got, err := listDependencyRecords(ctx, f.store, page)
			if err != nil {
				t.Fatalf("listDependencyRecords: %v", err)
			}
			listed := make(map[string]bool, len(page))
			for _, is := range page {
				listed[is.ID] = true
				if !reflect.DeepEqual(got[is.ID], all[is.ID]) {
					t.Errorf("edges of %s: page read %v, store-wide read %v", is.ID, describeDeps(got[is.ID]), describeDeps(all[is.ID]))
				}
			}
			for id := range got {
				if !listed[id] {
					t.Errorf("page read returned edges of %s, which is not listed", id)
				}
			}
			hydrate := func(ctx context.Context, ids []string) ([]*types.Issue, error) {
				return f.store.GetIssuesByIDs(ctx, ids)
			}
			assertSameListTextOutputs(t,
				renderListTextOutputs(t, hydrate, page, all),
				renderListTextOutputs(t, hydrate, page, got))
		})
	}
}

func describeDeps(deps []*types.Dependency) string {
	parts := make([]string, len(deps))
	for i, d := range deps {
		parts[i] = d.IssueID + " -[" + string(d.Type) + "]-> " + d.DependsOnID
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// listEdgeSpyStore is a real store that records how a command read its edges.
type listEdgeSpyStore struct {
	storage.DoltStorage
	wholeStoreReads int
	anchors         [][]string
}

func (s *listEdgeSpyStore) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	s.wholeStoreReads++
	return s.DoltStorage.GetAllDependencyRecords(ctx)
}

func (s *listEdgeSpyStore) EdgeReader() (issueops.EdgeReader, error) {
	inner, err := s.DoltStorage.EdgeReader()
	if err != nil {
		return nil, err
	}
	return spyEdgeReader{inner: inner, spy: s}, nil
}

type spyEdgeReader struct {
	inner issueops.EdgeReader
	spy   *listEdgeSpyStore
}

func (r spyEdgeReader) ReadEdges(ctx context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	r.spy.anchors = append(r.spy.anchors, slices.Clone(req.IDs))
	return r.inner.ReadEdges(ctx, req)
}

// TestListTextRenderingsReadTheListedIssuesEdgesOnly runs
// checkListTextRenderingsReadListedEdges on the Dolt server;
// TestEmbeddedListPageEdges runs it on embedded Dolt.
func TestListTextRenderingsReadTheListedIssuesEdgesOnly(t *testing.T) {
	checkListTextRenderingsReadListedEdges(t, seedListEdgeStore(t, newListEdgeServerStore(t)))
}

// checkListTextRenderingsReadListedEdges drives runListCore on the direct route
// over a real store and pins each text rendering that consumes edges to one
// edge read naming exactly the listed issues, never the store-wide
// GetAllDependencyRecords. backend/http's
// TestE2E_ListTextRenderingsReadOnlyTheListedIssuesEdges pins the same reads on
// the wire.
func checkListTextRenderingsReadListedEdges(t *testing.T, f listEdgeStoreFixture) {
	t.Helper()
	spy := &listEdgeSpyStore{DoltStorage: f.store}
	oldStore, oldCtx, oldJSON := store, rootCtx, jsonOutput
	store, rootCtx, jsonOutput = spy, t.Context(), false
	t.Cleanup(func() { store, rootCtx, jsonOutput = oldStore, oldCtx, oldJSON })

	gatedRow := ui.StatusIconGated + " " + f.ids["bravo"]
	for _, tc := range []struct {
		name    string
		args    []string
		anchors []string
		want    []string
	}{
		{"tree", nil, f.idSet("epic", "alpha", "bravo", "delta", "other"),
			[]string{"├── ○ " + f.ids["alpha"], gatedRow}},
		{"deps all", []string{"--deps=all"}, f.idSet("epic", "alpha", "bravo", "delta", "other"),
			[]string{"[depends-on]", "[related]", "[waits-for]", "[duplicates]", "↗ 1 outside this view: external:other:cap"}},
		{"truncated page", []string{"--limit", "2"}, f.idSet("epic", "alpha"),
			[]string{"└── ○ " + f.ids["alpha"]}},
		{"parent tree", []string{"--parent", f.ids["epic"]}, f.idSet("epic", "alpha", "bravo"),
			[]string{"├── ○ " + f.ids["alpha"], gatedRow}},
		{"format digraph", []string{"--format", "digraph"}, f.idSet("epic", "alpha", "bravo", "delta", "other"),
			[]string{f.ids["bravo"] + " " + f.ids["alpha"], f.ids["delta"] + " " + f.ids["bravo"]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy.wholeStoreReads, spy.anchors = 0, nil
			cmd := newListFlagsCommand(t, tc.args...)
			out := captureStdout(t, func() error { return runListCore(cmd, nil) })
			if spy.wholeStoreReads != 0 {
				t.Errorf("bd list %v read every edge in the store %d times", tc.args, spy.wholeStoreReads)
			}
			if len(spy.anchors) != 1 {
				t.Fatalf("bd list %v made %d edge reads, want 1: %v", tc.args, len(spy.anchors), spy.anchors)
			}
			if got := slices.Sorted(slices.Values(spy.anchors[0])); !slices.Equal(got, tc.anchors) {
				t.Errorf("bd list %v read the edges of %v, want exactly the listed issues %v", tc.args, got, tc.anchors)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("bd list %v output lacks %q:\n%s", tc.args, w, out)
				}
			}
			if strings.Contains(out, f.ids["hidden"]) {
				t.Errorf("bd list %v printed the closed issue %s:\n%s", tc.args, f.ids["hidden"], out)
			}
		})
	}
}
