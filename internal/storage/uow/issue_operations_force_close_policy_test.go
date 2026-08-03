package uow

import (
	"testing"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestUpdateSpecCarriesForceClosePolicy pins the unit-of-work backend's
// translation of the typed override into the update map. This backend reaches
// storage by a path the other two do not, and an earlier attempt was reverted
// for exactly this: the request carried the override, the spec did not, and the
// caller's force was lost silently.
func TestUpdateSpecCarriesForceClosePolicy(t *testing.T) {
	t.Parallel()

	statusPatch := publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusClosed}}
	for _, tc := range []struct {
		name    string
		request publicops.UpdateRequest
		want    bool
		present bool
	}{
		{
			name:    "forced status change",
			request: publicops.UpdateRequest{Actor: "a", IssueID: "bd-1", ForceClosePolicy: true, Patch: statusPatch},
			want:    true,
			present: true,
		},
		{
			name:    "unforced status change",
			request: publicops.UpdateRequest{Actor: "a", IssueID: "bd-1", Patch: statusPatch},
			present: false,
		},
		{
			name: "forced without a status change",
			request: publicops.UpdateRequest{Actor: "a", IssueID: "bd-1", ForceClosePolicy: true, Patch: publicops.IssuePatch{
				Priority: publicops.Field[int]{Set: true, Value: 1},
			}},
			present: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, err := updateSpec(tc.request)
			if err != nil {
				t.Fatalf("updateSpec: %v", err)
			}
			got, present := spec.Fields[storageissueops.OpForceClosePolicy]
			if present != tc.present {
				t.Fatalf("spec.Fields[%q] present = %v, want %v", storageissueops.OpForceClosePolicy, present, tc.present)
			}
			if present && got != tc.want {
				t.Errorf("spec.Fields[%q] = %v, want %v", storageissueops.OpForceClosePolicy, got, tc.want)
			}
		})
	}
}
