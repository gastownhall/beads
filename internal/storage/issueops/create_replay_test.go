package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestSnapshotUnsetCreateIDs pins the replay reset the retrying create
// callers share (bd-8qq0ho, GH#8455): reset clears only the fields that were
// empty at snapshot time, whatever an attempt wrote into them since.
func TestSnapshotUnsetCreateIDs(t *testing.T) {
	t.Run("nil entries are skipped", func(t *testing.T) {
		issue := &types.Issue{
			Dependencies: []*types.Dependency{nil, {DependsOnID: "x-1"}},
			Comments:     []*types.Comment{nil, {Text: "c"}},
		}
		reset := SnapshotUnsetCreateIDs([]*types.Issue{nil, issue})
		issue.ID, issue.Dependencies[1].IssueID, issue.Comments[1].ID = "x-2", "x-2", "c-1"
		reset()
		if issue.ID != "" || issue.Dependencies[1].IssueID != "" || issue.Comments[1].ID != "" {
			t.Fatalf("reset left minted values: id=%q dep=%q comment=%q",
				issue.ID, issue.Dependencies[1].IssueID, issue.Comments[1].ID)
		}
		SnapshotUnsetCreateIDs(nil)() // an empty batch resets nothing
	})

	t.Run("only originally empty fields are reset", func(t *testing.T) {
		minted := &types.Issue{
			Dependencies: []*types.Dependency{{DependsOnID: "x-anchor"}},
			Comments:     []*types.Comment{{Text: "fresh"}},
		}
		explicit := &types.Issue{
			ID:           "x-explicit",
			Dependencies: []*types.Dependency{{IssueID: "x-explicit", DependsOnID: "x-anchor"}},
			Comments:     []*types.Comment{{ID: "c-explicit", Text: "carried"}},
		}
		reset := SnapshotUnsetCreateIDs([]*types.Issue{minted, explicit})

		// An attempt mints into the empty fields and (hypothetically) rewrites
		// the explicit ones; reset must restore only the former.
		minted.ID, minted.Dependencies[0].IssueID, minted.Comments[0].ID = "x-7", "x-7", "c-7"
		explicit.ID, explicit.Dependencies[0].IssueID, explicit.Comments[0].ID = "x-changed", "x-changed", "c-changed"
		reset()

		if minted.ID != "" || minted.Dependencies[0].IssueID != "" || minted.Comments[0].ID != "" {
			t.Errorf("unset fields not reset: id=%q dep=%q comment=%q",
				minted.ID, minted.Dependencies[0].IssueID, minted.Comments[0].ID)
		}
		if explicit.ID != "x-changed" || explicit.Dependencies[0].IssueID != "x-changed" || explicit.Comments[0].ID != "c-changed" {
			t.Errorf("caller-set fields were touched: id=%q dep=%q comment=%q",
				explicit.ID, explicit.Dependencies[0].IssueID, explicit.Comments[0].ID)
		}

		// Idempotent: a second reset (another replay) is the same no-op on set
		// fields and the same clear on the snapshot's empty ones.
		minted.ID = "x-8"
		reset()
		reset()
		if minted.ID != "" {
			t.Errorf("second reset left id %q", minted.ID)
		}
	})

	t.Run("a snapshot taken after minting captures nothing", func(t *testing.T) {
		issue := &types.Issue{
			ID:           "x-9",
			Dependencies: []*types.Dependency{{IssueID: "x-9", DependsOnID: "x-anchor"}},
			Comments:     []*types.Comment{{ID: "c-9", Text: "minted"}},
		}
		SnapshotUnsetCreateIDs([]*types.Issue{issue})()
		if issue.ID != "x-9" || issue.Dependencies[0].IssueID != "x-9" || issue.Comments[0].ID != "c-9" {
			t.Errorf("late snapshot reset minted fields: id=%q dep=%q comment=%q",
				issue.ID, issue.Dependencies[0].IssueID, issue.Comments[0].ID)
		}
	})
}
