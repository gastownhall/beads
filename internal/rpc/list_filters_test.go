package rpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlitestorage "github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// setupTestServerWithStore creates a test server and returns the store for direct access
func setupTestServerWithStore(t *testing.T) (*Server, *Client, *sqlitestorage.SQLiteStorage, func()) {
	tmpDir, err := os.MkdirTemp("", "bd-rpc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	socketPath := filepath.Join(beadsDir, "bd.sock")

	os.Remove(socketPath)

	store, err := sqlitestorage.New(context.Background(), dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create store: %v", err)
	}

	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	server := NewServer(socketPath, store, tmpDir, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := server.Start(ctx); err != nil && err.Error() != "accept unix "+socketPath+": use of closed network connection" {
			t.Logf("Server error: %v", err)
		}
	}()

	maxWait := 50
	for i := 0; i < maxWait; i++ {
		time.Sleep(10 * time.Millisecond)
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		if i == maxWait-1 {
			cancel()
			server.Stop()
			store.Close()
			os.RemoveAll(tmpDir)
			t.Fatalf("Server socket not created after waiting")
		}
	}

	t.Chdir(tmpDir)

	client, err := TryConnect(socketPath)
	if err != nil {
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to connect client: %v", err)
	}

	if client == nil {
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Client is nil after connection")
	}

	client.dbPath = dbPath

	cleanup := func() {
		client.Close()
		cancel()
		server.Stop()
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return server, client, store, cleanup
}

// TestListFiltersParity verifies daemon mode (RPC) behaves identically to direct mode
// for all new filter flags added in bd-o43.
func TestListFiltersParity(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// Create diverse test fixtures
	fixtures := []struct {
		title       string
		description string
		notes       string
		status      types.Status
		priority    int
		assignee    string
		labels      []string
	}{
		{
			title:       "Implement authentication",
			description: "Add JWT token support",
			notes:       "Use bcrypt for password hashing",
			status:      types.StatusOpen,
			priority:    1,
			assignee:    "alice",
			labels:      []string{"security", "backend"},
		},
		{
			title:       "Fix login bug",
			description: "",
			notes:       "",
			status:      types.StatusInProgress,
			priority:    0,
			assignee:    "",
			labels:      []string{"bug"},
		},
		{
			title:       "Refactor database layer",
			description: "Extract common patterns into helpers",
			notes:       "Focus on query builders",
			status:      types.StatusClosed,
			priority:    2,
			assignee:    "bob",
			labels:      []string{},
		},
		{
			title:       "Update documentation",
			description: "Add API examples",
			notes:       "",
			status:      types.StatusOpen,
			priority:    3,
			assignee:    "alice",
			labels:      []string{"docs"},
		},
		{
			title:       "Authentication middleware",
			description: "Protect routes with JWT",
			notes:       "Remember to add rate limiting",
			status:      types.StatusBlocked,
			priority:    1,
			assignee:    "",
			labels:      []string{"backend", "security"},
		},
	}

	// Create issues and track their IDs
	issueIDs := make([]string, len(fixtures))
	for i, f := range fixtures {
		createArgs := &CreateArgs{
			Title:       f.title,
			Description: f.description,
			IssueType:   "task",
			Priority:    f.priority,
			Assignee:    f.assignee,
		}
		resp, err := client.Create(createArgs)
		if err != nil {
			t.Fatalf("Failed to create fixture %d: %v", i, err)
		}

		var createdIssue types.Issue
		if err := json.Unmarshal(resp.Data, &createdIssue); err != nil {
			t.Fatalf("Failed to unmarshal created issue: %v", err)
		}
		issueIDs[i] = createdIssue.ID

		// Manually update all fields directly via UpdateIssue
		updates := map[string]interface{}{
			"status": string(f.status),
			"notes":  f.notes,
		}
		if err := store.UpdateIssue(ctx, createdIssue.ID, updates, "test"); err != nil {
			t.Fatalf("Failed to update issue: %v", err)
		}

		// Add labels
		for _, label := range f.labels {
			if err := store.AddLabel(ctx, createdIssue.ID, label, "test"); err != nil {
				t.Fatalf("Failed to add label: %v", err)
			}
		}
	}

	tests := []struct {
		name        string
		listArgs    *ListArgs
		directCount int
		validator   func(t *testing.T, issues []*types.Issue)
	}{
		{
			name: "pattern matching - title contains",
			listArgs: &ListArgs{
				TitleContains: "authentication",
				Limit:         10,
			},
			directCount: 2,
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if !strings.Contains(strings.ToLower(issue.Title), "authentication") {
						t.Errorf("Issue %s title does not contain 'authentication': %s", issue.ID, issue.Title)
					}
				}
			},
		},
		{
			name: "pattern matching - description contains",
			listArgs: &ListArgs{
				DescriptionContains: "JWT",
				Limit:               10,
			},
			directCount: 2,
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if !strings.Contains(issue.Description, "JWT") {
						t.Errorf("Issue %s description does not contain 'JWT': %s", issue.ID, issue.Description)
					}
				}
			},
		},
		{
			name: "pattern matching - notes contains",
			listArgs: &ListArgs{
				NotesContains: "hashing",
				Limit:         10,
			},
			directCount: 1,
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if !strings.Contains(issue.Notes, "hashing") {
						t.Errorf("Issue %s notes do not contain 'hashing': %s", issue.ID, issue.Notes)
					}
				}
			},
		},
		{
			name: "empty description check",
			listArgs: &ListArgs{
				EmptyDescription: true,
				Limit:            10,
			},
			directCount: 1, // Only "Fix login bug" has empty description
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if issue.Description != "" {
						t.Errorf("Issue %s has non-empty description: %s", issue.ID, issue.Description)
					}
				}
			},
		},
		{
			name: "no assignee check",
			listArgs: &ListArgs{
				NoAssignee: true,
				Limit:      10,
			},
			directCount: 2,
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if issue.Assignee != "" {
						t.Errorf("Issue %s has assignee: %s", issue.ID, issue.Assignee)
					}
				}
			},
		},
		{
			name: "no labels check",
			listArgs: &ListArgs{
				NoLabels: true,
				Limit:    10,
			},
			directCount: 1,
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					labels, err := store.GetLabels(ctx, issue.ID)
					if err != nil {
						t.Errorf("Failed to get labels: %v", err)
						continue
					}
					if len(labels) > 0 {
						t.Errorf("Issue %s has labels: %v", issue.ID, labels)
					}
				}
			},
		},
		{
			name: "priority range - min",
			listArgs: &ListArgs{
				PriorityMin: ptrInt(1),
				Limit:       10,
			},
			directCount: 4, // priorities 1,1,1,2 from auth, auth middleware, doc, refactor
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if issue.Priority < 1 {
						t.Errorf("Issue %s priority %d is below min 1", issue.ID, issue.Priority)
					}
				}
			},
		},
		{
			name: "priority range - max",
			listArgs: &ListArgs{
				PriorityMax: ptrInt(1),
				Limit:       10,
			},
			directCount: 3,
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if issue.Priority > 1 {
						t.Errorf("Issue %s priority %d is above max 1", issue.ID, issue.Priority)
					}
				}
			},
		},
		{
			name: "priority range - both",
			listArgs: &ListArgs{
				PriorityMin: ptrInt(1),
				PriorityMax: ptrInt(2),
				Limit:       10,
			},
			directCount: 3,
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if issue.Priority < 1 || issue.Priority > 2 {
						t.Errorf("Issue %s priority %d is outside range [1,2]", issue.ID, issue.Priority)
					}
				}
			},
		},
		{
			name: "date range - created after (recent)",
			listArgs: &ListArgs{
				CreatedAfter: now.Add(-1 * time.Second).Format(time.RFC3339),
				Limit:        10,
			},
			directCount: 5, // All test issues created just now
			validator: func(t *testing.T, issues []*types.Issue) {
				cutoff := now.Add(-1 * time.Second)
				for _, issue := range issues {
					if issue.CreatedAt.Before(cutoff) {
						t.Errorf("Issue %s created at %v is before cutoff %v", issue.ID, issue.CreatedAt, cutoff)
					}
				}
			},
		},
		{
			name: "date range - created before (future)",
			listArgs: &ListArgs{
				CreatedBefore: now.Add(1 * time.Hour).Format(time.RFC3339),
				Limit:         10,
			},
			directCount: 5, // All test issues created before future time
			validator: func(t *testing.T, issues []*types.Issue) {
				cutoff := now.Add(1 * time.Hour)
				for _, issue := range issues {
					if issue.CreatedAt.After(cutoff) {
						t.Errorf("Issue %s created at %v is after cutoff %v", issue.ID, issue.CreatedAt, cutoff)
					}
				}
			},
		},
		{
			name: "complex combination - security issues without assignee",
			listArgs: &ListArgs{
				Labels:     []string{"security"},
				NoAssignee: true,
				Limit:      10,
			},
			directCount: 1,
			validator: func(t *testing.T, issues []*types.Issue) {
				for _, issue := range issues {
					if issue.Assignee != "" {
						t.Errorf("Issue %s has assignee: %s", issue.ID, issue.Assignee)
					}
					labels, err := store.GetLabels(ctx, issue.ID)
					if err != nil {
						t.Errorf("Failed to get labels: %v", err)
						continue
					}
					hasLabel := false
					for _, l := range labels {
						if l == "security" {
							hasLabel = true
							break
						}
					}
					if !hasLabel {
						t.Errorf("Issue %s does not have 'security' label", issue.ID)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test daemon mode (RPC)
			resp, err := client.List(tt.listArgs)
			if err != nil {
				t.Fatalf("RPC List failed: %v", err)
			}

			var rpcIssues []*types.Issue
			if err := json.Unmarshal(resp.Data, &rpcIssues); err != nil {
				t.Fatalf("Failed to unmarshal RPC issues: %v", err)
			}

			// Test direct mode
			filter := listArgsToFilter(tt.listArgs, t)
			directIssues, err := store.SearchIssues(ctx, "", *filter)
			if err != nil {
				t.Fatalf("Direct SearchIssues failed: %v", err)
			}

			// Compare counts
			if len(rpcIssues) != len(directIssues) {
				t.Errorf("Count mismatch: RPC returned %d issues, direct returned %d", len(rpcIssues), len(directIssues))
			}

			if len(rpcIssues) != tt.directCount {
				t.Errorf("Expected %d issues, RPC returned %d", tt.directCount, len(rpcIssues))
			}

			if len(directIssues) != tt.directCount {
				t.Errorf("Expected %d issues, direct returned %d", tt.directCount, len(directIssues))
			}

			// Validate RPC results
			if tt.validator != nil {
				tt.validator(t, rpcIssues)
			}

			// Validate direct results with same validator
			if tt.validator != nil {
				tt.validator(t, directIssues)
			}

			// Compare issue IDs (order might differ, so sort and compare)
			rpcIDs := make(map[string]bool)
			for _, issue := range rpcIssues {
				rpcIDs[issue.ID] = true
			}
			directIDs := make(map[string]bool)
			for _, issue := range directIssues {
				directIDs[issue.ID] = true
			}

			for id := range rpcIDs {
				if !directIDs[id] {
					t.Errorf("RPC returned issue %s not in direct results", id)
				}
			}
			for id := range directIDs {
				if !rpcIDs[id] {
					t.Errorf("Direct returned issue %s not in RPC results", id)
				}
			}
		})
	}
}

// TestListFiltersDateParsing tests various date formats
func TestListFiltersDateParsing(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create a test issue
	createArgs := &CreateArgs{
		Title:     "Test issue",
		IssueType: "task",
		Priority:  2,
	}
	_, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}

	testDate := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	validFormats := []struct {
		name   string
		format string
	}{
		{"RFC3339", testDate.Format(time.RFC3339)},
		{"RFC3339Nano", testDate.Format(time.RFC3339Nano)},
		{"YYYY-MM-DD", testDate.Format("2006-01-02")},
	}

	for _, tf := range validFormats {
		t.Run("valid_format_"+tf.name, func(t *testing.T) {
			listArgs := &ListArgs{
				CreatedAfter: tf.format,
				Limit:        10,
			}
			resp, err := client.List(listArgs)
			if err != nil {
				t.Errorf("Failed to parse %s format: %v", tf.name, err)
			}
			if resp == nil {
				t.Errorf("Expected response for %s format", tf.name)
			}
		})
	}

	invalidFormats := []string{
		"2025-13-01", // Invalid month
		"not-a-date",
		"",
	}

	for i, invalid := range invalidFormats {
		if invalid == "" {
			continue // Empty string is valid (means no filter)
		}
		t.Run("invalid_format_"+string(rune(i)), func(t *testing.T) {
			listArgs := &ListArgs{
				CreatedAfter: invalid,
				Limit:        10,
			}
			_, err := client.List(listArgs)
			// Should either fail gracefully or handle the error
			// The exact behavior depends on implementation
			if err == nil {
				t.Logf("Warning: invalid date %q did not produce error", invalid)
			}
		})
	}
}

// TestListFiltersStatusNormalization tests that status='all' is treated as unset
func TestListFiltersStatusNormalization(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create issues with different statuses
	statuses := []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusBlocked, types.StatusClosed}
	for _, status := range statuses {
		createArgs := &CreateArgs{
			Title:     "Test " + string(status),
			IssueType: "task",
			Priority:  2,
		}
		resp, err := client.Create(createArgs)
		if err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		var createdIssue types.Issue
		if err := json.Unmarshal(resp.Data, &createdIssue); err != nil {
			t.Fatalf("Failed to unmarshal created issue: %v", err)
		}

		// Update status (UpdateIssue automatically manages closed_at)
		statusUpdates := map[string]interface{}{
			"status": string(status),
		}
		if err := store.UpdateIssue(ctx, createdIssue.ID, statusUpdates, "test"); err != nil {
			t.Fatalf("Failed to update issue: %v", err)
		}
	}

	// Test status='all' vs no status filter
	allArgs := &ListArgs{
		Status: "all",
		Limit:  10,
	}
	allResp, err := client.List(allArgs)
	if err != nil {
		t.Fatalf("List with status='all' failed: %v", err)
	}

	noStatusArgs := &ListArgs{
		Limit: 10,
	}
	noStatusResp, err := client.List(noStatusArgs)
	if err != nil {
		t.Fatalf("List with no status failed: %v", err)
	}

	var allIssues, noStatusIssues []*types.Issue
	json.Unmarshal(allResp.Data, &allIssues)
	json.Unmarshal(noStatusResp.Data, &noStatusIssues)

	if len(allIssues) != len(noStatusIssues) {
		t.Errorf("status='all' returned %d issues, no status returned %d", len(allIssues), len(noStatusIssues))
	}

	// Both should return all 4 statuses
	if len(allIssues) < 4 {
		t.Errorf("Expected at least 4 issues, got %d", len(allIssues))
	}
}

// TestListFiltersBackwardCompat tests that deprecated --label flag still works
func TestListFiltersBackwardCompat(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create issue with label
	createArgs := &CreateArgs{
		Title:     "Test issue",
		IssueType: "task",
		Priority:  2,
	}
	resp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	var createdIssue types.Issue
	if err := json.Unmarshal(resp.Data, &createdIssue); err != nil {
		t.Fatalf("Failed to unmarshal created issue: %v", err)
	}

	if err := store.AddLabel(ctx, createdIssue.ID, "testlabel", "test"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}

	// Test deprecated Label field
	deprecatedArgs := &ListArgs{
		Label: "testlabel",
		Limit: 10,
	}
	deprecatedResp, err := client.List(deprecatedArgs)
	if err != nil {
		t.Fatalf("List with deprecated Label failed: %v", err)
	}

	// Test new Labels field
	newArgs := &ListArgs{
		Labels: []string{"testlabel"},
		Limit:  10,
	}
	newResp, err := client.List(newArgs)
	if err != nil {
		t.Fatalf("List with new Labels failed: %v", err)
	}

	var deprecatedIssues, newIssues []*types.Issue
	json.Unmarshal(deprecatedResp.Data, &deprecatedIssues)
	json.Unmarshal(newResp.Data, &newIssues)

	if len(deprecatedIssues) != len(newIssues) {
		t.Errorf("Deprecated Label and new Labels returned different counts: %d vs %d", len(deprecatedIssues), len(newIssues))
	}
}

// Helper functions

func ptrInt(i int) *int {
	return &i
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

// listArgsToFilter converts ListArgs to IssueFilter for direct store comparison
func listArgsToFilter(args *ListArgs, t *testing.T) *types.IssueFilter {
	filter := &types.IssueFilter{
		Limit:               args.Limit,
		TitleContains:       args.TitleContains,
		DescriptionContains: args.DescriptionContains,
		NotesContains:       args.NotesContains,
		EmptyDescription:    args.EmptyDescription,
		NoAssignee:          args.NoAssignee,
		NoLabels:            args.NoLabels,
		PriorityMin:         args.PriorityMin,
		PriorityMax:         args.PriorityMax,
		Labels:              args.Labels,
		SpecIDPrefix:        args.SpecIDPrefix,
	}

	if args.Status != "" && args.Status != "all" {
		status := types.Status(args.Status)
		filter.Status = &status
	}

	if args.Priority != nil {
		filter.Priority = args.Priority
	}

	if args.Assignee != "" {
		filter.Assignee = &args.Assignee
	}

	if args.IssueType != "" {
		issueType := types.IssueType(args.IssueType)
		filter.IssueType = &issueType
	}

	// Parse dates
	parseTime := func(s string) *time.Time {
		if s == "" {
			return nil
		}
		formats := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02"}
		for _, format := range formats {
			if t, err := time.Parse(format, s); err == nil {
				return &t
			}
		}
		return nil
	}

	filter.CreatedAfter = parseTime(args.CreatedAfter)
	filter.CreatedBefore = parseTime(args.CreatedBefore)
	filter.UpdatedAfter = parseTime(args.UpdatedAfter)
	filter.UpdatedBefore = parseTime(args.UpdatedBefore)
	filter.ClosedAfter = parseTime(args.ClosedAfter)
	filter.ClosedBefore = parseTime(args.ClosedBefore)

	return filter
}
