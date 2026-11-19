package issueops

import (
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// BatchSnapshots holds, for each row an apply-batch request wrote, the
// snapshot its LATEST create, update, close or metadata splice hydrated
// (bd-xx3ma9). Recording a row replaces its previous snapshot, so a request
// that writes one row a hundred times holds one snapshot of it at any point,
// not one per item.
//
// The batch runs keep ItemResult.Issue unset while items apply and call
// Attach once, after the end gate admitted the request, so every row entry
// shares its row's final snapshot.
type BatchSnapshots map[string]*types.Issue

// Record makes issue the latest snapshot of its row.
func (s BatchSnapshots) Record(issue *types.Issue) {
	if issue != nil {
		s[issue.ID] = issue
	}
}

// Attach hands every create, update and close result its row's latest
// snapshot. A dep_add result carries none.
func (s BatchSnapshots) Attach(items []publicops.ItemResult) {
	for i := range items {
		switch items[i].Kind {
		case publicops.ItemCreate, publicops.ItemUpdate, publicops.ItemClose:
			items[i].Issue = s[items[i].IssueID]
		}
	}
}
