package issueops

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestSnapshotCreateInputsRestoresWhatTheCreatePathWritesBack covers every
// write the create path makes into a caller's issues, including the two it
// makes through pointers the Issue struct only holds: the minted ID copied into
// each inline dependency that named no source, and the derived ID stamped onto
// each inline comment that had none. A struct copy restores neither.
func TestSnapshotCreateInputsRestoresWhatTheCreatePathWritesBack(t *testing.T) {
	presetEdge := &types.Dependency{IssueID: "test-explicit", DependsOnID: "test-other", Type: types.DepRelated}
	inlineEdge := &types.Dependency{DependsOnID: "test-anchor", Type: types.DepBlocks}
	inlineComment := &types.Comment{Author: "a", Text: "first"}
	importedComment := &types.Comment{ID: "c-imported", Author: "b", Text: "second"}
	issue := &types.Issue{
		Title:        "loser",
		Labels:       []string{"area:storage"},
		Dependencies: []*types.Dependency{inlineEdge, nil, presetEdge},
		Comments:     []*types.Comment{inlineComment, importedComment, nil},
	}
	second := &types.Issue{Title: "second"}
	restore := SnapshotCreateInputs([]*types.Issue{issue, nil, second})

	// What one create attempt leaves behind.
	issue.ID = "test-1"
	issue.CreatedAt = time.Now()
	issue.UpdatedAt = issue.CreatedAt
	issue.ContentHash = "attempt-1-hash"
	inlineEdge.IssueID = issue.ID
	inlineComment.ID = "c-attempt-1"
	second.ID = "test-2"

	restore()

	if issue.ID != "" || !issue.CreatedAt.IsZero() || !issue.UpdatedAt.IsZero() || issue.ContentHash != "" {
		t.Fatalf("issue after restore = id %q, created %v, updated %v, hash %q; want the caller's empty values",
			issue.ID, issue.CreatedAt, issue.UpdatedAt, issue.ContentHash)
	}
	if second.ID != "" {
		t.Fatalf("second issue id after restore = %q, want empty", second.ID)
	}
	if inlineEdge.IssueID != "" {
		t.Fatalf("inline edge source after restore = %q, want empty so the replay derives it from the new id", inlineEdge.IssueID)
	}
	if presetEdge.IssueID != "test-explicit" {
		t.Fatalf("explicit edge source after restore = %q, want the caller's %q", presetEdge.IssueID, "test-explicit")
	}
	if inlineComment.ID != "" {
		t.Fatalf("inline comment id after restore = %q, want empty so the replay derives it again", inlineComment.ID)
	}
	if importedComment.ID != "c-imported" {
		t.Fatalf("imported comment id after restore = %q, want the caller's %q", importedComment.ID, "c-imported")
	}
	if issue.Dependencies[0] != inlineEdge || issue.Comments[0] != inlineComment {
		t.Fatal("restore replaced the caller's dependency or comment objects instead of resetting them")
	}

	// Every replay starts from the same request.
	issue.ID = "test-3"
	inlineEdge.IssueID = issue.ID
	restore()
	if issue.ID != "" || inlineEdge.IssueID != "" {
		t.Fatalf("second restore left id %q and edge source %q", issue.ID, inlineEdge.IssueID)
	}
}
