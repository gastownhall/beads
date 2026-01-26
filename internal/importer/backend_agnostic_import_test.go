//go:build !integration
// +build !integration

package importer

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

func TestImportIssues_BackendAgnostic_DepsLabelsCommentsTombstone(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	commentTS := time.Date(2020, 1, 2, 3, 4, 5, 6, time.UTC)
	deletedTS := time.Date(2021, 2, 3, 4, 5, 6, 7, time.UTC)

	issueA := &types.Issue{
		ID:        "test-1",
		Title:     "Issue A",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
		Labels:    []string{"urgent"},
		Dependencies: []*types.Dependency{
			{IssueID: "test-1", DependsOnID: "test-2", Type: types.DepBlocks},
		},
		Comments: []*types.Comment{
			{Author: "tester", Text: "hello", CreatedAt: commentTS},
		},
	}
	issueB := &types.Issue{
		ID:           "test-2",
		Title:        "Issue B",
		IssueType:    types.TypeTask,
		Status:       types.StatusTombstone,
		Priority:     4,
		DeletedAt:    &deletedTS,
		DeletedBy:    "tester",
		DeleteReason: "bye",
		OriginalType: string(types.TypeTask),
		Description:  "tombstone",
		ContentHash:  "",
		Dependencies: nil,
		Labels:       nil,
		Comments:     nil,
		Assignee:     "",
		Owner:        "",
		CreatedBy:    "",
		SourceSystem: "",
		ExternalRef:  nil,
		ClosedAt:     nil,
		CompactedAt:  nil,
		DeferUntil:   nil,
		LastActivity: nil,
		QualityScore: nil,
		Validations:  nil,
		BondedFrom:   nil,
		Waiters:      nil,
	}

	res, err := ImportIssues(ctx, "", store, []*types.Issue{issueA, issueB}, Options{OrphanHandling: OrphanAllow})
	if err != nil {
		t.Fatalf("ImportIssues: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("expected Created=2, got %d", res.Created)
	}

	labels, err := store.GetLabels(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if len(labels) != 1 || labels[0] != "urgent" {
		t.Fatalf("expected labels [urgent], got %v", labels)
	}

	deps, err := store.GetDependencyRecords(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != "test-2" || deps[0].Type != types.DepBlocks {
		t.Fatalf("expected dependency test-1 blocks test-2, got %#v", deps)
	}

	comments, err := store.GetIssueComments(ctx, "test-1")
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if !comments[0].CreatedAt.Equal(commentTS) {
		t.Fatalf("expected comment timestamp preserved (%s), got %s", commentTS.Format(time.RFC3339Nano), comments[0].CreatedAt.Format(time.RFC3339Nano))
	}

	b, err := store.GetIssue(ctx, "test-2")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if b.Status != types.StatusTombstone {
		t.Fatalf("expected tombstone status, got %q", b.Status)
	}
	if b.DeletedAt == nil || !b.DeletedAt.Equal(deletedTS) {
		t.Fatalf("expected DeletedAt preserved (%s), got %#v", deletedTS.Format(time.RFC3339Nano), b.DeletedAt)
	}
}
