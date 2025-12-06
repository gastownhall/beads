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
		{StatusTombstone, true},
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

func TestIsTombstone(t *testing.T) {
	tests := []struct {
		name   string
		issue  Issue
		expect bool
	}{
		{
			name: "tombstone issue",
			issue: Issue{
				ID:        "test-1",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
			},
			expect: true,
		},
		{
			name: "open issue",
			issue: Issue{
				ID:        "test-1",
				Title:     "Open issue",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeTask,
			},
			expect: false,
		},
		{
			name: "closed issue",
			issue: Issue{
				ID:        "test-1",
				Title:     "Closed issue",
				Status:    StatusClosed,
				Priority:  2,
				IssueType: TypeTask,
				ClosedAt:  timePtr(time.Now()),
			},
			expect: false,
		},
		{
			name: "in_progress issue",
			issue: Issue{
				ID:        "test-1",
				Title:     "In progress issue",
				Status:    StatusInProgress,
				Priority:  2,
				IssueType: TypeTask,
			},
			expect: false,
		},
		{
			name: "blocked issue",
			issue: Issue{
				ID:        "test-1",
				Title:     "Blocked issue",
				Status:    StatusBlocked,
				Priority:  2,
				IssueType: TypeTask,
			},
			expect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.issue.IsTombstone(); got != tt.expect {
				t.Errorf("Issue.IsTombstone() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestStatusIsValidWithCustom(t *testing.T) {
	customStatuses := []string{"awaiting_review", "awaiting_testing", "awaiting_docs"}

	tests := []struct {
		name           string
		status         Status
		customStatuses []string
		valid          bool
	}{
		// Built-in statuses should always be valid
		{"built-in open", StatusOpen, nil, true},
		{"built-in open with custom", StatusOpen, customStatuses, true},
		{"built-in closed", StatusClosed, customStatuses, true},

		// Custom statuses with config
		{"custom awaiting_review", Status("awaiting_review"), customStatuses, true},
		{"custom awaiting_testing", Status("awaiting_testing"), customStatuses, true},
		{"custom awaiting_docs", Status("awaiting_docs"), customStatuses, true},

		// Custom statuses without config (should fail)
		{"custom without config", Status("awaiting_review"), nil, false},
		{"custom without config empty", Status("awaiting_review"), []string{}, false},

		// Invalid statuses
		{"invalid status", Status("not_a_status"), customStatuses, false},
		{"empty status", Status(""), customStatuses, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.IsValidWithCustom(tt.customStatuses); got != tt.valid {
				t.Errorf("Status(%q).IsValidWithCustom(%v) = %v, want %v", tt.status, tt.customStatuses, got, tt.valid)
			}
		})
	}
}

func TestValidateWithCustomStatuses(t *testing.T) {
	customStatuses := []string{"awaiting_review", "awaiting_testing"}

	tests := []struct {
		name           string
		issue          Issue
		customStatuses []string
		wantErr        bool
	}{
		{
			name: "valid issue with built-in status",
			issue: Issue{
				Title:     "Test Issue",
				Status:    StatusOpen,
				Priority:  1,
				IssueType: TypeTask,
			},
			customStatuses: nil,
			wantErr:        false,
		},
		{
			name: "valid issue with custom status",
			issue: Issue{
				Title:     "Test Issue",
				Status:    Status("awaiting_review"),
				Priority:  1,
				IssueType: TypeTask,
			},
			customStatuses: customStatuses,
			wantErr:        false,
		},
		{
			name: "invalid custom status without config",
			issue: Issue{
				Title:     "Test Issue",
				Status:    Status("awaiting_review"),
				Priority:  1,
				IssueType: TypeTask,
			},
			customStatuses: nil,
			wantErr:        true,
		},
		{
			name: "invalid custom status not in config",
			issue: Issue{
				Title:     "Test Issue",
				Status:    Status("unknown_status"),
				Priority:  1,
				IssueType: TypeTask,
			},
			customStatuses: customStatuses,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.issue.ValidateWithCustomStatuses(tt.customStatuses)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateWithCustomStatuses() error = %v, wantErr %v", err, tt.wantErr)
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

func TestIsExpired(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		issue   Issue
		ttl     time.Duration
		expired bool
	}{
		{
			name: "non-tombstone issue never expires",
			issue: Issue{
				ID:        "test-1",
				Title:     "Open issue",
				Status:    StatusOpen,
				Priority:  2,
				IssueType: TypeTask,
			},
			ttl:     0,
			expired: false,
		},
		{
			name: "closed issue never expires",
			issue: Issue{
				ID:        "test-2",
				Title:     "Closed issue",
				Status:    StatusClosed,
				Priority:  2,
				IssueType: TypeTask,
				ClosedAt:  timePtr(now),
			},
			ttl:     0,
			expired: false,
		},
		{
			name: "tombstone without DeletedAt does not expire",
			issue: Issue{
				ID:        "test-3",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: nil,
			},
			ttl:     0,
			expired: false,
		},
		{
			name: "tombstone within default TTL (30 days)",
			issue: Issue{
				ID:        "test-4",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-15 * 24 * time.Hour)), // 15 days ago
			},
			ttl:     0, // Use default TTL
			expired: false,
		},
		{
			name: "tombstone past default TTL (30 days)",
			issue: Issue{
				ID:        "test-5",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-35 * 24 * time.Hour)), // 35 days ago (past 30 days + 1 hour grace)
			},
			ttl:     0, // Use default TTL
			expired: true,
		},
		{
			name: "tombstone within custom TTL (7 days)",
			issue: Issue{
				ID:        "test-6",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-3 * 24 * time.Hour)), // 3 days ago
			},
			ttl:     7 * 24 * time.Hour,
			expired: false,
		},
		{
			name: "tombstone past custom TTL (7 days)",
			issue: Issue{
				ID:        "test-7",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-9 * 24 * time.Hour)), // 9 days ago (past 7 days + 1 hour grace)
			},
			ttl:     7 * 24 * time.Hour,
			expired: true,
		},
		{
			name: "tombstone at exact TTL boundary (within grace period)",
			issue: Issue{
				ID:        "test-8",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-30 * 24 * time.Hour)), // Exactly 30 days ago
			},
			ttl:     0, // Use default TTL (30 days + 1 hour grace)
			expired: false,
		},
		{
			name: "tombstone just past TTL boundary (beyond grace period)",
			issue: Issue{
				ID:        "test-9",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-(30*24*time.Hour + 2*time.Hour))), // 30 days + 2 hours ago
			},
			ttl:     0, // Use default TTL (30 days + 1 hour grace)
			expired: true,
		},
		{
			name: "tombstone within grace period",
			issue: Issue{
				ID:        "test-10",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-(30*24*time.Hour + 30*time.Minute))), // 30 days + 30 minutes ago
			},
			ttl:     0, // Use default TTL (30 days + 1 hour grace)
			expired: false,
		},
		{
			name: "tombstone with MinTombstoneTTL (7 days)",
			issue: Issue{
				ID:        "test-11",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-10 * 24 * time.Hour)), // 10 days ago
			},
			ttl:     MinTombstoneTTL, // 7 days
			expired: true,
		},
		{
			name: "tombstone with very short TTL (1 hour)",
			issue: Issue{
				ID:        "test-12",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(-3 * time.Hour)), // 3 hours ago
			},
			ttl:     1 * time.Hour, // 1 hour + 1 hour grace = 2 hours total
			expired: true,
		},
		{
			name: "tombstone deleted in the future (clock skew)",
			issue: Issue{
				ID:        "test-13",
				Title:     "(deleted)",
				Status:    StatusTombstone,
				Priority:  0,
				IssueType: TypeTask,
				DeletedAt: timePtr(now.Add(1 * time.Hour)), // 1 hour in the future
			},
			ttl:     7 * 24 * time.Hour,
			expired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.issue.IsExpired(tt.ttl)
			if got != tt.expired {
				t.Errorf("Issue.IsExpired(%v) = %v, want %v", tt.ttl, got, tt.expired)
			}
		})
	}
}

func TestTombstoneTTLConstants(t *testing.T) {
	// Test that constants have expected values
	if DefaultTombstoneTTL != 30*24*time.Hour {
		t.Errorf("DefaultTombstoneTTL = %v, want %v", DefaultTombstoneTTL, 30*24*time.Hour)
	}
	if MinTombstoneTTL != 7*24*time.Hour {
		t.Errorf("MinTombstoneTTL = %v, want %v", MinTombstoneTTL, 7*24*time.Hour)
	}
	if ClockSkewGrace != 1*time.Hour {
		t.Errorf("ClockSkewGrace = %v, want %v", ClockSkewGrace, 1*time.Hour)
	}

	// Test that MinTombstoneTTL is less than DefaultTombstoneTTL
	if MinTombstoneTTL >= DefaultTombstoneTTL {
		t.Errorf("MinTombstoneTTL (%v) should be less than DefaultTombstoneTTL (%v)", MinTombstoneTTL, DefaultTombstoneTTL)
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
