package bench_test

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/storage/sqlbuild/bench"
)

func TestMainShape_Name(t *testing.T) {
	shape := bench.MainShape()
	if shape.Name != "main" {
		t.Errorf("MainShape().Name = %q, want %q", shape.Name, "main")
	}
	if shape.Render == nil {
		t.Fatal("MainShape().Render is nil")
	}
}

// TestMainShape_MatchesProductionSearchCountsSQL pins MainShape as a pure
// passthrough to sqlbuild.SearchCountsSQL: the harness must never drift from
// what production actually runs, or an A/B comparison would be comparing a
// stand-in against itself instead of against a real candidate shape.
func TestMainShape_MatchesProductionSearchCountsSQL(t *testing.T) {
	tables := sqlbuild.IssuesFilterTables
	hyd := sqlbuild.CountsHydration{}

	wantSQL, wantArgs := sqlbuild.SearchCountsSQL(tables, nil, "WHERE i.status = 'open'", "ORDER BY i.priority", "LIMIT 50", true, hyd)
	gotSQL, gotArgs := bench.MainShape().Render(tables, nil, "WHERE i.status = 'open'", "ORDER BY i.priority", "LIMIT 50", true, hyd)

	if gotSQL != wantSQL {
		t.Errorf("MainShape SQL diverges from sqlbuild.SearchCountsSQL:\ngot:  %s\nwant: %s", gotSQL, wantSQL)
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("MainShape args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestMainShape_ByIDsForm(t *testing.T) {
	tables := sqlbuild.WispsFilterTables
	ids := []string{"w-1", "w-2"}
	hyd := sqlbuild.CountsHydration{Lite: true}

	wantSQL, wantArgs := sqlbuild.SearchCountsSQL(tables, ids, "", "", "", false, hyd)
	gotSQL, gotArgs := bench.MainShape().Render(tables, ids, "", "", "", false, hyd)

	if gotSQL != wantSQL {
		t.Error("MainShape by-IDs SQL diverges from sqlbuild.SearchCountsSQL")
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("MainShape by-IDs args = %v, want %v", gotArgs, wantArgs)
	}
}
