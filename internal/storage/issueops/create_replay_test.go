package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestSnapshotUnsetCreateIDs pins the helper the uow importer's retry reset
// leans on (GH#8455): it captures exactly the create-time id fields the
// caller left empty — Issue.ID, Dependency.IssueID, Comment.ID — and its
// reset clears those fields again, without ever touching caller-supplied
// values or panicking on nil elements.
func TestSnapshotUnsetCreateIDs(t *testing.T) {
	t.Run("MixOfSetAndUnsetFields", func(t *testing.T) {
		kept := &types.Issue{
			ID:           "kept",
			Dependencies: []*types.Dependency{{IssueID: "kept", DependsOnID: "other"}},
			Comments:     []*types.Comment{{ID: "kept-c1"}},
		}
		fresh := &types.Issue{
			Dependencies: []*types.Dependency{{DependsOnID: "other"}},
			Comments:     []*types.Comment{{}},
		}
		reset := SnapshotUnsetCreateIDs([]*types.Issue{kept, fresh})

		// The engine's mints: every empty field is written in place.
		fresh.ID = "minted"
		fresh.Dependencies[0].IssueID = "minted"
		fresh.Comments[0].ID = "minted-c1"
		// A set field rewritten between snapshot and reset (the shape a
		// second mint attempt would leave): reset must leave it alone,
		// because it captures FIELDS, not values.
		kept.ID = "kept-rewritten"

		reset()

		if fresh.ID != "" || fresh.Dependencies[0].IssueID != "" || fresh.Comments[0].ID != "" {
			t.Errorf("reset left minted fields set: id=%q dep.IssueID=%q comment.ID=%q",
				fresh.ID, fresh.Dependencies[0].IssueID, fresh.Comments[0].ID)
		}
		if kept.ID != "kept-rewritten" || kept.Dependencies[0].IssueID != "kept" || kept.Comments[0].ID != "kept-c1" {
			t.Errorf("reset touched caller-supplied values: id=%q dep.IssueID=%q comment.ID=%q",
				kept.ID, kept.Dependencies[0].IssueID, kept.Comments[0].ID)
		}
	})

	t.Run("ResetIsIdempotent", func(t *testing.T) {
		fresh := &types.Issue{Comments: []*types.Comment{{}}}
		reset := SnapshotUnsetCreateIDs([]*types.Issue{fresh})
		fresh.ID = "minted"
		fresh.Comments[0].ID = "minted-c1"
		reset()
		reset()
		if fresh.ID != "" || fresh.Comments[0].ID != "" {
			t.Errorf("reset twice left minted fields set: id=%q comment.ID=%q", fresh.ID, fresh.Comments[0].ID)
		}
	})

	t.Run("NilIssuesDepsAndCommentsAreSkipped", func(t *testing.T) {
		fresh := &types.Issue{
			Dependencies: []*types.Dependency{nil, {DependsOnID: "other"}},
			Comments:     []*types.Comment{nil, {}},
		}
		reset := SnapshotUnsetCreateIDs([]*types.Issue{nil, fresh, nil})
		fresh.ID = "minted"
		fresh.Dependencies[1].IssueID = "minted"
		fresh.Comments[1].ID = "minted-c1"

		reset()

		if fresh.ID != "" || fresh.Dependencies[1].IssueID != "" || fresh.Comments[1].ID != "" {
			t.Errorf("reset left minted fields set around nil siblings: id=%q dep.IssueID=%q comment.ID=%q",
				fresh.ID, fresh.Dependencies[1].IssueID, fresh.Comments[1].ID)
		}
	})

	t.Run("SnapshotAfterMintingCapturesNothing", func(t *testing.T) {
		// The caller that snapshots INSIDE the attempt, after the engine has
		// minted, captures nothing: every field reads "set", so reset is a
		// no-op. That is the misuse the doc comment's call-once rule exists
		// for, and this case pins why the importer snapshots before the loop.
		minted := &types.Issue{
			ID:           "minted",
			Dependencies: []*types.Dependency{{IssueID: "minted", DependsOnID: "other"}},
			Comments:     []*types.Comment{{ID: "minted-c1"}},
		}
		reset := SnapshotUnsetCreateIDs([]*types.Issue{minted})

		reset()

		if minted.ID != "minted" || minted.Dependencies[0].IssueID != "minted" || minted.Comments[0].ID != "minted-c1" {
			t.Errorf("reset cleared fields a late snapshot never saw empty: id=%q dep.IssueID=%q comment.ID=%q",
				minted.ID, minted.Dependencies[0].IssueID, minted.Comments[0].ID)
		}
	})
}
