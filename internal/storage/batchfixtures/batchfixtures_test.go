package batchfixtures_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/issueops"
)

// TestShapeItemCounts pins the exact item count and per-kind composition of
// each fixture shape, so a change to batchfixtures itself (not the storage
// layer under test) is caught here rather than surfacing as a confusing
// statement-count drift in the backend-level pinning tests.
func TestShapeItemCounts(t *testing.T) {
	const rootID = "root-1"

	tests := []struct {
		name       string
		req        issueops.ApplyBatchRequest
		wantTotal  int
		wantCreate int
		wantDepAdd int
		wantUpdate int
		wantClose  int
	}{
		{
			name:       "356 (mol 1x)",
			req:        batchfixtures.Shape356("tester", rootID),
			wantTotal:  356,
			wantCreate: 102,
			wantDepAdd: 238,
			wantUpdate: 16,
		},
		{
			name:       "712 (mol 2x)",
			req:        batchfixtures.Shape712("tester", rootID),
			wantTotal:  712,
			wantCreate: 204,
			wantDepAdd: 476,
			wantUpdate: 32,
		},
		{
			name:       "40 (classic)",
			req:        batchfixtures.ShapeClassic40("tester"),
			wantTotal:  40,
			wantCreate: 10,
			wantDepAdd: 20,
			wantUpdate: 10,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(tc.req.Items); got != tc.wantTotal {
				t.Fatalf("len(Items) = %d, want %d", got, tc.wantTotal)
			}
			var creates, depAdds, updates, closes int
			for _, item := range tc.req.Items {
				switch item.Kind {
				case issueops.ItemCreate:
					creates++
				case issueops.ItemDepAdd:
					depAdds++
				case issueops.ItemUpdate:
					updates++
				case issueops.ItemClose:
					closes++
				}
			}
			if creates != tc.wantCreate {
				t.Errorf("creates = %d, want %d", creates, tc.wantCreate)
			}
			if depAdds != tc.wantDepAdd {
				t.Errorf("dep_adds = %d, want %d", depAdds, tc.wantDepAdd)
			}
			if updates != tc.wantUpdate {
				t.Errorf("updates = %d, want %d", updates, tc.wantUpdate)
			}
			if closes != tc.wantClose {
				t.Errorf("closes = %d, want %d", closes, tc.wantClose)
			}

			// Every shape must validate and plan cleanly against the role's own
			// request-shape contract (ref ordering, key uniqueness,
			// exactly-one-payload, the issueops.MaxApplyBatchItems cap) — a
			// fixture that failed it would be measuring nothing, and one over
			// the cap would be measuring a request no caller can send.
			if _, err := storage.PlanApplyBatch(tc.req); err != nil {
				t.Fatalf("PlanApplyBatch: %v", err)
			}
		})
	}
}

// TestShapeClassic40NoRootRequired documents that the classic shape, unlike
// the mol shapes, needs no pre-existing root issue.
func TestShapeClassic40NoRootRequired(t *testing.T) {
	req := batchfixtures.ShapeClassic40("tester")
	if _, err := storage.PlanApplyBatch(req); err != nil {
		t.Fatalf("PlanApplyBatch: %v", err)
	}
}
