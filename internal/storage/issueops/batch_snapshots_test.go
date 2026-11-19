package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Recording a row again replaces its snapshot, so the accumulator holds one
// snapshot per row however many items wrote it, and Attach shares that one
// snapshot across the row's create, update and close entries only.
func TestBatchSnapshotsKeepOneSnapshotPerRow(t *testing.T) {
	snapshots := BatchSnapshots{}
	var last *types.Issue
	for i := 0; i < 40; i++ {
		last = &types.Issue{ID: "bd-row", RowVersion: int64(i)}
		snapshots.Record(last)
		if len(snapshots) != 1 || snapshots["bd-row"] != last {
			t.Fatalf("after %d records the accumulator holds %d snapshots (latest %v), want only the newest", i+1, len(snapshots), snapshots["bd-row"] == last)
		}
	}
	other := &types.Issue{ID: "bd-other"}
	snapshots.Record(other)
	snapshots.Record(nil)
	if len(snapshots) != 2 {
		t.Fatalf("accumulator holds %d snapshots, want 2 (nil is not recorded)", len(snapshots))
	}

	items := []publicops.ItemResult{
		{Kind: publicops.ItemCreate, IssueID: "bd-row", RowVersion: 0},
		{Kind: publicops.ItemUpdate, IssueID: "bd-row", RowVersion: 7},
		{Kind: publicops.ItemClose, IssueID: "bd-row", RowVersion: 39},
		{Kind: publicops.ItemUpdate, IssueID: "bd-other"},
		{Kind: publicops.ItemDepAdd, IssueID: "bd-other", DependsOnID: "bd-row"},
	}
	snapshots.Attach(items)
	for i, want := range []*types.Issue{last, last, last, other, nil} {
		if items[i].Issue != want {
			t.Errorf("item %d (%s) Issue = %p, want %p", i, items[i].Kind, items[i].Issue, want)
		}
	}
	if items[1].RowVersion != 7 {
		t.Errorf("Attach rewrote item 1's RowVersion to %d; each item keeps its own token", items[1].RowVersion)
	}
}
