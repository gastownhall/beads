package workapi

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// recordSource is a DetailSource whose count, hydrated rows and raw edge
// records are set independently, and which counts how often the raw records
// are read. skewSource cannot stand in: the raw read is the thing under test.
type recordSource struct {
	issue *types.Issue

	deps     []*types.IssueWithDependencyMetadata
	depsErr  error
	depCount int64
	countErr error

	records    []*types.Dependency
	recordsErr error

	recordReads int
}

func (s *recordSource) GetIssue(_ context.Context, _ string) (*types.Issue, error) {
	return s.issue, nil
}
func (s *recordSource) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	return nil, notFound(id)
}
func (s *recordSource) Labels(_ context.Context, _ string, _ bool) ([]string, error) {
	return nil, nil
}
func (s *recordSource) Dependencies(_ context.Context, _ string, _ bool) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps, s.depsErr
}
func (s *recordSource) DependencyRecords(_ context.Context, _ string, _ bool) ([]*types.Dependency, error) {
	s.recordReads++
	return s.records, s.recordsErr
}
func (s *recordSource) CountDependencies(_ context.Context, _ string, _ bool) (int64, error) {
	return s.depCount, s.countErr
}
func (s *recordSource) CountDependents(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (s *recordSource) CountComments(_ context.Context, _ string, _ bool) (int64, error) {
	return 0, nil
}
func (s *recordSource) IterDependents(_ context.Context, _ string, _ bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter[types.IssueWithDependencyMetadata](nil), nil
}
func (s *recordSource) IterComments(_ context.Context, _ string, _ bool) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter[types.Comment](nil), nil
}

func edgeRec(from, to string, t types.DependencyType) *types.Dependency {
	return &types.Dependency{IssueID: from, DependsOnID: to, Type: t}
}

// detailWire decodes the detail view the way a --json caller does, so the
// assertions pin the wire member and never the Go field name.
func detailWire(t *testing.T, details *types.IssueDetails) map[string]any {
	t.Helper()
	raw, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("marshal details: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}
	return out
}

// wireEdges returns unresolvable_dependency_edges as "target/type" pairs in
// order, and whether the member was present at all.
func wireEdges(t *testing.T, wire map[string]any) ([]string, bool) {
	t.Helper()
	v, present := wire["unresolvable_dependency_edges"]
	if !present {
		return nil, false
	}
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("unresolvable_dependency_edges = %#v, want an array", v)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		edge, _ := item.(map[string]any)
		out = append(out, edge["depends_on_id"].(string)+"/"+edge["type"].(string))
	}
	return out, true
}

func wireNumber(wire map[string]any, member string) (float64, bool) {
	v, ok := wire[member].(float64)
	return v, ok
}

func wireDependencyIDs(wire map[string]any) []string {
	list, _ := wire["dependencies"].([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		dep, _ := item.(map[string]any)
		id, _ := dep["id"].(string)
		out = append(out, id)
	}
	return out
}

// TestBuildIssueDetails_UnresolvableDependencyEdges pins #5272 on the detail
// view: the edges unresolvable_dependencies counts are named, as stored, in a
// member of their own, while dependencies stays the list of local neighbors.
func TestBuildIssueDetails_UnresolvableDependencyEdges(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1", Title: "subject", Status: types.StatusOpen}

	t.Run("cross-prefix blocks and external tracks are named, sorted by target", func(t *testing.T) {
		src := &recordSource{issue: issue, depCount: 2, records: []*types.Dependency{
			edgeRec("rp-1", "other-xyz", types.DepBlocks),
			edgeRec("rp-1", "external:proj:cap", types.DepTracks),
		}}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		wire := detailWire(t, details)
		edges, present := wireEdges(t, wire)
		if want := []string{"external:proj:cap/tracks", "other-xyz/blocks"}; !present || !reflect.DeepEqual(edges, want) {
			t.Errorf("unresolvable_dependency_edges = %v (present %v), want %v", edges, present, want)
		}
		// The existing members are untouched: the count keeps both edges, the
		// delta still says two, and the hydrated array stays empty.
		if n, _ := wireNumber(wire, "dependency_count"); n != 2 {
			t.Errorf("dependency_count = %v, want 2", wire["dependency_count"])
		}
		if n, _ := wireNumber(wire, "unresolvable_dependencies"); n != 2 {
			t.Errorf("unresolvable_dependencies = %v, want 2", wire["unresolvable_dependencies"])
		}
		if v, present := wire["dependencies"]; present {
			t.Errorf("dependencies = %v, want absent: neither far end has a row here", v)
		}
	})

	t.Run("one target under two types sorts by type", func(t *testing.T) {
		// Writes reject a second edge onto the same target, so only a fake
		// can produce this; the order must still be total.
		src := &recordSource{issue: issue, depCount: 2, records: []*types.Dependency{
			edgeRec("rp-1", "zz-1", types.DepTracks),
			edgeRec("rp-1", "zz-1", types.DepBlocks),
		}}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		edges, _ := wireEdges(t, detailWire(t, details))
		if want := []string{"zz-1/blocks", "zz-1/tracks"}; !reflect.DeepEqual(edges, want) {
			t.Errorf("unresolvable_dependency_edges = %v, want %v", edges, want)
		}
	})

	t.Run("mixed: local rows stay hydrated, only the missing edge is named", func(t *testing.T) {
		src := &recordSource{issue: issue, depCount: 2,
			deps: []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
			records: []*types.Dependency{
				edgeRec("rp-1", "rp-2", types.DepBlocks),
				edgeRec("rp-1", "other-xyz", types.DepBlocks),
			}}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		wire := detailWire(t, details)
		if ids := wireDependencyIDs(wire); !reflect.DeepEqual(ids, []string{"rp-2"}) {
			t.Errorf("dependencies = %v, want only the local rp-2", ids)
		}
		edges, _ := wireEdges(t, wire)
		if want := []string{"other-xyz/blocks"}; !reflect.DeepEqual(edges, want) {
			t.Errorf("unresolvable_dependency_edges = %v, want %v", edges, want)
		}
	})

	t.Run("fully local and edgeless issues read no raw records", func(t *testing.T) {
		for name, src := range map[string]*recordSource{
			"local": {issue: issue, depCount: 1, deps: []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
				records: []*types.Dependency{edgeRec("rp-1", "rp-2", types.DepBlocks)}},
			"no edges": {issue: issue},
		} {
			details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
			if err != nil {
				t.Fatalf("%s: BuildIssueDetails: %v", name, err)
			}
			if _, present := detailWire(t, details)["unresolvable_dependency_edges"]; present {
				t.Errorf("%s: unresolvable_dependency_edges present, want omitted", name)
			}
			if src.recordReads != 0 {
				t.Errorf("%s: raw records read %d times, want none", name, src.recordReads)
			}
		}
	})

	t.Run("failed reads publish no edges", func(t *testing.T) {
		boom := errors.New("backend down")
		recs := []*types.Dependency{edgeRec("rp-1", "other-xyz", types.DepBlocks)}
		tests := []struct {
			name      string
			src       *recordSource
			wantReads int
			wantCount bool
		}{
			{"count failed", &recordSource{issue: issue, depCount: 1, countErr: boom, records: recs}, 0, false},
			{"listing failed", &recordSource{issue: issue, depCount: 1, depsErr: boom, records: recs}, 0, false},
			// The count and listing succeeded, so the gap is measured and
			// stays published; only the names are withheld.
			{"raw read failed", &recordSource{issue: issue, depCount: 1, records: recs, recordsErr: boom}, 1, true},
		}
		for _, tc := range tests {
			details, err := BuildIssueDetails(ctx, tc.src, issue, false, DetailOptions{})
			if err != nil {
				t.Fatalf("%s: BuildIssueDetails: %v", tc.name, err)
			}
			wire := detailWire(t, details)
			if v, present := wire["unresolvable_dependency_edges"]; present {
				t.Errorf("%s: unresolvable_dependency_edges = %v, want absent", tc.name, v)
			}
			if _, present := wireNumber(wire, "unresolvable_dependencies"); present != tc.wantCount {
				t.Errorf("%s: unresolvable_dependencies present = %v, want %v", tc.name, present, tc.wantCount)
			}
			if tc.src.recordReads != tc.wantReads {
				t.Errorf("%s: raw records read %d times, want %d", tc.name, tc.src.recordReads, tc.wantReads)
			}
		}
	})

	t.Run("partial raw coverage names what it found and keeps the full count", func(t *testing.T) {
		// Three edges counted, one raw row on this plane: an opposite-plane
		// remnant stays counted and unnamed.
		src := &recordSource{issue: issue, depCount: 3,
			records: []*types.Dependency{edgeRec("rp-1", "other-xyz", types.DepBlocks)}}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		wire := detailWire(t, details)
		if n, _ := wireNumber(wire, "unresolvable_dependencies"); n != 3 {
			t.Errorf("unresolvable_dependencies = %v, want 3", wire["unresolvable_dependencies"])
		}
		edges, _ := wireEdges(t, wire)
		if want := []string{"other-xyz/blocks"}; !reflect.DeepEqual(edges, want) {
			t.Errorf("unresolvable_dependency_edges = %v, want %v", edges, want)
		}
	})

	t.Run("brief deps trims only the hydrated rows", func(t *testing.T) {
		src := &recordSource{issue: issue, depCount: 2,
			deps:    []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
			records: []*types.Dependency{edgeRec("rp-1", "other-xyz", types.DepBlocks), edgeRec("rp-1", "rp-2", types.DepBlocks)}}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{BriefDeps: true})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		edges, _ := wireEdges(t, detailWire(t, details))
		if want := []string{"other-xyz/blocks"}; !reflect.DeepEqual(edges, want) {
			t.Errorf("unresolvable_dependency_edges under BriefDeps = %v, want %v", edges, want)
		}
	})

	t.Run("the source's records are neither reordered nor aliased", func(t *testing.T) {
		recs := []*types.Dependency{
			edgeRec("rp-1", "zz-9", types.DepBlocks),
			edgeRec("rp-1", "aa-1", types.DepBlocks),
		}
		before := []*types.Dependency{recs[0], recs[1]}
		src := &recordSource{issue: issue, depCount: 2, records: recs}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if recs[0] != before[0] || recs[1] != before[1] {
			t.Errorf("the source's slice was reordered")
		}
		// Read by name so this file still compiles, and fails on behavior,
		// against a tree that predates the member.
		field := reflect.ValueOf(details).Elem().FieldByName("UnresolvableDependencyEdges")
		if !field.IsValid() || field.Len() != 2 {
			t.Fatalf("UnresolvableDependencyEdges missing or not two edges: %v", field)
		}
		for i := 0; i < field.Len(); i++ {
			got := field.Index(i).Interface().(*types.Dependency)
			if got == recs[0] || got == recs[1] {
				t.Errorf("published edge %s aliases the source's record", got.DependsOnID)
			}
		}
	})

	t.Run("an unresolved parent-child or blocks edge changes no derived field", func(t *testing.T) {
		src := &recordSource{issue: issue, depCount: 2, records: []*types.Dependency{
			edgeRec("rp-1", "other-parent", types.DepParentChild),
			edgeRec("rp-1", "other-xyz", types.DepBlocks),
		}}
		details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails: %v", err)
		}
		if details.Parent != nil {
			t.Errorf("Parent = %q from an unhydrated edge, want unset as on base", *details.Parent)
		}
		if len(details.GatedBy) != 0 || len(details.Dependencies) != 0 {
			t.Errorf("GatedBy = %v, Dependencies = %v; an unhydrated edge must reach neither", details.GatedBy, details.Dependencies)
		}
	})
}

// skewDepUC keeps fakeDepUC's per-table routing checks but counts the raw
// records, so the use-case adapter can be driven into the unresolvable branch.
type skewDepUC struct{ fakeDepUC }

func (f skewDepUC) CountByIssueID(_ context.Context, id string, _ domain.DepListFilter) (int64, error) {
	return int64(len(f.fx.records[id])), nil
}

func (f skewDepUC) CountByWispID(_ context.Context, id string, _ domain.DepListFilter) (int64, error) {
	return int64(len(f.fx.records[id])), nil
}

// TestBuildIssueDetails_UnresolvableDependencyEdgesRouteByPlane pins both
// adapters' raw reads: the use-case source asks the table the issue lives in
// (fakeDepUC fails a cross-plane call), and the store source asks the store.
func TestBuildIssueDetails_UnresolvableDependencyEdgesRouteByPlane(t *testing.T) {
	ctx := context.Background()
	fx := newDetailFixture()
	fx.records = map[string][]*types.Dependency{
		"bd-1": {
			edgeRec("bd-1", "bd-parent", types.DepParentChild),
			edgeRec("bd-1", "bd-blocker", types.DepBlocks),
			edgeRec("bd-1", "other-xyz", types.DepBlocks),
		},
		"bd-w1": {
			edgeRec("bd-w1", "bd-1", types.DepBlocks),
			edgeRec("bd-w1", "external:proj:cap", types.DepTracks),
		},
	}
	useCase := newUseCaseDetailSource(fakeIssueUC{fx: fx}, fakeLabelUC{fx: fx}, skewDepUC{fakeDepUC{fx: fx}}, fakeCommentUC{fx: fx})

	for _, tc := range []struct {
		id     string
		isWisp bool
		want   []string
	}{
		{"bd-1", false, []string{"other-xyz/blocks"}},
		{"bd-w1", true, []string{"external:proj:cap/tracks"}},
	} {
		issue, isWisp, err := GetIssueOrWisp(ctx, useCase, tc.id)
		if err != nil || isWisp != tc.isWisp {
			t.Fatalf("GetIssueOrWisp(%s) = wisp %v, err %v", tc.id, isWisp, err)
		}
		details, err := BuildIssueDetails(ctx, useCase, issue, isWisp, DetailOptions{})
		if err != nil {
			t.Fatalf("BuildIssueDetails(%s): %v", tc.id, err)
		}
		if edges, _ := wireEdges(t, detailWire(t, details)); !reflect.DeepEqual(edges, tc.want) {
			t.Errorf("%s: unresolvable_dependency_edges = %v, want %v", tc.id, edges, tc.want)
		}
	}

	// The store adapter forwards to GetDependencyRecords, which routes by
	// plane itself.
	store := NewStoreDetailSource(skewStoreReader{fakeStoreReader{fx: fx}})
	details, err := BuildIssueDetails(ctx, store, fx.issues["bd-1"], false, DetailOptions{})
	if err != nil {
		t.Fatalf("BuildIssueDetails store: %v", err)
	}
	if edges, _ := wireEdges(t, detailWire(t, details)); !reflect.DeepEqual(edges, []string{"other-xyz/blocks"}) {
		t.Errorf("store: unresolvable_dependency_edges = %v, want [other-xyz/blocks]", edges)
	}
}

type skewStoreReader struct{ fakeStoreReader }

func (f skewStoreReader) CountDependencies(_ context.Context, id string) (int64, error) {
	return int64(len(f.fx.records[id])), nil
}
