package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// renameRefsFakeStore serves issues from a slice and records the UpdateIssue
// calls the reference scan makes. The DoltStorage embed only satisfies the
// parameter's type: the scan must not call anything else on the store, and on
// this fake anything else would panic through the nil embed.
type renameRefsFakeStore struct {
	storage.DoltStorage
	issues  []*types.Issue
	updates []renameRefsFakeUpdate
}

// renameRefsFakeUpdate is one UpdateIssue call the fake recorded.
type renameRefsFakeUpdate struct {
	id      string
	updates map[string]interface{}
}

func (s *renameRefsFakeStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return s.issues, nil
}

func (s *renameRefsFakeStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	s.updates = append(s.updates, renameRefsFakeUpdate{id: id, updates: updates})
	return nil
}

// update returns the value the scan wrote to field on id, or nil when it made
// no such call.
func (s *renameRefsFakeStore) update(id, field string) interface{} {
	for _, u := range s.updates {
		if u.id == id {
			return u.updates[field]
		}
	}
	return nil
}

func TestUpdateReferencesInAllIssues(t *testing.T) {
	t.Run("rewrites close_reason on another issue", func(t *testing.T) {
		// GH#7040: a closed issue's close_reason kept naming the old ID.
		s := &renameRefsFakeStore{
			issues: []*types.Issue{
				{ID: "bd-other", Title: "Other", Status: types.StatusClosed, CloseReason: "duplicate of bd-old"},
			},
		}
		if err := updateReferencesInAllIssues(context.Background(), s, "bd-old", "bd-new", "test"); err != nil {
			t.Fatalf("updateReferencesInAllIssues: %v", err)
		}
		if got := s.update("bd-other", "close_reason"); got != "duplicate of bd-new" {
			t.Errorf("close_reason update = %v, want %q", got, "duplicate of bd-new")
		}
	})
}
