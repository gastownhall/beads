package types

import (
	"testing"
	"time"
)

func TestIssueValidation(t *testing.T) {
	tests := []struct {
		name    string
		issue   Issue
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid issue",
			issue: Issue{
				ID:          "test-1",
				Title:       "Valid issue",
				Description: "Description",
				Status:      StatusOpen,
				Priority:    2,
				IssueType:   TypeFeature,
			},
			wantErr: false,
		},
		{
			name: "missing title",
			issue: Issue{
				ID:        "test-1",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "title is required",
		},
		{
			name: "title too long",
			issue: Issue{
				ID:        "test-1",
				Title:     string(make([]byte, 501)), // 501 characters
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "title must be 500 characters or less",
		},
		{
			name: "invalid priority too low",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  -1,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "priority must be between 0 and 4",
		},
		{
			name: "invalid priority too high",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  5,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "priority must be between 0 and 4",
		},
		{
			name: "invalid status",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    Status("invalid"),
				Priority:  2,
				IssueType: TypeFeature,
			},
			wantErr: true,
			errMsg:  "invalid status",
		},
		{
			name: "invalid issue type",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: IssueType("invalid"),
			},
			wantErr: true,
			errMsg:  "invalid issue type",
		},
		{
			name: "negative estimated minutes",
			issue: Issue{
				ID:               "test-1",
				Title:            "Test",
				Status:           StatusOpen,
				Priority:         2,
				IssueType:        TypeFeature,
				EstimatedMinutes: intPtr(-10),
			},
			wantErr: true,
			errMsg:  "estimated_minutes cannot be negative",
		},
		{
			name: "valid estimated minutes",
			issue: Issue{
				ID:               "test-1",
				Title:            "Test",
				Status:           StatusOpen,
				Priority:         2,
				IssueType:        TypeFeature,
				EstimatedMinutes: intPtr(60),
			},
			wantErr: false,
		},
		{
			name: "closed issue without closed_at",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusClosed,
				Priority:  2,
				IssueType: TypeFeature,
				ClosedAt:  nil,
			},
			wantErr: true,
			errMsg:  "closed issues must have closed_at timestamp",
		},
		{
			name: "open issue with closed_at",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeFeature,
				ClosedAt:  timePtr(time.Now()),
			},
			wantErr: true,
			errMsg:  "non-closed issues cannot have closed_at timestamp",
		},
		{
			name: "in_progress issue with closed_at",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusInProgress,
				Priority:  2,
				IssueType: TypeFeature,
				ClosedAt:  timePtr(time.Now()),
			},
			wantErr: true,
			errMsg:  "non-closed issues cannot have closed_at timestamp",
		},
		{
			name: "closed issue with closed_at",
			issue: Issue{
				ID:        "test-1",
				Title:     "Test",
				Status:    StatusClosed,
				Priority:  2,
				IssueType: TypeFeature,
				ClosedAt:  timePtr(time.Now()),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.issue.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error containing %q, got nil", tt.errMsg)
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestStatusIsValid(t *testing.T) {
	tests := []struct {
		status Status
		valid  bool
	}{
		{StatusOpen, true},
		{StatusInProgress, true},
		{StatusBlocked, true},
		{StatusClosed, true},
		{Status("invalid"), false},
		{Status(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsValid(); got != tt.valid {
				t.Errorf("Status(%q).IsValid() = %v, want %v", tt.status, got, tt.valid)
			}
		})
	}
}

func TestIssueTypeIsValid(t *testing.T) {
	tests := []struct {
		issueType IssueType
		valid     bool
	}{
		{TypeBug, true},
		{TypeFeature, true},
		{TypeTask, true},
		{TypeEpic, true},
		{TypeChore, true},
		{IssueType("invalid"), false},
		{IssueType(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.issueType), func(t *testing.T) {
			if got := tt.issueType.IsValid(); got != tt.valid {
				t.Errorf("IssueType(%q).IsValid() = %v, want %v", tt.issueType, got, tt.valid)
			}
		})
	}
}

func TestDependencyTypeIsValid(t *testing.T) {
	tests := []struct {
		depType DependencyType
		valid   bool
	}{
		{DepBlocks, true},
		{DepRelated, true},
		{DepParentChild, true},
		{DepDiscoveredFrom, true},
		{DependencyType("invalid"), false},
		{DependencyType(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.depType), func(t *testing.T) {
			if got := tt.depType.IsValid(); got != tt.valid {
				t.Errorf("DependencyType(%q).IsValid() = %v, want %v", tt.depType, got, tt.valid)
			}
		})
	}
}

func TestIssueStructFields(t *testing.T) {
	// Test that all time fields work correctly
	now := time.Now()
	closedAt := now.Add(time.Hour)

	issue := Issue{
		ID:          "test-1",
		Title:       "Test Issue",
		Description: "Test description",
		Status:      StatusClosed,
		Priority:    1,
		IssueType:   TypeBug,
		CreatedAt:   now,
		UpdatedAt:   now,
		ClosedAt:    &closedAt,
	}

	if issue.CreatedAt != now {
		t.Errorf("CreatedAt = %v, want %v", issue.CreatedAt, now)
	}
	if issue.ClosedAt == nil || *issue.ClosedAt != closedAt {
		t.Errorf("ClosedAt = %v, want %v", issue.ClosedAt, closedAt)
	}
}

func TestBlockedIssueEmbedding(t *testing.T) {
	blocked := BlockedIssue{
		Issue: Issue{
			ID:        "test-1",
			Title:     "Blocked issue",
			Status:    StatusBlocked,
			Priority:  2,
			IssueType: TypeFeature,
		},
		BlockedByCount: 2,
		BlockedBy:      []string{"test-2", "test-3"},
	}

	// Test that embedded Issue fields are accessible
	if blocked.ID != "test-1" {
		t.Errorf("BlockedIssue.ID = %q, want %q", blocked.ID, "test-1")
	}
	if blocked.BlockedByCount != 2 {
		t.Errorf("BlockedByCount = %d, want 2", blocked.BlockedByCount)
	}
	if len(blocked.BlockedBy) != 2 {
		t.Errorf("len(BlockedBy) = %d, want 2", len(blocked.BlockedBy))
	}
}

func TestTreeNodeEmbedding(t *testing.T) {
	node := TreeNode{
		Issue: Issue{
			ID:        "test-1",
			Title:     "Root node",
			Status:    StatusOpen,
			Priority:  1,
			IssueType: TypeEpic,
		},
		Depth:     0,
		Truncated: false,
	}

	// Test that embedded Issue fields are accessible
	if node.ID != "test-1" {
		t.Errorf("TreeNode.ID = %q, want %q", node.ID, "test-1")
	}
	if node.Depth != 0 {
		t.Errorf("Depth = %d, want 0", node.Depth)
	}
}

func TestComputeContentHash(t *testing.T) {
	issue1 := Issue{
		ID:                "test-1",
		Title:             "Test Issue",
		Description:       "Description",
		Status:            StatusOpen,
		Priority:          2,
		IssueType:         TypeFeature,
		EstimatedMinutes:  intPtr(60),
	}

	// Same content should produce same hash
	issue2 := Issue{
		ID:                "test-2", // Different ID
		Title:             "Test Issue",
		Description:       "Description",
		Status:            StatusOpen,
		Priority:          2,
		IssueType:         TypeFeature,
		EstimatedMinutes:  intPtr(60),
		CreatedAt:         time.Now(), // Different timestamp
	}

	hash1 := issue1.ComputeContentHash()
	hash2 := issue2.ComputeContentHash()

	if hash1 != hash2 {
		t.Errorf("Expected same hash for identical content, got %s and %s", hash1, hash2)
	}

	// Different content should produce different hash
	issue3 := issue1
	issue3.Title = "Different Title"
	hash3 := issue3.ComputeContentHash()

	if hash1 == hash3 {
		t.Errorf("Expected different hash for different content")
	}

	// Test with external ref
	externalRef := "EXT-123"
	issue4 := issue1
	issue4.ExternalRef = &externalRef
	hash4 := issue4.ComputeContentHash()

	if hash1 == hash4 {
		t.Errorf("Expected different hash when external ref is present")
	}
}

func TestSortPolicyIsValid(t *testing.T) {
	tests := []struct {
		policy SortPolicy
		valid  bool
	}{
		{SortPolicyHybrid, true},
		{SortPolicyPriority, true},
		{SortPolicyOldest, true},
		{SortPolicy(""), true}, // empty is valid
		{SortPolicy("invalid"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.policy), func(t *testing.T) {
			if got := tt.policy.IsValid(); got != tt.valid {
				t.Errorf("SortPolicy(%q).IsValid() = %v, want %v", tt.policy, got, tt.valid)
			}
		})
	}
}

// Helper functions

func intPtr(i int) *int {
	return &i
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
