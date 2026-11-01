package issueops_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

type exportReader struct {
	page    []*types.IssueWithCounts
	hasMore bool
	lists   []issueops.ListRequest
	gets    []issueops.GetRequest
	err     error
	getErr  error
}

func (r *exportReader) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, errors.New("ready is not export's read")
}

func (r *exportReader) List(_ context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	r.lists = append(r.lists, req)
	if r.err != nil {
		return issueops.IssuePage{}, r.err
	}
	return issueops.IssuePage{Items: r.page, HasMore: r.hasMore}, nil
}

func (r *exportReader) Get(_ context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	r.gets = append(r.gets, req)
	if r.getErr != nil {
		return nil, r.getErr
	}
	return &issueops.IssueDetails{Comments: []*types.Comment{{IssueID: req.ID, Text: "c of " + req.ID}}}, nil
}

type exportEdges struct {
	calls   []issueops.EdgeReadRequest
	missing string
}

func (e *exportEdges) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	e.calls = append(e.calls, req)
	out := issueops.EdgeReadResult{Anchors: []issueops.AnchorEdges{}}
	for _, id := range req.IDs {
		a := issueops.AnchorEdges{ID: id, Edges: []*types.Dependency{}, Missing: id == e.missing}
		if id == "x-1" {
			a.Edges = []*types.Dependency{{IssueID: "x-1", DependsOnID: "x-2", Type: types.DepBlocks}}
		}
		out.Anchors = append(out.Anchors, a)
	}
	return out, nil
}

func exportPage() []*types.IssueWithCounts {
	return []*types.IssueWithCounts{
		{Issue: &types.Issue{ID: "x-1", Labels: []string{"l"}}, DependencyCount: 1, CommentCount: 2},
		{Issue: &types.Issue{ID: "x-2"}, DependentCount: 1},
		{Issue: &types.Issue{ID: "x-wisp", Ephemeral: true}},
		{Issue: &types.Issue{ID: "x-nh", NoHistory: true}},
		nil,
	}
}

// TestReadExportAllExportsEveryRowThroughRoles is S21: `bd export --all`
// composes served role reads, so a remote backend exports without the raw
// search and bulk relation reads it does not serve.
func TestReadExportAllExportsEveryRowThroughRoles(t *testing.T) {
	rd := &exportReader{page: exportPage()}
	edges := &exportEdges{}
	rows, err := issueops.ReadExport(context.Background(), rd, edges, issueops.ExportRequest{All: true})
	if err != nil {
		t.Fatalf("ReadExport: %v", err)
	}
	zero := 0
	wantList := issueops.ListRequest{AllFlag: true, IncludeGates: true, IncludeEphemeral: true, IncludeInfra: true, IncludeTemplates: true, Limit: &zero}
	if len(rd.lists) != 1 || !reflect.DeepEqual(rd.lists[0], wantList) {
		t.Fatalf("List calls = %#v, want one %#v", rd.lists, wantList)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.Issue.ID)
	}
	if want := []string{"x-1", "x-2", "x-wisp", "x-nh"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("rows = %v, want %v (every row, in list order)", ids, want)
	}
	if len(edges.calls) != 1 || !reflect.DeepEqual(edges.calls[0].IDs, ids) {
		t.Fatalf("ReadEdges calls = %#v, want one over %v", edges.calls, ids)
	}
	if got := rows[0].Issue.Dependencies; len(got) != 1 || got[0].DependsOnID != "x-2" {
		t.Fatalf("x-1 dependencies = %#v, want the stored edge onto x-2", got)
	}
	if rows[1].Issue.Dependencies != nil {
		t.Fatalf("x-2 dependencies = %#v, want none", rows[1].Issue.Dependencies)
	}
	// Only the row with comments is read for them.
	if want := []issueops.GetRequest{{ID: "x-1", IncludeComments: true}}; !reflect.DeepEqual(rd.gets, want) {
		t.Fatalf("Get calls = %#v, want %#v", rd.gets, want)
	}
	if c := rows[0].Issue.Comments; len(c) != 1 || c[0].Text != "c of x-1" {
		t.Fatalf("x-1 comments = %#v", c)
	}
	if rows[0].CommentCount != 2 || rows[0].DependencyCount != 1 || rows[1].DependentCount != 1 {
		t.Fatalf("counts lost: %+v %+v", rows[0], rows[1])
	}
	if !reflect.DeepEqual(rows[0].Issue.Labels, []string{"l"}) {
		t.Fatalf("labels lost: %v", rows[0].Issue.Labels)
	}
}

// TestReadExportDefaultDropsTrueWispsKeepsNoHistory pins the default scope:
// gates and the wisps plane are read, templates and infra are the listing's to
// exclude, and the true wisps are dropped while no-history rows stay (GH#3649).
func TestReadExportDefaultDropsTrueWispsKeepsNoHistory(t *testing.T) {
	for _, infra := range []bool{false, true} {
		rd := &exportReader{page: exportPage()}
		rows, err := issueops.ReadExport(context.Background(), rd, &exportEdges{}, issueops.ExportRequest{IncludeInfra: infra})
		if err != nil {
			t.Fatalf("ReadExport: %v", err)
		}
		zero := 0
		wantList := issueops.ListRequest{AllFlag: true, IncludeGates: true, IncludeEphemeral: true, IncludeInfra: infra, Limit: &zero}
		if !reflect.DeepEqual(rd.lists[0], wantList) {
			t.Fatalf("List = %#v, want %#v", rd.lists[0], wantList)
		}
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.Issue.ID)
		}
		if want := []string{"x-1", "x-2", "x-nh"}; !reflect.DeepEqual(ids, want) {
			t.Fatalf("rows = %v, want %v", ids, want)
		}
	}
}

func TestReadExportNeverShortensOnAFailure(t *testing.T) {
	refusal := &issueops.ErrUnsupported{Op: "Reader.List", Backend: "http"}
	if rows, err := issueops.ReadExport(context.Background(), &exportReader{err: refusal}, &exportEdges{}, issueops.ExportRequest{All: true}); !errors.Is(err, refusal) || rows != nil {
		t.Fatalf("ReadExport = (%v, %v), want (nil, the refusal)", rows, err)
	}
	if _, err := issueops.ReadExport(context.Background(), &exportReader{page: exportPage()}, &exportEdges{missing: "x-2"}, issueops.ExportRequest{All: true}); err == nil {
		t.Fatal("ReadExport with a vanished anchor succeeded, want an error")
	}
	if rows, err := issueops.ReadExport(context.Background(), &exportReader{page: exportPage(), hasMore: true}, &exportEdges{}, issueops.ExportRequest{All: true}); err == nil || rows != nil {
		t.Fatalf("ReadExport over a truncated listing = (%v, %v), want (nil, an error)", rows, err)
	}
	notFound := errors.New("x-1 not found")
	if rows, err := issueops.ReadExport(context.Background(), &exportReader{page: exportPage(), getErr: notFound}, &exportEdges{}, issueops.ExportRequest{All: true}); !errors.Is(err, notFound) || rows != nil {
		t.Fatalf("ReadExport with a failed comment read = (%v, %v), want (nil, the failure)", rows, err)
	}
	var unsupported *issueops.ErrUnsupported
	if _, err := issueops.ReadExport(context.Background(), nil, nil, issueops.ExportRequest{}); !errors.As(err, &unsupported) {
		t.Fatalf("ReadExport(nil) = %v, want ErrUnsupported", err)
	}
}
