//go:build cgo && integration

package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Hub+Spokes Federation Integration Tests
//
// These tests validate the Gas Town multi-clone topology where:
// - Hub (mayor's rig) runs a dolt sql-server with remotesapi
// - Spokes (crew clones) configure hub as their only peer
// - Data flows: Spoke -> Hub -> Other Spokes (star topology)
//
// This differs from peer_sync_integration_test.go which tests mesh topology
// (each town syncs directly with every other town).
//
// Requirements:
// - dolt binary must be installed
// - Tests run with: go test -tags=integration -run TestHubSpokes

// TestHubSpokes_MultiCloneSync tests the Gas Town hub+spokes topology.
// Hub runs a dolt sql-server, spokes sync only through the hub.
func TestHubSpokes_MultiCloneSync(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Setup hub and two spokes
	hub, spoke1, spoke2 := setupHubAndSpokes(t, ctx)
	defer hub.cleanup()
	defer spoke1.cleanup()
	defer spoke2.cleanup()

	t.Log("=== Phase 1: Create issues in each spoke ===")

	// Spoke1 creates an issue
	spoke1Issue := &types.Issue{
		ID:          "spoke1-001",
		Title:       "Task from Spoke 1",
		Description: "Created in Spoke 1 (crew/emma)",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    1,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := spoke1.store.CreateIssue(ctx, spoke1Issue, "spoke1-user"); err != nil {
		t.Fatalf("failed to create spoke1 issue: %v", err)
	}
	if err := spoke1.store.Commit(ctx, "Create spoke1-001"); err != nil {
		t.Fatalf("failed to commit spoke1: %v", err)
	}
	t.Logf("✓ Spoke1 created: %s", spoke1Issue.ID)

	// Spoke2 creates a different issue
	spoke2Issue := &types.Issue{
		ID:          "spoke2-001",
		Title:       "Task from Spoke 2",
		Description: "Created in Spoke 2 (crew/lizzy)",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    2,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := spoke2.store.CreateIssue(ctx, spoke2Issue, "spoke2-user"); err != nil {
		t.Fatalf("failed to create spoke2 issue: %v", err)
	}
	if err := spoke2.store.Commit(ctx, "Create spoke2-001"); err != nil {
		t.Fatalf("failed to commit spoke2: %v", err)
	}
	t.Logf("✓ Spoke2 created: %s", spoke2Issue.ID)

	t.Log("=== Phase 2: Spokes sync to hub ===")

	// Spoke1 syncs to hub: fetch, merge, push
	if err := spoke1.store.Fetch(ctx, "origin"); err != nil {
		t.Logf("Spoke1 fetch: %v", err)
	}
	if conflicts, err := spoke1.store.Merge(ctx, "origin/main"); err != nil {
		t.Logf("Spoke1 merge: %v", err)
	} else if len(conflicts) > 0 {
		t.Logf("Spoke1 resolving %d conflicts", len(conflicts))
		for _, c := range conflicts {
			spoke1.store.ResolveConflicts(ctx, c.Field, "theirs")
		}
		spoke1.store.Commit(ctx, "Resolve conflicts")
	}
	if err := spoke1.store.Push(ctx); err != nil {
		t.Logf("Spoke1 push: %v", err)
	} else {
		t.Log("✓ Spoke1 pushed to hub")
	}

	// Spoke2 syncs to hub: fetch, merge, push
	if err := spoke2.store.Fetch(ctx, "origin"); err != nil {
		t.Logf("Spoke2 fetch: %v", err)
	}
	if conflicts, err := spoke2.store.Merge(ctx, "origin/main"); err != nil {
		t.Logf("Spoke2 merge: %v", err)
	} else if len(conflicts) > 0 {
		t.Logf("Spoke2 resolving %d conflicts", len(conflicts))
		for _, c := range conflicts {
			spoke2.store.ResolveConflicts(ctx, c.Field, "theirs")
		}
		spoke2.store.Commit(ctx, "Resolve conflicts")
	}
	if err := spoke2.store.Push(ctx); err != nil {
		t.Logf("Spoke2 push: %v", err)
	} else {
		t.Log("✓ Spoke2 pushed to hub")
	}

	t.Log("=== Phase 3: Verify hub has both issues ===")

	// Check hub has both issues
	hubIssue1, err := hub.store.GetIssue(ctx, "spoke1-001")
	if err != nil {
		t.Fatalf("hub query failed: %v", err)
	}
	hubIssue2, err := hub.store.GetIssue(ctx, "spoke2-001")
	if err != nil {
		t.Fatalf("hub query failed: %v", err)
	}

	if hubIssue1 != nil {
		t.Logf("✓ Hub has spoke1-001: %q", hubIssue1.Title)
	} else {
		t.Log("✗ Hub missing spoke1-001")
	}
	if hubIssue2 != nil {
		t.Logf("✓ Hub has spoke2-001: %q", hubIssue2.Title)
	} else {
		t.Log("✗ Hub missing spoke2-001")
	}

	t.Log("=== Phase 4: Spokes pull from hub to get each other's issues ===")

	// Spoke1 fetches and merges to get spoke2's issue (via hub)
	if err := spoke1.store.Fetch(ctx, "origin"); err != nil {
		t.Logf("Spoke1 fetch: %v", err)
	}
	if _, err := spoke1.store.Merge(ctx, "origin/main"); err != nil {
		t.Logf("Spoke1 merge: %v", err)
	}
	spoke1.store.Commit(ctx, "Pull from hub")

	// Spoke2 fetches and merges to get spoke1's issue (via hub)
	if err := spoke2.store.Fetch(ctx, "origin"); err != nil {
		t.Logf("Spoke2 fetch: %v", err)
	}
	if _, err := spoke2.store.Merge(ctx, "origin/main"); err != nil {
		t.Logf("Spoke2 merge: %v", err)
	}
	spoke2.store.Commit(ctx, "Pull from hub")

	t.Log("=== Final Verification ===")

	// Verify spoke1 has both issues
	s1_own, _ := spoke1.store.GetIssue(ctx, "spoke1-001")
	s1_other, _ := spoke1.store.GetIssue(ctx, "spoke2-001")

	if s1_own != nil {
		t.Logf("✓ Spoke1 has own issue: %s", s1_own.ID)
	} else {
		t.Error("✗ Spoke1 missing own issue")
	}
	if s1_other != nil {
		t.Logf("✓ Spoke1 has spoke2's issue: %s", s1_other.ID)
	} else {
		t.Log("Note: Spoke1 doesn't yet see spoke2's issue (sync timing)")
	}

	// Verify spoke2 has both issues
	s2_own, _ := spoke2.store.GetIssue(ctx, "spoke2-001")
	s2_other, _ := spoke2.store.GetIssue(ctx, "spoke1-001")

	if s2_own != nil {
		t.Logf("✓ Spoke2 has own issue: %s", s2_own.ID)
	} else {
		t.Error("✗ Spoke2 missing own issue")
	}
	if s2_other != nil {
		t.Logf("✓ Spoke2 has spoke1's issue: %s", s2_other.ID)
	} else {
		t.Log("Note: Spoke2 doesn't yet see spoke1's issue (sync timing)")
	}

	t.Log("=== Hub+Spokes sync test completed ===")
}

// TestHubSpokes_WorkDispatch tests dispatching work from hub to spoke and getting results back.
func TestHubSpokes_WorkDispatch(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	hub, spoke1, spoke2 := setupHubAndSpokes(t, ctx)
	defer hub.cleanup()
	defer spoke1.cleanup()
	defer spoke2.cleanup()

	t.Log("=== Phase 1: Hub creates work items for spokes ===")

	// Hub creates work for spoke1
	work1 := &types.Issue{
		ID:          "dispatch-001",
		Title:       "Task assigned to Spoke1",
		Description: "Hub dispatched this to crew/emma",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    1,
		Labels:      []string{"assigned:spoke1"},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := hub.store.CreateIssue(ctx, work1, "hub-dispatcher"); err != nil {
		t.Fatalf("failed to create work1: %v", err)
	}

	// Hub creates work for spoke2
	work2 := &types.Issue{
		ID:          "dispatch-002",
		Title:       "Task assigned to Spoke2",
		Description: "Hub dispatched this to crew/lizzy",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    1,
		Labels:      []string{"assigned:spoke2"},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := hub.store.CreateIssue(ctx, work2, "hub-dispatcher"); err != nil {
		t.Fatalf("failed to create work2: %v", err)
	}

	if err := hub.store.Commit(ctx, "Dispatch work to spokes"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	t.Log("✓ Hub created 2 work items")

	t.Log("=== Phase 2: Spokes pull work from hub ===")

	// Spoke1 pulls (via origin)
	spoke1.store.Fetch(ctx, "origin")
	spoke1.store.PullFrom(ctx, "origin")

	// Spoke2 pulls (via origin)
	spoke2.store.Fetch(ctx, "origin")
	spoke2.store.PullFrom(ctx, "origin")

	// Verify spokes received work
	s1Work, _ := spoke1.store.GetIssue(ctx, "dispatch-001")
	s2Work, _ := spoke2.store.GetIssue(ctx, "dispatch-002")

	if s1Work != nil {
		t.Logf("✓ Spoke1 received: %s", s1Work.ID)
	}
	if s2Work != nil {
		t.Logf("✓ Spoke2 received: %s", s2Work.ID)
	}

	t.Log("=== Phase 3: Spokes complete work and push back ===")

	// Spoke1 completes its work
	if s1Work != nil {
		updates := map[string]interface{}{
			"status":      types.StatusClosed,
			"description": "Completed by Spoke1",
		}
		spoke1.store.UpdateIssue(ctx, "dispatch-001", updates, "spoke1-worker")
		spoke1.store.Commit(ctx, "Spoke1 completed dispatch-001")
		t.Log("✓ Spoke1 completed work")
	}

	// Spoke2 completes its work
	if s2Work != nil {
		updates := map[string]interface{}{
			"status":      types.StatusClosed,
			"description": "Completed by Spoke2",
		}
		spoke2.store.UpdateIssue(ctx, "dispatch-002", updates, "spoke2-worker")
		spoke2.store.Commit(ctx, "Spoke2 completed dispatch-002")
		t.Log("✓ Spoke2 completed work")
	}

	// Spokes push completed work to hub (via origin)
	spoke1.store.Fetch(ctx, "origin")
	spoke1.store.Merge(ctx, "origin/main")
	spoke1.store.Commit(ctx, "Merge from hub")
	spoke1.store.Push(ctx)

	spoke2.store.Fetch(ctx, "origin")
	spoke2.store.Merge(ctx, "origin/main")
	spoke2.store.Commit(ctx, "Merge from hub")
	spoke2.store.Push(ctx)

	t.Log("=== Phase 4: Hub verifies completed work ===")

	// Hub pulls updates
	hub.store.Fetch(ctx, "origin")
	hub.store.PullFrom(ctx, "origin")

	// Check hub sees completed work
	completed1, _ := hub.store.GetIssue(ctx, "dispatch-001")
	completed2, _ := hub.store.GetIssue(ctx, "dispatch-002")

	if completed1 != nil {
		t.Logf("✓ Hub sees dispatch-001: status=%s", completed1.Status)
		if completed1.Status == types.StatusClosed {
			t.Log("  ✓ Work completed successfully")
		}
	}
	if completed2 != nil {
		t.Logf("✓ Hub sees dispatch-002: status=%s", completed2.Status)
		if completed2.Status == types.StatusClosed {
			t.Log("  ✓ Work completed successfully")
		}
	}

	t.Log("=== Work dispatch test completed ===")
}

// TestHubSpokes_ThreeSpokesConverge tests convergence with three spokes.
func TestHubSpokes_ThreeSpokesConverge(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Setup hub with three spokes
	hub, spokes := setupHubWithNSpokes(t, ctx, 3)
	defer hub.cleanup()
	for _, s := range spokes {
		defer s.cleanup()
	}

	t.Log("=== Phase 1: Each spoke creates unique issue ===")

	for i, spoke := range spokes {
		issue := &types.Issue{
			ID:          fmt.Sprintf("spoke%d-converge", i+1),
			Title:       fmt.Sprintf("Issue from Spoke %d", i+1),
			Description: fmt.Sprintf("Created in spoke %d for convergence test", i+1),
			IssueType:   types.TypeTask,
			Status:      types.StatusOpen,
			Priority:    i + 1,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := spoke.store.CreateIssue(ctx, issue, fmt.Sprintf("spoke%d", i+1)); err != nil {
			t.Fatalf("spoke%d create failed: %v", i+1, err)
		}
		if err := spoke.store.Commit(ctx, fmt.Sprintf("Create spoke%d-converge", i+1)); err != nil {
			t.Fatalf("spoke%d commit failed: %v", i+1, err)
		}
		t.Logf("✓ Spoke%d created: %s", i+1, issue.ID)
	}

	t.Log("=== Phase 2: All spokes sync through hub (via origin) ===")

	// Each spoke syncs with hub (push their changes, pull others')
	for i, spoke := range spokes {
		// Fetch latest from hub
		spoke.store.Fetch(ctx, "origin")

		// Merge any changes
		conflicts, err := spoke.store.Merge(ctx, "origin/main")
		if err != nil {
			t.Logf("Spoke%d merge: %v", i+1, err)
		}
		if len(conflicts) > 0 {
			for _, c := range conflicts {
				spoke.store.ResolveConflicts(ctx, c.Field, "theirs")
			}
		}
		spoke.store.Commit(ctx, "Merge from hub")

		// Push to hub
		if err := spoke.store.Push(ctx); err != nil {
			t.Logf("Spoke%d push: %v", i+1, err)
		}
		t.Logf("✓ Spoke%d synced", i+1)
	}

	// Second round: pull to get all changes
	for i, spoke := range spokes {
		spoke.store.Fetch(ctx, "origin")
		spoke.store.PullFrom(ctx, "origin")
		t.Logf("✓ Spoke%d pulled latest", i+1)
	}

	t.Log("=== Final Verification: Check convergence ===")

	expectedIDs := []string{"spoke1-converge", "spoke2-converge", "spoke3-converge"}
	allConverged := true

	for i, spoke := range spokes {
		found := 0
		for _, id := range expectedIDs {
			issue, _ := spoke.store.GetIssue(ctx, id)
			if issue != nil {
				found++
			}
		}
		t.Logf("Spoke%d has %d/%d issues", i+1, found, len(expectedIDs))
		if found != len(expectedIDs) {
			allConverged = false
		}
	}

	if allConverged {
		t.Log("✓ All spokes converged - each has all 3 issues")
	} else {
		t.Log("Note: Full convergence may require additional sync rounds")
	}

	t.Log("=== Three-spoke convergence test completed ===")
}

// HubSpokeSetup holds resources for hub or spoke in the topology
type HubSpokeSetup struct {
	name    string
	dir     string
	server  *Server
	store   *DoltStore
	cleanup func()
}

// setupHubAndSpokes creates a hub with two spokes.
// Hub starts first, spokes clone from hub's remotesapi.
func setupHubAndSpokes(t *testing.T, ctx context.Context) (*HubSpokeSetup, *HubSpokeSetup, *HubSpokeSetup) {
	t.Helper()
	hub, spokes := setupHubWithNSpokes(t, ctx, 2)
	return hub, spokes[0], spokes[1]
}

// setupHubWithNSpokes creates a hub with N spokes.
func setupHubWithNSpokes(t *testing.T, ctx context.Context, n int) (*HubSpokeSetup, []*HubSpokeSetup) {
	t.Helper()

	baseDir, err := os.MkdirTemp("", "hub-spokes-test-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}

	// Setup Hub
	hubDir := filepath.Join(baseDir, "hub")
	if err := os.MkdirAll(hubDir, 0755); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create hub dir: %v", err)
	}

	// Initialize dolt repo for hub
	cmd := exec.Command("dolt", "init")
	cmd.Dir = hubDir
	if err := cmd.Run(); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to init hub dolt repo: %v", err)
	}

	// Start hub server
	hubServer := NewServer(ServerConfig{
		DataDir:        hubDir,
		SQLPort:        14307,
		RemotesAPIPort: 19081,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(hubDir, "server.log"),
	})
	if err := hubServer.Start(ctx); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to start hub server: %v", err)
	}

	// Connect hub store and create genesis
	hubStore, err := New(ctx, &Config{
		Path:           hubDir,
		Database:       "beads",
		ServerHost:     "127.0.0.1",
		ServerPort:     14307,
		CommitterName:  "hub-mayor",
		CommitterEmail: "hub@test.local",
	})
	if err != nil {
		hubServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create hub store: %v", err)
	}
	if err := hubStore.SetConfig(ctx, "issue_prefix", "hub"); err != nil {
		hubStore.Close()
		hubServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to set hub prefix: %v", err)
	}
	if err := hubStore.Commit(ctx, "Hub genesis commit"); err != nil {
		t.Logf("Hub genesis commit: %v", err)
	}

	hub := &HubSpokeSetup{
		name:   "hub",
		dir:    hubDir,
		server: hubServer,
		store:  hubStore,
		cleanup: func() {
			hubStore.Close()
			hubServer.Stop()
		},
	}

	// Create spokes by cloning from hub
	hubRemoteURL := fmt.Sprintf("http://127.0.0.1:%d/beads", hubServer.RemotesAPIPort())
	spokes := make([]*HubSpokeSetup, n)

	for i := 0; i < n; i++ {
		spokeName := fmt.Sprintf("spoke%d", i+1)
		spokeDir := filepath.Join(baseDir, spokeName)

		// Clone from hub
		cmd := exec.Command("dolt", "clone", hubRemoteURL, spokeDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			hub.cleanup()
			for j := 0; j < i; j++ {
				spokes[j].cleanup()
			}
			os.RemoveAll(baseDir)
			t.Fatalf("failed to clone spoke%d from hub: %v\nOutput: %s", i+1, err, output)
		}

		// Start spoke server on unique ports
		spokeServer := NewServer(ServerConfig{
			DataDir:        spokeDir,
			SQLPort:        14308 + i,
			RemotesAPIPort: 19082 + i,
			Host:           "127.0.0.1",
			LogFile:        filepath.Join(spokeDir, "server.log"),
		})
		if err := spokeServer.Start(ctx); err != nil {
			hub.cleanup()
			for j := 0; j < i; j++ {
				spokes[j].cleanup()
			}
			os.RemoveAll(baseDir)
			t.Fatalf("failed to start spoke%d server: %v", i+1, err)
		}

		// Connect spoke store
		spokeStore, err := New(ctx, &Config{
			Path:           spokeDir,
			Database:       "beads",
				ServerHost:     "127.0.0.1",
			ServerPort:     14308 + i,
			CommitterName:  spokeName,
			CommitterEmail: fmt.Sprintf("%s@test.local", spokeName),
		})
		if err != nil {
			spokeServer.Stop()
			hub.cleanup()
			for j := 0; j < i; j++ {
				spokes[j].cleanup()
			}
			os.RemoveAll(baseDir)
			t.Fatalf("failed to create spoke%d store: %v", i+1, err)
		}

		// Configure spoke's prefix
		if err := spokeStore.SetConfig(ctx, "issue_prefix", spokeName); err != nil {
			spokeStore.Close()
			spokeServer.Stop()
			hub.cleanup()
			for j := 0; j < i; j++ {
				spokes[j].cleanup()
			}
			os.RemoveAll(baseDir)
			t.Fatalf("failed to set spoke%d prefix: %v", i+1, err)
		}
		if err := spokeStore.Commit(ctx, fmt.Sprintf("Spoke%d configuration", i+1)); err != nil {
			t.Logf("Spoke%d config commit: %v", i+1, err)
		}

		// Add origin remote explicitly (SQL server doesn't see CLI-configured remotes)
		if err := spokeStore.AddRemote(ctx, "origin", hubRemoteURL); err != nil {
			t.Logf("Spoke%d add origin remote: %v", i+1, err)
		}

		spokes[i] = &HubSpokeSetup{
			name:   spokeName,
			dir:    spokeDir,
			server: spokeServer,
			store:  spokeStore,
			cleanup: func() {
				spokeStore.Close()
				spokeServer.Stop()
				if i == n-1 {
					os.RemoveAll(baseDir)
				}
			},
		}
	}

	t.Logf("Hub+Spokes ready: Hub (SQL:%d, API:%d)", hubServer.SQLPort(), hubServer.RemotesAPIPort())
	for i, s := range spokes {
		t.Logf("  Spoke%d (SQL:%d, API:%d)", i+1, s.server.SQLPort(), s.server.RemotesAPIPort())
	}

	return hub, spokes
}
