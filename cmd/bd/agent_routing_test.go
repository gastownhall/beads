package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestAgentStateWithRouting tests that bd agent state respects routes.jsonl
// for cross-repo agent resolution. This is a regression test for the bug where
// bd agent state failed to find agents in routed databases while bd show worked.
//
// NOTE: This test uses os.Chdir and cannot run in parallel with other tests.
func TestAgentStateWithRouting(t *testing.T) {
	ctx := context.Background()

	// Create temp directory structure:
	// tmpDir/
	//   .beads/
	//     beads.db (town database)
	//     routes.jsonl (routing config)
	//   rig/
	//     .beads/
	//       beads.db (rig database with agent)
	tmpDir := t.TempDir()

	// Create town .beads directory
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	// Create rig .beads directory
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	// Initialize town database using helper (prefix without trailing hyphen)
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	// Initialize rig database using helper (prefix without trailing hyphen)
	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create an agent bead in the rig database (using task type with gt:agent label)
	agentBead := &types.Issue{
		ID:        "gt-testrig-polecat-test",
		Title:     "Agent: gt-testrig-polecat-test",
		IssueType: types.TypeTask, // Use task type; gt:agent label marks it as agent
		Status:    types.StatusOpen,
		RoleType:  "polecat",
		Rig:       "testrig",
	}
	if err := rigStore.CreateIssue(ctx, agentBead, "test"); err != nil {
		t.Fatalf("Failed to create agent bead: %v", err)
	}
	if err := rigStore.AddLabel(ctx, agentBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Create routes.jsonl in town .beads directory
	routesContent := `{"prefix":"gt-","path":"rig"}`
	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state for routing to work
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	// Change to tmpDir so routing can find town root via CWD
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Test the routed resolution
	result, err := resolveAndGetIssueWithRouting(ctx, townStore, "gt-testrig-polecat-test")
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting failed: %v", err)
	}
	if result == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil result")
	}
	defer result.Close()

	if result.Issue == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil issue")
	}

	if result.Issue.ID != "gt-testrig-polecat-test" {
		t.Errorf("Expected issue ID %q, got %q", "gt-testrig-polecat-test", result.Issue.ID)
	}

	if !result.Routed {
		t.Error("Expected result.Routed to be true for cross-repo lookup")
	}

	if result.Issue.IssueType != types.TypeTask {
		t.Errorf("Expected issue type %q, got %q", types.TypeTask, result.Issue.IssueType)
	}

	t.Logf("Successfully resolved agent %s via routing", result.Issue.ID)
}

// TestNeedsRoutingFunction tests the needsRouting function
func TestNeedsRoutingFunction(t *testing.T) {
	// Without dbPath set, needsRouting should return false
	oldDbPath := dbPath
	dbPath = ""
	t.Cleanup(func() { dbPath = oldDbPath })

	if needsRouting("any-id") {
		t.Error("needsRouting should return false when dbPath is empty")
	}
}

// TestAgentHeartbeatWithRouting tests that bd agent heartbeat respects routes.jsonl
//
// NOTE: This test uses os.Chdir and cannot run in parallel with other tests.
func TestAgentHeartbeatWithRouting(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()

	// Create town .beads directory
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	// Create rig .beads directory
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	// Initialize databases (prefix without trailing hyphen)
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create an agent bead in the rig database (using task type with gt:agent label)
	agentBead := &types.Issue{
		ID:        "gt-test-witness",
		Title:     "Agent: gt-test-witness",
		IssueType: types.TypeTask, // Use task type; gt:agent label marks it as agent
		Status:    types.StatusOpen,
		RoleType:  "witness",
		Rig:       "test",
	}
	if err := rigStore.CreateIssue(ctx, agentBead, "test"); err != nil {
		t.Fatalf("Failed to create agent bead: %v", err)
	}
	if err := rigStore.AddLabel(ctx, agentBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Create routes.jsonl
	routesContent := `{"prefix":"gt-","path":"rig"}`
	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	// Change to tmpDir so routing can find town root via CWD
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Test that we can resolve the agent from the town directory
	result, err := resolveAndGetIssueWithRouting(ctx, townStore, "gt-test-witness")
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting failed: %v", err)
	}
	if result == nil || result.Issue == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil")
	}
	defer result.Close()

	if result.Issue.ID != "gt-test-witness" {
		t.Errorf("Expected issue ID %q, got %q", "gt-test-witness", result.Issue.ID)
	}

	if !result.Routed {
		t.Error("Expected result.Routed to be true")
	}

	t.Logf("Successfully resolved agent %s via routing for heartbeat test", result.Issue.ID)
}

// TestAgentShowWithRouting tests that bd agent show respects routes.jsonl
//
// NOTE: This test uses os.Chdir and cannot run in parallel with other tests.
func TestAgentShowWithRouting(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()

	// Create town .beads directory
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	// Create rig .beads directory
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	// Initialize databases (prefix without trailing hyphen)
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create an agent bead in the rig database (using task type with gt:agent label)
	agentBead := &types.Issue{
		ID:        "gt-myrig-crew-alice",
		Title:     "Agent: gt-myrig-crew-alice",
		IssueType: types.TypeTask, // Use task type; gt:agent label marks it as agent
		Status:    types.StatusOpen,
		RoleType:  "crew",
		Rig:       "myrig",
	}
	if err := rigStore.CreateIssue(ctx, agentBead, "test"); err != nil {
		t.Fatalf("Failed to create agent bead: %v", err)
	}
	if err := rigStore.AddLabel(ctx, agentBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Create routes.jsonl
	routesContent := `{"prefix":"gt-","path":"rig"}`
	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	// Change to tmpDir so routing can find town root via CWD
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Test that we can resolve the agent from the town directory
	result, err := resolveAndGetIssueWithRouting(ctx, townStore, "gt-myrig-crew-alice")
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting failed: %v", err)
	}
	if result == nil || result.Issue == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil")
	}
	defer result.Close()

	if result.Issue.ID != "gt-myrig-crew-alice" {
		t.Errorf("Expected issue ID %q, got %q", "gt-myrig-crew-alice", result.Issue.ID)
	}

	if result.Issue.IssueType != types.TypeTask {
		t.Errorf("Expected issue type %q, got %q", types.TypeTask, result.Issue.IssueType)
	}

	t.Logf("Successfully resolved agent %s via routing for show test", result.Issue.ID)
}

// TestBeadsDirOverrideSkipsRouting tests that when BEADS_DIR is set,
// prefix-based routing is skipped and the local store is used.
// This is a regression test for GH#663: when BEADS_DIR points to town beads,
// bd show should query that store directly instead of prefix-routing to a rig.
//
// NOTE: This test uses os.Chdir and cannot run in parallel with other tests.
func TestBeadsDirOverrideSkipsRouting(t *testing.T) {
	ctx := context.Background()

	// Create temp directory structure:
	// tmpDir/
	//   .beads/
	//     beads.db (town database with prefix "gt" - has the bead)
	//     routes.jsonl (routing config that would route gt- to rig/)
	//   rig/
	//     .beads/
	//       beads.db (rig database with prefix "gt" - does NOT have the bead)
	//
	// Without BEADS_DIR: routing sends gt-* lookups to rig/.beads → miss
	// With BEADS_DIR=tmpDir/.beads: routing skipped → found in town store
	tmpDir := t.TempDir()

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	// Both stores use prefix "gt" — town holds the bead, rig is empty
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "gt")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	_ = newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create a bead in the town database
	townBead := &types.Issue{
		ID:        "gt-town-message",
		Title:     "Message stored in town beads",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := townStore.CreateIssue(ctx, townBead, "test"); err != nil {
		t.Fatalf("Failed to create bead: %v", err)
	}

	// Create routes.jsonl that would route gt- to rig/
	routesContent := `{"prefix":"gt-","path":"rig"}`
	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	// Change to tmpDir so routing can find town root via CWD
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Set BEADS_DIR to town beads — this should override prefix routing
	t.Setenv("BEADS_DIR", townBeadsDir)

	// Test: resolveAndGetIssueWithRouting should find the bead in town store
	// WITHOUT routing, because BEADS_DIR is set.
	result, err := resolveAndGetIssueWithRouting(ctx, townStore, "gt-town-message")
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting with BEADS_DIR failed: %v", err)
	}
	if result == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil — BEADS_DIR override not working")
	}
	defer result.Close()

	if result.Issue == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil issue")
	}
	if result.Issue.ID != "gt-town-message" {
		t.Errorf("Expected issue ID %q, got %q", "gt-town-message", result.Issue.ID)
	}
	if result.Routed {
		t.Error("Expected result.Routed to be false when BEADS_DIR is set")
	}

	// Test: needsRouting should return false when BEADS_DIR is set
	if needsRouting("gt-town-message") {
		t.Error("needsRouting should return false when BEADS_DIR is set")
	}

	// Test: getRoutedStoreForID should return nil when BEADS_DIR is set
	routedStore, err := getRoutedStoreForID(ctx, "gt-town-message")
	if err != nil {
		t.Fatalf("getRoutedStoreForID with BEADS_DIR failed: %v", err)
	}
	if routedStore != nil {
		t.Error("getRoutedStoreForID should return nil when BEADS_DIR is set")
		_ = routedStore.Close()
	}

	t.Log("BEADS_DIR override correctly skipped prefix routing (GH#663)")
}
