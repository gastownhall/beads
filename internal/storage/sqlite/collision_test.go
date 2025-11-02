package sqlite

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestDetectCollisions(t *testing.T) {
	store := newTestStore(t, "file::memory:?mode=memory&cache=private")
	ctx := context.Background()

	// Create existing issue
	existing := &types.Issue{
		ID:          "bd-1",
		Title:       "Existing Issue",
		Description: "Original description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, existing, "test"); err != nil {
		t.Fatalf("Failed to create existing issue: %v", err)
	}

	tests := []struct {
		name              string
		incoming          []*types.Issue
		wantExactMatches  int
		wantCollisions    int
		wantNewIssues     int
		checkCollisionID  string
		expectedConflicts []string
	}{
		{
			name: "exact match - idempotent",
			incoming: []*types.Issue{
				{
					ID:          "bd-1",
					Title:       "Existing Issue",
					Description: "Original description",
					Status:      types.StatusOpen,
					Priority:    1,
					IssueType:   types.TypeTask,
				},
			},
			wantExactMatches: 1,
			wantCollisions:   0,
			wantNewIssues:    0,
		},
		{
			name: "collision - different title",
			incoming: []*types.Issue{
				{
					ID:          "bd-1",
					Title:       "Modified Title",
					Description: "Original description",
					Status:      types.StatusOpen,
					Priority:    1,
					IssueType:   types.TypeTask,
				},
			},
			wantExactMatches:  0,
			wantCollisions:    1,
			wantNewIssues:     0,
			checkCollisionID:  "bd-1",
			expectedConflicts: []string{"title"},
		},
		{
			name: "collision - multiple fields",
			incoming: []*types.Issue{
				{
					ID:          "bd-1",
					Title:       "Modified Title",
					Description: "Modified description",
					Status:      types.StatusInProgress,
					Priority:    2,
					IssueType:   types.TypeTask,
				},
			},
			wantExactMatches:  0,
			wantCollisions:    1,
			wantNewIssues:     0,
			checkCollisionID:  "bd-1",
			expectedConflicts: []string{"title", "description", "status", "priority"},
		},
		{
			name: "new issue",
			incoming: []*types.Issue{
				{
					ID:          "bd-2",
					Title:       "New Issue",
					Description: "New description",
					Status:      types.StatusOpen,
					Priority:    1,
					IssueType:   types.TypeBug,
				},
			},
			wantExactMatches: 0,
			wantCollisions:   0,
			wantNewIssues:    1,
		},
		{
			name: "mixed - exact, collision, and new",
			incoming: []*types.Issue{
				{
					ID:          "bd-1",
					Title:       "Existing Issue",
					Description: "Original description",
					Status:      types.StatusOpen,
					Priority:    1,
					IssueType:   types.TypeTask,
				},
				{
					ID:          "bd-2",
					Title:       "New Issue",
					Description: "New description",
					Status:      types.StatusOpen,
					Priority:    1,
					IssueType:   types.TypeBug,
				},
			},
			wantExactMatches: 1,
			wantCollisions:   0,
			wantNewIssues:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := DetectCollisions(ctx, store, tt.incoming)
			if err != nil {
				t.Fatalf("DetectCollisions failed: %v", err)
			}

			if len(result.ExactMatches) != tt.wantExactMatches {
				t.Errorf("ExactMatches: got %d, want %d", len(result.ExactMatches), tt.wantExactMatches)
			}
			if len(result.Collisions) != tt.wantCollisions {
				t.Errorf("Collisions: got %d, want %d", len(result.Collisions), tt.wantCollisions)
			}
			if len(result.NewIssues) != tt.wantNewIssues {
				t.Errorf("NewIssues: got %d, want %d", len(result.NewIssues), tt.wantNewIssues)
			}

			// Check collision details if expected
			if tt.checkCollisionID != "" && len(result.Collisions) > 0 {
				collision := result.Collisions[0]
				if collision.ID != tt.checkCollisionID {
					t.Errorf("Collision ID: got %s, want %s", collision.ID, tt.checkCollisionID)
				}
				if len(collision.ConflictingFields) != len(tt.expectedConflicts) {
					t.Errorf("ConflictingFields count: got %d, want %d", len(collision.ConflictingFields), len(tt.expectedConflicts))
				}
				for i, field := range tt.expectedConflicts {
					if i >= len(collision.ConflictingFields) || collision.ConflictingFields[i] != field {
						t.Errorf("ConflictingFields[%d]: got %v, want %s", i, collision.ConflictingFields, field)
					}
				}
			}
		})
	}
}

func TestCompareIssues(t *testing.T) {
	base := &types.Issue{
		ID:                 "test-1",
		Title:              "Base",
		Description:        "Base description",
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeTask,
		Assignee:           "alice",
		Design:             "Base design",
		AcceptanceCriteria: "Base acceptance",
		Notes:              "Base notes",
	}

	tests := []struct {
		name            string
		modify          func(*types.Issue) *types.Issue
		wantConflicts   []string
		wantNoConflicts bool
	}{
		{
			name: "identical issues",
			modify: func(i *types.Issue) *types.Issue {
				copy := *i
				return &copy
			},
			wantNoConflicts: true,
		},
		{
			name: "different title",
			modify: func(i *types.Issue) *types.Issue {
				copy := *i
				copy.Title = "Modified"
				return &copy
			},
			wantConflicts: []string{"title"},
		},
		{
			name: "different description",
			modify: func(i *types.Issue) *types.Issue {
				copy := *i
				copy.Description = "Modified"
				return &copy
			},
			wantConflicts: []string{"description"},
		},
		{
			name: "different status",
			modify: func(i *types.Issue) *types.Issue {
				copy := *i
				copy.Status = types.StatusClosed
				return &copy
			},
			wantConflicts: []string{"status"},
		},
		{
			name: "different priority",
			modify: func(i *types.Issue) *types.Issue {
				copy := *i
				copy.Priority = 2
				return &copy
			},
			wantConflicts: []string{"priority"},
		},
		{
			name: "different assignee",
			modify: func(i *types.Issue) *types.Issue {
				copy := *i
				copy.Assignee = "bob"
				return &copy
			},
			wantConflicts: []string{"assignee"},
		},
		{
			name: "multiple differences",
			modify: func(i *types.Issue) *types.Issue {
				copy := *i
				copy.Title = "Modified"
				copy.Priority = 2
				copy.Status = types.StatusClosed
				return &copy
			},
			wantConflicts: []string{"title", "status", "priority"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modified := tt.modify(base)
			conflicts := compareIssues(base, modified)

			if tt.wantNoConflicts {
				if len(conflicts) != 0 {
					t.Errorf("Expected no conflicts, got %v", conflicts)
				}
				return
			}

			if len(conflicts) != len(tt.wantConflicts) {
				t.Errorf("Conflict count: got %d, want %d (conflicts: %v)", len(conflicts), len(tt.wantConflicts), conflicts)
			}

			for _, wantField := range tt.wantConflicts {
				found := false
				for _, gotField := range conflicts {
					if gotField == wantField {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected conflict field %s not found in %v", wantField, conflicts)
				}
			}
		})
	}
}

func TestHashIssueContent(t *testing.T) {
	issue1 := &types.Issue{
		ID:                 "test-1",
		Title:              "Issue",
		Description:        "Description",
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeTask,
		Assignee:           "alice",
		Design:             "Design",
		AcceptanceCriteria: "Acceptance",
		Notes:              "Notes",
	}

	issue2 := &types.Issue{
		ID:                 "test-1",
		Title:              "Issue",
		Description:        "Description",
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeTask,
		Assignee:           "alice",
		Design:             "Design",
		AcceptanceCriteria: "Acceptance",
		Notes:              "Notes",
	}

	issue3 := &types.Issue{
		ID:                 "test-1",
		Title:              "Different",
		Description:        "Description",
		Status:             types.StatusOpen,
		Priority:           1,
		IssueType:          types.TypeTask,
		Assignee:           "alice",
		Design:             "Design",
		AcceptanceCriteria: "Acceptance",
		Notes:              "Notes",
	}

	hash1 := hashIssueContent(issue1)
	hash2 := hashIssueContent(issue2)
	hash3 := hashIssueContent(issue3)

	if hash1 != hash2 {
		t.Errorf("Expected identical issues to have same hash, got %s vs %s", hash1, hash2)
	}

	if hash1 == hash3 {
		t.Errorf("Expected different issues to have different hashes")
	}

	// Verify hash is deterministic
	hash1Again := hashIssueContent(issue1)
	if hash1 != hash1Again {
		t.Errorf("Hash function not deterministic: %s vs %s", hash1, hash1Again)
	}
}

func TestHashIssueContentWithExternalRef(t *testing.T) {
	ref1 := "JIRA-123"
	ref2 := "JIRA-456"

	issueWithRef1 := &types.Issue{
		ID:          "test-1",
		Title:       "Issue",
		ExternalRef: &ref1,
	}

	issueWithRef2 := &types.Issue{
		ID:          "test-1",
		Title:       "Issue",
		ExternalRef: &ref2,
	}

	issueNoRef := &types.Issue{
		ID:    "test-1",
		Title: "Issue",
	}

	hash1 := hashIssueContent(issueWithRef1)
	hash2 := hashIssueContent(issueWithRef2)
	hash3 := hashIssueContent(issueNoRef)

	if hash1 == hash2 {
		t.Errorf("Expected different external refs to produce different hashes")
	}

	if hash1 == hash3 {
		t.Errorf("Expected issue with external ref to differ from issue without")
	}
}
