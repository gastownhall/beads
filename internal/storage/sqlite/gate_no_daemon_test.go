package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestGateFieldsPreservedAcrossConnections reproduces beads-70c4:
// Gate await fields should not be cleared when a new database connection
// is opened (simulating --no-daemon CLI access).
func TestGateFieldsPreservedAcrossConnections(t *testing.T) {
	// Use a temporary file database (not :memory:) to simulate real-world scenario
	dbPath := t.TempDir() + "/beads.db"

	ctx := context.Background()

	// Step 1: Create a database and add a gate with await fields
	// (simulating daemon creating a gate)
	store1, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create first store: %v", err)
	}

	// Initialize the database with a prefix
	if err := store1.SetConfig(ctx, "issue_prefix", "beads"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Configure custom types for Gas Town types (gate is not a core type)
	if err := store1.SetConfig(ctx, "types.custom", "gate"); err != nil {
		t.Fatalf("failed to set types.custom: %v", err)
	}

	gate := &types.Issue{
		ID:        "beads-test1",
		Title:     "Test Gate",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: "gate",
		Ephemeral: true,
		AwaitType: "timer",
		AwaitID:   "5s",
		Timeout:   5 * time.Second,
		Waiters:   []string{"system"},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	gate.ContentHash = gate.ComputeContentHash()

	if err := store1.CreateIssue(ctx, gate, "daemon"); err != nil {
		t.Fatalf("failed to create gate: %v", err)
	}

	// Verify gate was created with await fields
	retrieved1, err := store1.GetIssue(ctx, gate.ID)
	if err != nil || retrieved1 == nil {
		t.Fatalf("failed to get gate from store1: %v", err)
	}
	if retrieved1.AwaitType != "timer" {
		t.Errorf("store1: expected AwaitType=timer, got %q", retrieved1.AwaitType)
	}
	if retrieved1.AwaitID != "5s" {
		t.Errorf("store1: expected AwaitID=5s, got %q", retrieved1.AwaitID)
	}

	// Close the first store (simulating daemon connection)
	if err := store1.Close(); err != nil {
		t.Fatalf("failed to close store1: %v", err)
	}

	// Step 2: Open a NEW connection to the same database
	// (simulating `bd show --no-daemon` opening a new connection)
	store2, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create second store: %v", err)
	}
	defer store2.Close()

	// Step 3: Read the gate from the new connection
	// This should NOT clear the await fields
	retrieved2, err := store2.GetIssue(ctx, gate.ID)
	if err != nil || retrieved2 == nil {
		t.Fatalf("failed to get gate from store2: %v", err)
	}

	// Verify await fields are PRESERVED
	if retrieved2.AwaitType != "timer" {
		t.Errorf("AwaitType was cleared! expected 'timer', got %q", retrieved2.AwaitType)
	}
	if retrieved2.AwaitID != "5s" {
		t.Errorf("AwaitID was cleared! expected '5s', got %q", retrieved2.AwaitID)
	}
	if retrieved2.Timeout != 5*time.Second {
		t.Errorf("Timeout was cleared! expected %v, got %v", 5*time.Second, retrieved2.Timeout)
	}
	if len(retrieved2.Waiters) != 1 || retrieved2.Waiters[0] != "system" {
		t.Errorf("Waiters was cleared! expected [system], got %v", retrieved2.Waiters)
	}
}
