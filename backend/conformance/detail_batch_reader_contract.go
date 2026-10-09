package conformance

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Three legs share ExecuteDetailBatch: one reading, engine and wrapper checks.
// These cases pin DetailBatchReader's successful Reader.Get parity, ordered
// absence/repeats and 200-ID boundaries. Single-transaction scope is structural:
// each accessor opens once and the UOW repository delegates to the same DBTX body.
// Validation/failure SQL tests live beside the body; opener rejection tests live
// beside the accessors. Expectations below follow the approved per-route policy.
type DetailBatchReaderFixture struct {
	IssuePrefix       string
	DetailBatchReader publicops.DetailBatchReader
	Reader            publicops.Reader
	CreateIssue       func(context.Context, *types.Issue, string) error
	CreateWisp        func(context.Context, *types.Issue, string) error
	// Exec is out-of-band fixture seeding, as in CycleDetectorFixture. It allows
	// edge IDs, dangling targets and dual residency that public writes cannot seed.
	Exec func(context.Context, []SQLStatement) error
	// DirectServer chooses one outgoing plane by wisp existence. UOW chooses
	// labels/comments by the durable-first subject; store legs use wisp existence.
	DirectServer bool
	UOW          bool
}

// RunDetailBatchReaderParity pins DetailBatchReader and DetailBatchResult
// against Reader.Get for every found subject and all three-option combinations.
func RunDetailBatchReaderParity(t *testing.T, ctx context.Context, f DetailBatchReaderFixture) {
	t.Helper()
	id := func(s string) string { return f.IssuePrefix + "-par-" + s }
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	tags := []string{"a", "b", "parent-z", "parent-a", "dep-z", "dep-a", "wisp", "epic", "closed"}
	ids := make([]string, 0, len(tags))
	for _, tag := range tags {
		issue := &types.Issue{ID: id(tag), Title: "durable " + tag, Description: "detail text " + tag, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: base, UpdatedAt: base}
		if tag == "epic" {
			issue.IssueType = types.TypeEpic
		}
		if tag == "closed" {
			issue.Status = types.StatusClosed
			issue.ClosedAt = &base
			// Shallow dependents keep close_reason; a non-completing one also
			// exercises the epic-closeable classifier on every route.
			issue.CloseReason = "duplicate of parent-a"
		}
		issue.Labels = []string{"zeta", "alpha", "middle"}
		var err error
		if tag == "wisp" {
			issue.Ephemeral = true
			issue.CreatedAt = base.Add(-time.Hour)
			err = f.CreateWisp(ctx, issue, "seed")
		} else {
			err = f.CreateIssue(ctx, issue, "seed")
		}
		if err != nil {
			t.Fatalf("seed %s: %v", tag, err)
		}
		ids = append(ids, issue.ID)
	}
	statements := []SQLStatement{
		{Query: "SET foreign_key_checks=0"},
		{Query: "INSERT INTO wisps (id,title,status,priority,issue_type,created_at,updated_at,ephemeral) VALUES (?, 'wisp a', 'open', 2, 'task', ?, ?, 1)", Args: []any{id("a"), base, base}},
	}
	edge := func(table, key, source, target string, typ types.DependencyType) {
		statements = append(statements, SQLStatement{Query: "INSERT INTO " + table + " (id,issue_id,depends_on_issue_id,type,created_at,created_by) VALUES (?,?,?,?,?,?)", Args: []any{id(key), id(source), id(target), string(typ), base, "seed"}})
	}
	edge("dependencies", "01", "a", "parent-z", types.DepParentChild)
	edge("dependencies", "02", "a", "parent-a", types.DepParentChild)
	edge("dependencies", "03", "a", "b", types.DepRelated)
	edge("dependencies", "04", "a", "missing", types.DepBlocks)
	edge("wisp_dependencies", "01w", "a", "b", types.DepRelated)
	edge("wisp_dependencies", "02w", "a", "parent-a", types.DepParentChild)
	// Edge-id order (08, 09) disagrees with source-id order (dep-a, dep-z), so
	// the order check fails if a leg breaks the equal-created_at tie by the wrong key.
	edge("dependencies", "09", "dep-a", "a", types.DepBlocks)
	edge("dependencies", "08", "dep-z", "a", types.DepRelated)
	edge("wisp_dependencies", "03w", "wisp", "a", types.DepBlocks)
	edge("wisp_dependencies", "04w", "wisp", "b", types.DepRelated)
	edge("dependencies", "05", "b", "epic", types.DepParentChild)
	edge("dependencies", "06", "closed", "epic", types.DepParentChild)
	for _, label := range []string{"wisp-alpha", "wisp-middle", "wisp-zeta"} {
		statements = append(statements, SQLStatement{Query: "INSERT INTO wisp_labels (issue_id,label) VALUES (?,?)", Args: []any{id("a"), label}})
	}
	for i, table := range []string{"comments", "wisp_comments", "wisp_comments"} {
		statements = append(statements, SQLStatement{Query: "INSERT INTO " + table + " (id,issue_id,author,text,created_at) VALUES (?,?,?,?,?)", Args: []any{id(fmt.Sprintf("comment-%d", i)), id("a"), "seed", table, base.Add(time.Duration(i) * time.Second)}})
	}
	statements = append(statements, SQLStatement{Query: "SET foreign_key_checks=1"})
	if err := f.Exec(ctx, statements); err != nil {
		t.Fatal(err)
	}
	for options := 0; options < 8; options++ {
		t.Run(fmt.Sprintf("options-%d", options), func(t *testing.T) {
			req := publicops.DetailBatchRequest{IDs: ids, IncludeDependents: options&1 != 0, IncludeComments: options&2 != 0, BriefDeps: options&4 != 0}
			result, err := f.DetailBatchReader.GetBatch(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != len(ids) {
				t.Fatalf("items=%d, want %d", len(result.Items), len(ids))
			}
			for i, item := range result.Items {
				if item.ID != ids[i] || !item.Found || item.Issue == nil {
					t.Fatalf("item %d: %+v", i, item)
				}
				perID, err := f.Reader.Get(ctx, publicops.GetRequest{ID: item.ID, IncludeDependents: req.IncludeDependents, IncludeComments: req.IncludeComments, BriefDeps: req.BriefDeps})
				if err != nil {
					t.Fatal(err)
				}
				if got, want := detailBatchOrderFree(item.Issue), detailBatchOrderFree(perID); !reflect.DeepEqual(got, want) {
					gotJSON, _ := json.Marshal(got)
					wantJSON, _ := json.Marshal(want)
					t.Fatalf("Reader.Get parity ID=%s direct=%v uow=%v options=%d\nbatch=%s nested labels %v\nper-ID=%s nested labels %v", item.ID, f.DirectServer, f.UOW, options, gotJSON, got.Issue.Labels, wantJSON, want.Issue.Labels)
				}
			}
			a := result.Items[0].Issue
			if a.Title != "durable a" {
				t.Errorf("dual subject title=%q, want durable a", a.Title)
			}
			if *a.DependencyCount != 6 || *a.DependentCount != 3 {
				t.Errorf("dual counts out=%d in=%d, want 6 and 3", *a.DependencyCount, *a.DependentCount)
			}
			wantOut := 5
			parent := id("parent-z")
			if f.DirectServer {
				wantOut = 2
				parent = id("parent-a")
			}
			if len(a.Dependencies) != wantOut || a.Parent == nil || *a.Parent != parent {
				t.Errorf("outgoing=%d parent=%v, want %d %s", len(a.Dependencies), a.Parent, wantOut, parent)
			}
			// Parity above ignores row order, so pin the batch's own fixed order.
			wantOutOrder := []string{id("parent-z"), id("parent-a"), id("b"), id("b"), id("parent-a")}
			wantInOrder := []string{id("dep-z"), id("dep-a"), id("wisp")}
			if f.DirectServer {
				wantOutOrder = []string{id("b"), id("parent-a")}
				wantInOrder = []string{id("wisp"), id("dep-a"), id("dep-z")}
			}
			if got := detailBatchRowIDs(a.Dependencies); !slices.Equal(got, wantOutOrder) {
				t.Errorf("outgoing order=%v, want %v", got, wantOutOrder)
			}
			if got := detailBatchRowIDs(a.Dependents); req.IncludeDependents && !slices.Equal(got, wantInOrder) {
				t.Errorf("dependent order=%v, want %v", got, wantInOrder)
			}
			bRows := 0
			for _, dep := range a.Dependencies {
				if dep.ID == id("b") {
					bRows++
				}
			}
			wantB := 2
			if f.DirectServer {
				wantB = 1
			}
			if bRows != wantB {
				t.Errorf("dual-table outgoing b rows=%d, want %d", bRows, wantB)
			}
			labels := []string{"wisp-alpha", "wisp-middle", "wisp-zeta"}
			comments := int64(2)
			commentText := "wisp_comments"
			if f.UOW {
				labels = []string{"alpha", "middle", "zeta"}
				comments = 1
				commentText = "comments"
			}
			if !reflect.DeepEqual(a.Labels, labels) || *a.CommentCount != comments {
				t.Errorf("dual auxiliary plane labels=%v comments=%d, want %v %d", a.Labels, *a.CommentCount, labels, comments)
			}
			if req.IncludeComments {
				if int64(len(a.Comments)) != comments {
					t.Errorf("comment rows=%d, want %d", len(a.Comments), comments)
				}
				for _, c := range a.Comments {
					if c.Text != commentText {
						t.Errorf("comment plane text=%s, want %s", c.Text, commentText)
					}
				}
			}
			b := result.Items[1].Issue
			if *b.DependentCount != 3 {
				t.Errorf("b incoming count=%d, want 3", *b.DependentCount)
			}
			if req.IncludeDependents {
				if len(a.Dependents) != 3 || len(b.Dependents) != 3 {
					t.Errorf("incoming rows a=%d b=%d, want 3 each", len(a.Dependents), len(b.Dependents))
				}
				aRows := 0
				for _, dep := range b.Dependents {
					if dep.ID == id("a") {
						aRows++
					}
				}
				if aRows != 2 {
					t.Errorf("dual-table incoming a rows=%d, want 2", aRows)
				}
				epic := result.Items[7].Issue
				if epic.EpicTotalChildren == nil || *epic.EpicTotalChildren != 2 || epic.EpicClosedChildren == nil || *epic.EpicClosedChildren != 1 {
					t.Errorf("epic progress=%+v, want 2 children and 1 closed", epic)
				}
				closeReason := ""
				for _, dep := range epic.Dependents {
					if dep.ID == id("closed") {
						closeReason = dep.CloseReason
					}
				}
				if closeReason != "duplicate of parent-a" {
					t.Errorf("closed child close_reason=%q, want the stored reason", closeReason)
				}
			}
		})
	}
	result, err := f.DetailBatchReader.GetBatch(ctx, publicops.DetailBatchRequest{IDs: []string{id("b"), id("missing"), id("a"), id("b")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 4 || result.Items[0].ID != id("b") || result.Items[1].ID != id("missing") || result.Items[1].Found || result.Items[1].Issue != nil || result.Items[2].ID != id("a") || !reflect.DeepEqual(result.Items[0], result.Items[3]) {
		t.Fatalf("ordered repeat/missing items=%+v", result.Items)
	}
}

// detailBatchOrderFree copies d with its relation rows sorted and Parent
// cleared. Reader.Get leaves most relation orders to the database, and Parent
// is the first parent-child row in that order, so parity compares rows as a
// multiset and the parity case pins the batch's own order and parent.
func detailBatchOrderFree(d *publicops.IssueDetails) publicops.IssueDetails {
	c := *d
	c.Parent = nil
	for _, rows := range []*[]*types.IssueWithDependencyMetadata{&c.Dependencies, &c.Dependents} {
		*rows = slices.Clone(*rows)
		slices.SortStableFunc(*rows, func(x, y *types.IssueWithDependencyMetadata) int {
			return cmp.Or(strings.Compare(x.ID, y.ID), strings.Compare(string(x.DependencyType), string(y.DependencyType)))
		})
	}
	return c
}

func detailBatchRowIDs(rows []*types.IssueWithDependencyMetadata) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// RunDetailBatchReaderChunkBoundaries pins the unlimited ordered request at
// 199, 200 and 201 distinct subjects, plus a repeat after the chunk boundary.
func RunDetailBatchReaderChunkBoundaries(t *testing.T, ctx context.Context, f DetailBatchReaderFixture) {
	t.Helper()
	ids := make([]string, 201)
	statements := make([]SQLStatement, 0, len(ids))
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-chunk-%03d", f.IssuePrefix, i)
		statements = append(statements, SQLStatement{Query: "INSERT INTO issues (id,title,description,design,acceptance_criteria,notes,status,priority,issue_type,created_at,updated_at) VALUES (?,?,'','','','','open',2,'task',?,?)", Args: []any{ids[i], ids[i], time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)}})
	}
	if err := f.Exec(ctx, statements); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{199, 200, 201} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			result, err := f.DetailBatchReader.GetBatch(ctx, publicops.DetailBatchRequest{IDs: ids[:n]})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Items) != n {
				t.Fatalf("items=%d, want %d", len(result.Items), n)
			}
			for i, item := range result.Items {
				if !item.Found || item.ID != ids[i] || item.Issue == nil || item.Issue.Title != ids[i] {
					t.Fatalf("item %d=%+v", i, item)
				}
			}
		})
	}
	repeated := append(append([]string(nil), ids...), ids[0])
	result, err := f.DetailBatchReader.GetBatch(ctx, publicops.DetailBatchRequest{IDs: repeated})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 202 || !reflect.DeepEqual(result.Items[0], result.Items[201]) {
		t.Fatal("repeat across chunk boundary lost")
	}
}
