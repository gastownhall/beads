package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestPrefixValidation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-prefix-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	ctx = context.Background()

	// Set prefix to "test"
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	tests := []struct {
		name    string
		issueID string
		wantErr bool
	}{
		{
			name:    "valid prefix - matches",
			issueID: "test-123",
			wantErr: false,
		},
		{
			name:    "invalid prefix - wrong prefix",
			issueID: "bd-456",
			wantErr: true,
		},
		{
			name:    "invalid prefix - no dash",
			issueID: "test123",
			wantErr: true,
		},
		{
			name:    "invalid prefix - empty",
			issueID: "",
			wantErr: false, // Empty ID triggers auto-generation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &types.Issue{
				ID:        tt.issueID,
				Title:     "Test issue",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: types.TypeTask,
			}

			err := store.CreateIssue(ctx, issue, "test-user")
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateIssue() error = %v, wantErr %v", err, tt.wantErr)
			}

			// If we expected success and the ID was empty, verify it was generated with correct prefix
			if err == nil && tt.issueID == "" {
				if issue.ID == "" {
					t.Error("ID should be generated")
				}
				if issue.ID[:5] != "test-" {
					t.Errorf("Generated ID should have prefix 'test-', got %s", issue.ID)
				}
			}
		})
	}
}

func TestPrefixValidationBatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-prefix-batch-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer store.Close()

	ctx = context.Background()

	// Set prefix to "batch"
	if err := store.SetConfig(ctx, "issue_prefix", "batch"); err != nil {
		t.Fatalf("failed to set prefix: %v", err)
	}

	tests := []struct {
		name    string
		issues  []*types.Issue
		wantErr bool
	}{
		{
			name: "all valid prefixes",
			issues: []*types.Issue{
				{ID: "batch-1", Title: "Test 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "batch-2", Title: "Test 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			wantErr: false,
		},
		{
			name: "one invalid prefix in batch",
			issues: []*types.Issue{
				{ID: "batch-10", Title: "Test 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "wrong-20", Title: "Test 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			wantErr: true,
		},
		{
			name: "mixed auto-generated and explicit",
			issues: []*types.Issue{
				{ID: "batch-100", Title: "Explicit ID", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "", Title: "Auto ID", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			wantErr: false,
		},
		{
			name: "mixed with invalid prefix",
			issues: []*types.Issue{
				{ID: "", Title: "Auto ID", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
				{ID: "invalid-500", Title: "Wrong prefix", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.CreateIssues(ctx, tt.issues, "test-user")
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateIssues() error = %v, wantErr %v", err, tt.wantErr)
			}

			// For successful batches, verify all IDs have correct prefix
			if err == nil {
				for i, issue := range tt.issues {
					if issue.ID[:6] != "batch-" {
						t.Errorf("Issue %d: ID should have prefix 'batch-', got %s", i, issue.ID)
					}
				}
			}
		})
	}
}
