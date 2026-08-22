package issueops

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestAuthorizeNotesOverwrite pins every branch of the notes-overwrite fence.
func TestAuthorizeNotesOverwrite(t *testing.T) {
	withNotes := func(notes string) *types.Issue {
		return &types.Issue{ID: "bd-1", Notes: notes}
	}
	notesPatch := func(mutate func(*publicops.UpdateRequest)) publicops.UpdateRequest {
		request := publicops.UpdateRequest{
			Actor:   "actor",
			IssueID: "bd-1",
			Patch:   publicops.IssuePatch{Notes: publicops.Field[string]{Set: true, Value: "replacement"}},
		}
		if mutate != nil {
			mutate(&request)
		}
		return request
	}
	holder := "someone"

	cases := []struct {
		name    string
		before  *types.Issue
		request publicops.UpdateRequest
		refuse  bool
	}{
		{
			name:    "unset notes patch",
			before:  withNotes("original"),
			request: notesPatch(func(r *publicops.UpdateRequest) { r.Patch.Notes = publicops.Field[string]{} }),
		},
		{
			name:    "existing notes empty",
			before:  withNotes(""),
			request: notesPatch(nil),
		},
		{
			name:    "new value matches existing",
			before:  withNotes("original"),
			request: notesPatch(func(r *publicops.UpdateRequest) { r.Patch.Notes.Value = "original" }),
		},
		{
			name:    "forced overwrite",
			before:  withNotes("original"),
			request: notesPatch(func(r *publicops.UpdateRequest) { r.ForceNotesOverwrite = true }),
		},
		{
			// Deliberately NOT exempted by ExpectedAssignee, unlike the
			// assignee fence: there is no compare-and-set that authorizes a
			// notes overwrite, so pairing the two guards does not stand this
			// one down.
			name:    "expected assignee set does not exempt it",
			before:  withNotes("original"),
			request: notesPatch(func(r *publicops.UpdateRequest) { r.ExpectedAssignee = &holder }),
			refuse:  true,
		},
		{
			name:    "unforced overwrite refuses",
			before:  withNotes("original"),
			request: notesPatch(nil),
			refuse:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AuthorizeNotesOverwrite(tc.before, tc.request)
			if !tc.refuse {
				if err != nil {
					t.Fatalf("AuthorizeNotesOverwrite = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, storage.ErrNotesOverwrite) {
				t.Fatalf("AuthorizeNotesOverwrite = %v, want ErrNotesOverwrite", err)
			}
			if !strings.Contains(err.Error(), tc.before.ID) {
				t.Errorf("refusal %q omits issue %s", err, tc.before.ID)
			}
		})
	}
}
