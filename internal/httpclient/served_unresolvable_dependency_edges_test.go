//go:build cgo

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// seedUnresolvableEdgeFixture plants ude-root with one local blocker and one
// `external:` tracks edge whose far end has no row in this database.
func seedUnresolvableEdgeFixture(t *testing.T, e *servedEnv) {
	t.Helper()
	ctx := t.Context()
	for _, row := range []*types.Issue{
		{ID: "ude-root", Title: "root", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "ude-dep", Title: "local blocker", Status: types.StatusOpen, IssueType: types.TypeTask},
	} {
		if err := e.createIssue(ctx, row, "seed"); err != nil {
			t.Fatalf("seed %s: %v", row.ID, err)
		}
	}
	for _, edge := range []*types.Dependency{
		{IssueID: "ude-root", DependsOnID: "ude-dep", Type: types.DepBlocks},
		{IssueID: "ude-root", DependsOnID: "external:liveop:liveop-kmf", Type: types.DepTracks, Metadata: `{"why":"convoy"}`},
	} {
		if err := e.addDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}
}

// AC-4 (bd-uschhs, #5850): the HTTP detail and the SDK Reader carry the
// exact stored record of an edge whose far end has no row here, beside a
// dependencies list of real issues only, and the count is the record count.
// The http answer equals the reference store's own Reader and combined read.
func TestServedDetailCarriesUnresolvableDependencyEdges(t *testing.T) {
	e := newServedEnv(t, "ude")
	ctx := t.Context()
	seedUnresolvableEdgeFixture(t, e)

	_, wantEdges, err := e.reference.GetDependenciesWithUnresolvable(ctx, "ude-root")
	if err != nil {
		t.Fatalf("reference GetDependenciesWithUnresolvable: %v", err)
	}
	if len(wantEdges) != 1 || wantEdges[0].DependsOnID != "external:liveop:liveop-kmf" {
		t.Fatalf("the REFERENCE answered %+v, the fixture expects one external record", wantEdges)
	}

	local, err := e.reference.IssueReader()
	if err != nil {
		t.Fatalf("reference IssueReader: %v", err)
	}
	remote, err := e.subject.IssueReader()
	if err != nil {
		t.Fatalf("http IssueReader: %v", err)
	}
	for _, side := range []struct {
		name   string
		reader issueops.Reader
	}{{"reference", local}, {"http", remote}} {
		details, err := side.reader.Get(ctx, issueops.GetRequest{ID: "ude-root"})
		if err != nil {
			t.Fatalf("%s Reader.Get: %v", side.name, err)
		}
		if len(details.Dependencies) != 1 || details.Dependencies[0].ID != "ude-dep" {
			t.Errorf("%s dependencies = %+v, want only the real issue ude-dep", side.name, details.Dependencies)
		}
		if details.UnresolvableDependencies == nil || *details.UnresolvableDependencies != 1 {
			t.Errorf("%s unresolvable_dependencies = %v, want 1", side.name, details.UnresolvableDependencies)
		}
		assertSameJSON(t, side.name+" unresolvable_dependency_edges", wantEdges, details.UnresolvableDependencyEdges)
	}
}

// AC-7 (bd-uschhs, #5850): over a current server the http combined read
// equals the reference store's own.
func TestServedGetDependenciesWithUnresolvableMatchesReference(t *testing.T) {
	e := newServedEnv(t, "ude")
	ctx := t.Context()
	seedUnresolvableEdgeFixture(t, e)

	wantDeps, wantEdges, err := e.reference.GetDependenciesWithUnresolvable(ctx, "ude-root")
	if err != nil {
		t.Fatalf("reference GetDependenciesWithUnresolvable: %v", err)
	}
	gotDeps, gotEdges, err := e.subject.GetDependenciesWithUnresolvable(ctx, "ude-root")
	if err != nil {
		t.Fatalf("http GetDependenciesWithUnresolvable: %v", err)
	}
	assertSameJSON(t, "resolved", wantDeps, gotDeps)
	assertSameJSON(t, "unresolvable", wantEdges, gotEdges)
}
