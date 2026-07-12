package flatfile

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// SQL-reference oracle: issueops.checkCrossTableIDCollision — an incoming
// issue whose ID exists in the sibling bucket (issues vs wisps) is a hard
// error; with ConflictSkip it is a FULL skip (CreateIssueInTxWithResult
// returns before PersistLabels/PersistComments, so no aux data lands either).

func newCrossBucketStore(t *testing.T) *FlatFileStore {
	t.Helper()
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// Existing wisp occupying the ID.
	wisp := &types.Issue{ID: "test-x1", Title: "a wisp", Priority: 2, Ephemeral: true}
	if err := s.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	return s
}

func TestBatchCrossBucketCollisionErrors(t *testing.T) {
	s := newCrossBucketStore(t)
	durable := &types.Issue{ID: "test-x1", Title: "durable takeover", Priority: 2}
	err := s.CreateIssues(ctx, []*types.Issue{durable}, "tester")
	if err == nil {
		t.Fatal("durable issue over existing wisp ID succeeded; SQL errors on cross-table collision")
	}
	if !strings.Contains(err.Error(), "wisps") {
		t.Errorf("error = %v, want mention of the wisps table", err)
	}
	got, gerr := s.GetIssue(ctx, "test-x1")
	if gerr != nil {
		t.Fatalf("GetIssue: %v", gerr)
	}
	if !got.Ephemeral || got.Title != "a wisp" {
		t.Errorf("stored wisp mutated: %+v", got)
	}
}

func TestBatchCrossBucketReverseDirectionErrors(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := s.CreateIssue(ctx, &types.Issue{ID: "test-x2", Title: "durable", Priority: 2}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	wisp := &types.Issue{ID: "test-x2", Title: "wisp takeover", Priority: 2, Ephemeral: true}
	err := s.CreateIssues(ctx, []*types.Issue{wisp}, "tester")
	if err == nil {
		t.Fatal("wisp over existing durable ID succeeded; SQL errors on cross-table collision")
	}
	if !strings.Contains(err.Error(), "issues") {
		t.Errorf("error = %v, want mention of the issues table", err)
	}
}

// SQL-reference oracle for check ORDER: CreateIssueInTxWithResult runs
// checkCrossTableIDCollision BEFORE CheckOrphan, so a hierarchical ID whose
// parent is missing AND whose ID is held by the sibling bucket resolves as a
// cross-table collision, never as an orphan.

func newHierarchicalWispStore(t *testing.T) *FlatFileStore {
	t.Helper()
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// Wisp holding a hierarchical ID whose parent test-1 does not exist.
	wisp := &types.Issue{ID: "test-1.2", Title: "a wisp", Priority: 2, Ephemeral: true}
	if err := s.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	return s
}

func TestBatchCrossBucketCollisionBeatsOrphanSkip(t *testing.T) {
	s := newHierarchicalWispStore(t)
	durable := &types.Issue{ID: "test-1.2", Title: "durable orphan", Priority: 2}
	err := s.CreateIssuesWithFullOptions(ctx, []*types.Issue{durable}, "tester", storage.BatchCreateOptions{
		OrphanHandling: storage.OrphanSkip,
	})
	if err == nil {
		t.Fatal("orphan-skip swallowed a cross-bucket collision; SQL checks the collision first and aborts")
	}
	if !strings.Contains(err.Error(), "wisps") {
		t.Errorf("error = %v, want mention of the wisps table", err)
	}
}

func TestBatchCrossBucketConflictSkipBeatsOrphanStrict(t *testing.T) {
	s := newHierarchicalWispStore(t)
	durable := &types.Issue{ID: "test-1.2", Title: "durable orphan", Priority: 2}
	err := s.CreateIssuesWithFullOptions(ctx, []*types.Issue{durable}, "tester", storage.BatchCreateOptions{
		OrphanHandling: storage.OrphanStrict,
		ConflictSkip:   true,
	})
	if err != nil {
		t.Fatalf("orphan-strict fired before the cross-bucket check; SQL full-skips the collision first: %v", err)
	}
	got, gerr := s.GetIssue(ctx, "test-1.2")
	if gerr != nil {
		t.Fatalf("GetIssue: %v", gerr)
	}
	if !got.Ephemeral || got.Title != "a wisp" {
		t.Errorf("stored wisp mutated under ConflictSkip: %+v", got)
	}
}

func TestBatchCrossBucketConflictSkipIsFullSkip(t *testing.T) {
	s := newCrossBucketStore(t)
	durable := &types.Issue{
		ID:       "test-x1",
		Title:    "durable takeover",
		Priority: 2,
		Labels:   []string{"newlabel"},
		Comments: []*types.Comment{{Author: "alice", Text: "should not land"}},
	}
	err := s.CreateIssuesWithFullOptions(ctx, []*types.Issue{durable}, "tester", storage.BatchCreateOptions{
		OrphanHandling: storage.OrphanAllow,
		ConflictSkip:   true,
	})
	if err != nil {
		t.Fatalf("ConflictSkip batch: %v", err)
	}
	got, gerr := s.GetIssue(ctx, "test-x1")
	if gerr != nil {
		t.Fatalf("GetIssue: %v", gerr)
	}
	if !got.Ephemeral || got.Title != "a wisp" {
		t.Errorf("stored wisp mutated under ConflictSkip: %+v", got)
	}
	if len(got.Labels) != 0 {
		t.Errorf("labels merged on cross-bucket skip: %v; SQL persists none", got.Labels)
	}
	comments, cerr := s.GetIssueComments(ctx, "test-x1")
	if cerr != nil {
		t.Fatalf("GetIssueComments: %v", cerr)
	}
	if len(comments) != 0 {
		t.Errorf("comments imported on cross-bucket skip: %+v; SQL persists none", comments)
	}
}
