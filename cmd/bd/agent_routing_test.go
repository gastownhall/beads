//go:build cgo

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	// Close rig store to release Dolt lock before routing opens it
	if closer, ok := rigStore.(io.Closer); ok {
		closer.Close()
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

// TestUpdateClaimUsesCASOnRoutedIssue verifies routed-ID update --claim follows
// storage CAS semantics (first claim succeeds, second claim fails).
//
// Regression coverage for GH#1522.
func TestUpdateClaimUsesCASOnRoutedIssue(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatalf("create rig beads dir: %v", err)
	}

	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	_ = newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")
	issue := &types.Issue{
		ID:        "gt-claim-cas-1",
		Title:     "Claim CAS routed issue",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := rigStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("create routed issue: %v", err)
	}
	if closer, ok := rigStore.(io.Closer); ok {
		_ = closer.Close()
	}

	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir tmp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	routed, err := resolveAndGetIssueWithRouting(ctx, newTestStoreWithPrefix(t, townDBPath, "hq"), issue.ID)
	if err != nil {
		t.Fatalf("resolve routed issue: %v", err)
	}
	if routed == nil {
		t.Fatalf("expected routed result for %s", issue.ID)
	}
	defer routed.Close()
	if !routed.Routed {
		t.Fatalf("expected routed lookup for %s", issue.ID)
	}

	if err := routed.Store.ClaimIssue(ctx, routed.ResolvedID, "actor-a"); err != nil {
		t.Fatalf("first ClaimIssue failed: %v", err)
	}

	firstClaimed, err := routed.Store.GetIssue(ctx, routed.ResolvedID)
	if err != nil {
		t.Fatalf("get issue after first claim: %v", err)
	}
	if firstClaimed == nil {
		t.Fatalf("issue %s not found after first claim", issue.ID)
	}
	if firstClaimed.Assignee == "" {
		t.Fatalf("expected assignee to be set after first claim")
	}
	if firstClaimed.Status != types.StatusInProgress {
		t.Fatalf("status after first claim = %q, want %q", firstClaimed.Status, types.StatusInProgress)
	}
	initialAssignee := firstClaimed.Assignee
	err = routed.Store.ClaimIssue(ctx, routed.ResolvedID, "actor-b")
	if err == nil {
		t.Fatalf("second ClaimIssue should fail for already-claimed issue")
	}
	if !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("second claim error = %q, want contains %q", err.Error(), "already claimed")
	}

	secondClaimed, err := routed.Store.GetIssue(ctx, routed.ResolvedID)
	if err != nil {
		t.Fatalf("get issue after second claim: %v", err)
	}
	if secondClaimed == nil {
		t.Fatalf("issue %s not found after second claim", issue.ID)
	}
	if secondClaimed.Assignee != initialAssignee {
		t.Fatalf("assignee after failed second claim = %q, want %q", secondClaimed.Assignee, initialAssignee)
	}
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

	// Close rig store to release Dolt lock before routing opens it
	if closer, ok := rigStore.(io.Closer); ok {
		closer.Close()
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

	// Close rig store to release Dolt lock before routing opens it
	if closer, ok := rigStore.(io.Closer); ok {
		closer.Close()
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

// TestSlotClearWithRouting tests that bd slot clear resolves agent beads via routing.
// This is the fix for bd-yw7ui: gt unsling fails with 'issue not found' because
// bd slot clear didn't use routing for agent bead resolution.
func TestSlotClearWithRouting(t *testing.T) {
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

	// Initialize databases
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	_ = newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create an agent bead in the rig database with a hook set
	agentBead := &types.Issue{
		ID:        "gt-testrig-polecat-slottest",
		Title:     "Agent: gt-testrig-polecat-slottest",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		HookBead:  "gt-some-old-bead",
	}
	if err := rigStore.CreateIssue(ctx, agentBead, "test"); err != nil {
		t.Fatalf("Failed to create agent bead: %v", err)
	}
	if err := rigStore.AddLabel(ctx, agentBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Close rig store to release lock before routing opens it
	if closer, ok := rigStore.(io.Closer); ok {
		closer.Close()
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

	oldStore := store
	store = newTestStoreWithPrefix(t, townDBPath, "hq")
	t.Cleanup(func() { store = oldStore })

	oldCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = oldCtx })

	oldActor := actor
	actor = "test"
	t.Cleanup(func() { actor = oldActor })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Test: bd slot clear should find the agent via routing and clear the hook
	err = runSlotClear(nil, []string{"gt-testrig-polecat-slottest", "hook"})
	if err != nil {
		t.Fatalf("runSlotClear failed: %v", err)
	}

	// Verify the hook was cleared by reading the agent bead from the rig database
	rigStore2 := newTestStoreWithPrefix(t, rigDBPath, "gt")
	defer func() {
		if closer, ok := rigStore2.(io.Closer); ok {
			closer.Close()
		}
	}()

	updated, err := rigStore2.GetIssue(ctx, "gt-testrig-polecat-slottest")
	if err != nil {
		t.Fatalf("Failed to get updated agent bead: %v", err)
	}
	if updated.HookBead != "" {
		t.Errorf("Expected hook_bead to be cleared, got %q", updated.HookBead)
	}
}

// TestSlotShowWithRouting tests that bd slot show resolves agent beads via routing.
func TestSlotShowWithRouting(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	_ = newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	agentBead := &types.Issue{
		ID:        "gt-testrig-crew-showtest",
		Title:     "Agent: gt-testrig-crew-showtest",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		HookBead:  "gt-some-work",
		RoleBead:  "gt-some-role",
	}
	if err := rigStore.CreateIssue(ctx, agentBead, "test"); err != nil {
		t.Fatalf("Failed to create agent bead: %v", err)
	}
	if err := rigStore.AddLabel(ctx, agentBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	if closer, ok := rigStore.(io.Closer); ok {
		closer.Close()
	}

	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"gt-","path":"rig"}`), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	oldStore := store
	store = newTestStoreWithPrefix(t, townDBPath, "hq")
	t.Cleanup(func() { store = oldStore })

	oldCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = oldCtx })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Test: bd slot show should find the agent via routing (no error)
	err = runSlotShow(nil, []string{"gt-testrig-crew-showtest"})
	if err != nil {
		t.Fatalf("runSlotShow failed: %v", err)
	}
}

// TestSlotAgentLabelCheck tests that slot commands accept agent beads with gt:agent
// label regardless of issue_type (agents may have type=task, not type=agent).
func TestSlotAgentLabelCheck(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	testDBPath := filepath.Join(beadsDir, "beads.db")
	testStore := newTestStoreWithPrefix(t, testDBPath, "bd")

	// Create an agent bead with type=task (NOT type=agent) but with gt:agent label
	agentBead := &types.Issue{
		ID:        "bd-label-agent-test",
		Title:     "Agent: bd-label-agent-test",
		IssueType: types.TypeTask, // Not "agent" - this is the new pattern
		Status:    types.StatusOpen,
		HookBead:  "bd-some-work",
	}
	if err := testStore.CreateIssue(ctx, agentBead, "test"); err != nil {
		t.Fatalf("Failed to create agent bead: %v", err)
	}
	if err := testStore.AddLabel(ctx, agentBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Set dbPath to empty to skip routing - this test is about label checking, not routing
	oldDbPath := dbPath
	dbPath = ""
	t.Cleanup(func() { dbPath = oldDbPath })

	oldStore := store
	store = testStore
	t.Cleanup(func() { store = oldStore })

	oldCtx := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = oldCtx })

	oldActor := actor
	actor = "test"
	t.Cleanup(func() { actor = oldActor })

	// slot clear should succeed even though IssueType is "task" (not "agent")
	err := runSlotClear(nil, []string{"bd-label-agent-test", "hook"})
	if err != nil {
		t.Fatalf("runSlotClear should accept task-type agent with gt:agent label, got: %v", err)
	}

	// Verify hook was cleared
	updated, err := testStore.GetIssue(ctx, "bd-label-agent-test")
	if err != nil {
		t.Fatalf("Failed to get updated agent bead: %v", err)
	}
	if updated.HookBead != "" {
		t.Errorf("Expected hook_bead to be cleared, got %q", updated.HookBead)
	}

	// slot show should also work
	err = runSlotShow(nil, []string{"bd-label-agent-test"})
	if err != nil {
		t.Fatalf("runSlotShow should accept task-type agent with gt:agent label, got: %v", err)
	}
}
