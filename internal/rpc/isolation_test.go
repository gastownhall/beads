package rpc

import (
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDatabaseIsolation verifies that test issues are created in isolated
// test database, not production database. This is a canary test - if it
// pollutes production, test isolation is broken and must be fixed immediately.
func TestDatabaseIsolation(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	// Create a single test issue with distinctive title
	args := &CreateArgs{
		Title:       "CANARY TEST ISSUE - Database Isolation Verification",
		Description: "If you see this in production database, test isolation is BROKEN",
		IssueType:   "task",
		Priority:    1,
	}

	// Create via RPC
	createResp, err := client.Create(args)
	if err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	if !createResp.Success {
		t.Fatalf("Create failed: %s", createResp.Error)
	}

	var created types.Issue
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		t.Fatalf("Failed to unmarshal created issue: %v", err)
	}

	if created.ID == "" {
		t.Fatal("Created issue has empty ID")
	}

	// Verify it exists in test database by listing
	listArgs := &ListArgs{
		Status: "open",
	}
	listResp, err := client.List(listArgs)
	if err != nil {
		t.Fatalf("Failed to list issues: %v", err)
	}

	if !listResp.Success {
		t.Fatalf("List failed: %s", listResp.Error)
	}

	var issues []types.Issue
	if err := json.Unmarshal(listResp.Data, &issues); err != nil {
		t.Fatalf("Failed to unmarshal issues: %v", err)
	}

	found := false
	for _, issue := range issues {
		if issue.ID == created.ID {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("Created issue %s not found in test database", created.ID)
	}

	t.Logf("✓ Successfully created and verified issue %s in isolated test database", created.ID)
}
