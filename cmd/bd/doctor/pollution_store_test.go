//go:build cgo

package doctor

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestPollutionDetectStoreExcludesEdgesAndComments runs the evidence seam
// against a real isolated store so mocks cannot conceal table-routing gaps:
// the incoming dependency for one candidate comes from a wisp-plane row
// (wisp dependency table) and another's from a permanent row, and a third
// candidate carries a comment. Only the isolated candidate survives.
func TestPollutionDetectStoreExcludesEdgesAndComments(t *testing.T) {
	store := newTestDoltStore(t, "pdet")
	ctx := context.Background()

	newCand := func(title string) *types.Issue {
		return &types.Issue{Title: title, Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}

	candOut := newCand("test candidate outgoing")
	candInPermanent := newCand("test candidate inbound permanent")
	candInWisp := newCand("test candidate inbound wisp")
	candCommented := newCand("test candidate commented")
	candIsolated := newCand("test candidate isolated")
	for _, issue := range []*types.Issue{candOut, candInPermanent, candInWisp, candCommented, candIsolated} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%q): %v", issue.Title, err)
		}
	}

	permanent := &types.Issue{Title: "Real work item", Description: "Carries a substantive description.", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, permanent, "test"); err != nil {
		t.Fatalf("CreateIssue(permanent): %v", err)
	}
	wisp := &types.Issue{Title: "wisp blocker note", Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, NoHistory: true}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("CreateIssue(wisp): %v", err)
	}

	// candOut has an outgoing edge; candInPermanent's inbound edge lives in
	// the permanent dependency table; candInWisp's inbound edge comes from
	// a wisp-plane row (wisp dependency table); candCommented has a comment.
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: candOut.ID, DependsOnID: permanent.ID, Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("AddDependency(outgoing): %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: permanent.ID, DependsOnID: candInPermanent.ID, Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("AddDependency(inbound permanent): %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: wisp.ID, DependsOnID: candInWisp.ID, Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("AddDependency(inbound wisp): %v", err)
	}
	// The structured comment path (the Commenter role, the seam
	// GetCommentCounts reads) — not the audit-event AddComment.
	commenter, err := store.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	if _, err := commenter.AddComment(ctx, publicops.AddCommentRequest{
		Author:  "test",
		IssueID: candCommented.ID,
		Text:    "a comment protects this row",
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}

	matches, err := DetectTestPollution(ctx, store, allIssues)
	if err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}
	if len(matches) != 1 || matches[0].Issue.ID != candIsolated.ID {
		var got []string
		for _, m := range matches {
			got = append(got, m.Issue.ID+" ("+m.Issue.Title+")")
		}
		t.Fatalf("DetectTestPollution matched %v, want [%s] (edges, wisp-plane inbound edges and comments must all exclude)", got, candIsolated.ID)
	}
}
