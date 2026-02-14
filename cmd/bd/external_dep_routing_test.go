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

// TestResolveExternalDepsViaRouting tests that external dependency references
// (e.g., "external:gastown:gt-42zaq") are resolved via prefix routes to show
// the actual issue details from the target database.
//
// This is the core fix for bd-k0pfm: bd show should resolve cross-database deps.
func TestResolveExternalDepsViaRouting(t *testing.T) {
	ctx := context.Background()

	// Create temp directory structure:
	// tmpDir/
	//   .beads/
	//     beads.db (town database - local)
	//     routes.jsonl
	//   rig/
	//     .beads/
	//       beads.db (remote rig database)
	tmpDir := t.TempDir()

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	// Initialize town database (local store)
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	// Initialize rig database (remote store)
	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create an issue in the remote rig database
	remoteIssue := &types.Issue{
		ID:        "gt-remote1",
		Title:     "Remote issue for cross-db dep test",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
	}
	if err := rigStore.CreateIssue(ctx, remoteIssue, "test"); err != nil {
		t.Fatalf("Failed to create remote issue: %v", err)
	}

	// Close rig store to release lock before routing opens it
	if closer, ok := rigStore.(io.Closer); ok {
		closer.Close()
	}

	// Create a local issue that depends on the remote issue via external ref
	localIssue := &types.Issue{
		ID:        "hq-local1",
		Title:     "Local issue with external dep",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
	}
	if err := townStore.CreateIssue(ctx, localIssue, "test"); err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}

	// Add external dependency: local issue depends on "external:rig:gt-remote1"
	dep := &types.Dependency{
		IssueID:     "hq-local1",
		DependsOnID: "external:rig:gt-remote1",
		Type:        types.DepBlocks,
	}
	if err := townStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add external dependency: %v", err)
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

	oldStore := store
	store = townStore
	t.Cleanup(func() { store = oldStore })

	// Change to tmpDir so routing can find town root via CWD
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Verify: GetDependenciesWithMetadata should NOT return the external dep
	// (because it JOINs on issues table and external refs aren't there)
	depsWithMeta, err := townStore.GetDependenciesWithMetadata(ctx, "hq-local1")
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata failed: %v", err)
	}
	if len(depsWithMeta) != 0 {
		t.Errorf("Expected 0 deps from GetDependenciesWithMetadata (external ref not in local issues table), got %d", len(depsWithMeta))
	}

	// Verify: resolveExternalDepsViaRouting SHOULD resolve the external dep
	resolved, err := resolveExternalDepsViaRouting(ctx, townStore, "hq-local1")
	if err != nil {
		t.Fatalf("resolveExternalDepsViaRouting failed: %v", err)
	}

	if len(resolved) != 1 {
		t.Fatalf("Expected 1 resolved external dep, got %d", len(resolved))
	}

	resolvedDep := resolved[0]
	if resolvedDep.Issue.ID != "gt-remote1" {
		t.Errorf("Expected resolved dep ID %q, got %q", "gt-remote1", resolvedDep.Issue.ID)
	}
	if resolvedDep.Issue.Title != "Remote issue for cross-db dep test" {
		t.Errorf("Expected resolved dep title %q, got %q", "Remote issue for cross-db dep test", resolvedDep.Issue.Title)
	}
	if resolvedDep.DependencyType != types.DepBlocks {
		t.Errorf("Expected dependency type %q, got %q", types.DepBlocks, resolvedDep.DependencyType)
	}

	t.Logf("Successfully resolved external dep: %s -> %s: %s",
		localIssue.ID, resolvedDep.Issue.ID, resolvedDep.Issue.Title)
}

// TestResolveExternalDepsUnresolvable tests that unresolvable external deps
// produce placeholder entries rather than being silently dropped.
func TestResolveExternalDepsUnresolvable(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	// Create a local issue with an external dep to a non-existent target
	localIssue := &types.Issue{
		ID:        "hq-orphan1",
		Title:     "Issue with unresolvable external dep",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
	}
	if err := townStore.CreateIssue(ctx, localIssue, "test"); err != nil {
		t.Fatalf("Failed to create local issue: %v", err)
	}

	dep := &types.Dependency{
		IssueID:     "hq-orphan1",
		DependsOnID: "external:nonexistent:fake-id",
		Type:        types.DepBlocks,
	}
	if err := townStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add external dependency: %v", err)
	}

	// Set up global state
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	oldStore := store
	store = townStore
	t.Cleanup(func() { store = oldStore })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Should produce a placeholder for the unresolvable dep
	resolved, err := resolveExternalDepsViaRouting(ctx, townStore, "hq-orphan1")
	if err != nil {
		t.Fatalf("resolveExternalDepsViaRouting failed: %v", err)
	}

	if len(resolved) != 1 {
		t.Fatalf("Expected 1 placeholder dep, got %d", len(resolved))
	}

	placeholder := resolved[0]
	if placeholder.Issue.ID != "external:nonexistent:fake-id" {
		t.Errorf("Expected placeholder ID to be the external ref, got %q", placeholder.Issue.ID)
	}
	if !strings.Contains(placeholder.Issue.Title, "unresolved") {
		t.Errorf("Expected placeholder title to contain 'unresolved', got %q", placeholder.Issue.Title)
	}
}

// TestResolveBlockedByRefs tests that blocked-by lists with external refs
// are resolved to human-readable strings.
func TestResolveBlockedByRefs(t *testing.T) {
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
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	remoteIssue := &types.Issue{
		ID:        "gt-blocker1",
		Title:     "Blocking issue in remote rig",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  0,
	}
	if err := rigStore.CreateIssue(ctx, remoteIssue, "test"); err != nil {
		t.Fatalf("Failed to create remote issue: %v", err)
	}
	if closer, ok := rigStore.(io.Closer); ok {
		closer.Close()
	}

	routesContent := `{"prefix":"gt-","path":"rig"}`
	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	oldStore := store
	store = townStore
	t.Cleanup(func() { store = oldStore })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Test with mixed refs
	refs := []string{"hq-local1", "external:rig:gt-blocker1"}
	resolved := resolveBlockedByRefs(ctx, refs)

	if len(resolved) != 2 {
		t.Fatalf("Expected 2 resolved refs, got %d", len(resolved))
	}

	// First ref should pass through unchanged
	if resolved[0] != "hq-local1" {
		t.Errorf("Expected first ref to be %q, got %q", "hq-local1", resolved[0])
	}

	// Second ref should be resolved to show issue ID and title
	if !strings.Contains(resolved[1], "gt-blocker1") {
		t.Errorf("Expected second ref to contain issue ID 'gt-blocker1', got %q", resolved[1])
	}
	if !strings.Contains(resolved[1], "Blocking issue in remote rig") {
		t.Errorf("Expected second ref to contain issue title, got %q", resolved[1])
	}
}

// TestNoExternalDeps verifies that resolveExternalDepsViaRouting returns nil
// when there are no external deps (common case - no performance overhead).
func TestNoExternalDeps(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	dbFile := filepath.Join(beadsDir, "beads.db")
	testStore := newTestStoreWithPrefix(t, dbFile, "test")

	// Create two local issues with a local dependency
	issue1 := &types.Issue{
		ID:        "test-a",
		Title:     "Issue A",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	issue2 := &types.Issue{
		ID:        "test-b",
		Title:     "Issue B",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := testStore.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := testStore.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatal(err)
	}
	dep := &types.Dependency{
		IssueID:     "test-a",
		DependsOnID: "test-b",
		Type:        types.DepBlocks,
	}
	if err := testStore.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatal(err)
	}

	oldDbPath := dbPath
	dbPath = dbFile
	t.Cleanup(func() { dbPath = oldDbPath })

	// No external deps — should return nil quickly
	resolved, err := resolveExternalDepsViaRouting(ctx, testStore, "test-a")
	if err != nil {
		t.Fatalf("resolveExternalDepsViaRouting failed: %v", err)
	}
	if resolved != nil {
		t.Errorf("Expected nil for no external deps, got %d entries", len(resolved))
	}
}
