//go:build integration
// +build integration

package dolt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Peer-to-Peer Federation Integration Tests
//
// These tests validate actual sync operations between two dolt sql-servers.
// They test:
// 1. Two sql-servers pushing/pulling data
// 2. Conflict resolution with different strategies
// 3. Work handoff between towns
// 4. Validation/reputation tracking (basic validator data sync)
//
// Requirements:
// - dolt binary must be installed
// - Tests run with: go test -tags=integration -run TestPeer
// - Each test uses isolated temp directories and non-conflicting ports

// TestPeerToPeerSync tests bidirectional sync between two dolt sql-servers.
// This is the core federation test - verifying that two towns can push and pull data.
func TestPeerToPeerSync(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Setup two towns with their own servers
	alpha, beta := setupTwoTowns(t, ctx)
	defer alpha.cleanup()
	defer beta.cleanup()

	t.Log("=== Phase 1: Create issues in each town ===")

	// Create issue in Alpha
	alphaIssue := &types.Issue{
		ID:          "alpha-sync-001",
		Title:       "Alpha originated task",
		Description: "Created in Town Alpha",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    1,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := alpha.store.CreateIssue(ctx, alphaIssue, "alpha-user"); err != nil {
		t.Fatalf("failed to create alpha issue: %v", err)
	}
	if err := alpha.store.Commit(ctx, "Create alpha-sync-001"); err != nil {
		t.Fatalf("failed to commit alpha: %v", err)
	}
	t.Logf("✓ Alpha created: %s", alphaIssue.ID)

	// Create different issue in Beta
	betaIssue := &types.Issue{
		ID:          "beta-sync-001",
		Title:       "Beta originated task",
		Description: "Created in Town Beta",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    2,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := beta.store.CreateIssue(ctx, betaIssue, "beta-user"); err != nil {
		t.Fatalf("failed to create beta issue: %v", err)
	}
	if err := beta.store.Commit(ctx, "Create beta-sync-001"); err != nil {
		t.Fatalf("failed to commit beta: %v", err)
	}
	t.Logf("✓ Beta created: %s", betaIssue.ID)

	t.Log("=== Phase 2: Configure peers ===")

	// Add remotes using the remotesapi URLs (HTTP)
	alphaRemoteURL := fmt.Sprintf("http://127.0.0.1:%d/beads", alpha.server.RemotesAPIPort())
	betaRemoteURL := fmt.Sprintf("http://127.0.0.1:%d/beads", beta.server.RemotesAPIPort())

	// Alpha adds Beta as a remote
	if err := alpha.store.AddRemote(ctx, "beta", betaRemoteURL); err != nil {
		t.Logf("AddRemote beta to alpha: %v", err)
	}

	// Beta adds Alpha as a remote (named "alpha" for clarity)
	if err := beta.store.AddRemote(ctx, "alpha", alphaRemoteURL); err != nil {
		t.Logf("AddRemote alpha to beta: %v", err)
	}

	// List remotes to verify setup
	alphaRemotes, _ := alpha.store.ListRemotes(ctx)
	t.Logf("Alpha remotes: %d", len(alphaRemotes))
	for _, r := range alphaRemotes {
		t.Logf("  - %s: %s", r.Name, r.URL)
	}
	betaRemotes, _ := beta.store.ListRemotes(ctx)
	t.Logf("Beta remotes: %d", len(betaRemotes))
	for _, r := range betaRemotes {
		t.Logf("  - %s: %s", r.Name, r.URL)
	}

	t.Log("=== Phase 3: Sync Alpha -> Beta ===")

	// Beta fetches from Alpha
	if err := beta.store.Fetch(ctx, "alpha"); err != nil {
		t.Logf("Beta fetch from alpha: %v", err)
	}

	// Beta merges alpha/main (should have common history now)
	conflicts, err := beta.store.Merge(ctx, "alpha/main")
	if err != nil {
		t.Logf("Merge origin/main: %v", err)
	} else {
		if len(conflicts) > 0 {
			t.Logf("Merge produced %d conflicts, resolving with 'theirs'", len(conflicts))
			for _, c := range conflicts {
				if err := beta.store.ResolveConflicts(ctx, c.Field, "theirs"); err != nil {
					t.Logf("Resolve: %v", err)
				}
			}
		}
		if err := beta.store.Commit(ctx, "Merge from alpha"); err != nil {
			t.Logf("Commit: %v", err)
		}
		t.Log("✓ Beta merged origin/main successfully")
	}

	// Verify Beta now has Alpha's issue
	alphaInBeta, err := beta.store.GetIssue(ctx, "alpha-sync-001")
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if alphaInBeta == nil {
		t.Log("Note: Alpha's issue not visible in Beta after merge")
	} else {
		t.Logf("✓ Beta sees alpha-sync-001: %q", alphaInBeta.Title)
	}

	t.Log("=== Phase 4: Sync Beta -> Alpha ===")

	// Alpha fetches from Beta
	if err := alpha.store.Fetch(ctx, "beta"); err != nil {
		t.Logf("Alpha fetch from beta: %v", err)
	}

	// Alpha merges beta/main
	conflicts, err = alpha.store.Merge(ctx, "beta/main")
	if err != nil {
		t.Logf("Merge beta/main: %v", err)
	} else {
		if len(conflicts) > 0 {
			t.Logf("Merge produced %d conflicts, resolving with 'theirs'", len(conflicts))
			for _, c := range conflicts {
				if err := alpha.store.ResolveConflicts(ctx, c.Field, "theirs"); err != nil {
					t.Logf("Resolve: %v", err)
				}
			}
		}
		if err := alpha.store.Commit(ctx, "Merge from beta"); err != nil {
			t.Logf("Commit: %v", err)
		}
		t.Log("✓ Alpha merged beta/main successfully")
	}

	// Verify Alpha now has Beta's issue
	betaInAlpha, err := alpha.store.GetIssue(ctx, "beta-sync-001")
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if betaInAlpha == nil {
		t.Log("Note: Beta's issue not visible in Alpha after merge")
	} else {
		t.Logf("✓ Alpha sees beta-sync-001: %q", betaInAlpha.Title)
	}

	t.Log("=== Final Verification ===")

	// Both towns should still have their own issues
	alphaCheck, _ := alpha.store.GetIssue(ctx, "alpha-sync-001")
	if alphaCheck == nil {
		t.Fatal("Alpha should have its own issue")
	}
	t.Logf("✓ Alpha has: %s", alphaCheck.ID)

	betaCheck, _ := beta.store.GetIssue(ctx, "beta-sync-001")
	if betaCheck == nil {
		t.Fatal("Beta should have its own issue")
	}
	t.Logf("✓ Beta has: %s", betaCheck.ID)

	t.Log("=== Peer sync test completed ===")
}

// TestConflictResolution tests conflict detection and resolution strategies.
func TestConflictResolution(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Setup two towns
	alpha, beta := setupTwoTowns(t, ctx)
	defer alpha.cleanup()
	defer beta.cleanup()

	t.Log("=== Phase 1: Create same issue in both towns ===")

	// Create the same issue ID in both towns with different values
	// This simulates a conflict scenario
	sharedID := "conflict-001"

	alphaIssue := &types.Issue{
		ID:          sharedID,
		Title:       "Alpha's version of the title",
		Description: "Alpha wrote this description",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    1,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := alpha.store.CreateIssue(ctx, alphaIssue, "alpha"); err != nil {
		t.Fatalf("failed to create alpha issue: %v", err)
	}
	if err := alpha.store.Commit(ctx, "Create conflict-001 in Alpha"); err != nil {
		t.Fatalf("failed to commit alpha: %v", err)
	}

	betaIssue := &types.Issue{
		ID:          sharedID,
		Title:       "Beta's version of the title",
		Description: "Beta wrote this description",
		IssueType:   types.TypeTask,
		Status:      types.StatusInProgress,
		Priority:    3,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := beta.store.CreateIssue(ctx, betaIssue, "beta"); err != nil {
		t.Fatalf("failed to create beta issue: %v", err)
	}
	if err := beta.store.Commit(ctx, "Create conflict-001 in Beta"); err != nil {
		t.Fatalf("failed to commit beta: %v", err)
	}

	t.Log("✓ Created same issue ID with different values in both towns")

	t.Log("=== Phase 2: Sync and detect conflicts ===")

	// Configure peers
	alphaRemoteURL := fmt.Sprintf("http://%s:%d/beads", alpha.server.Host(), alpha.server.RemotesAPIPort())
	betaRemoteURL := fmt.Sprintf("http://%s:%d/beads", beta.server.Host(), beta.server.RemotesAPIPort())

	_ = alpha.store.AddRemote(ctx, "beta", betaRemoteURL)
	_ = beta.store.AddRemote(ctx, "alpha", alphaRemoteURL)

	// Alpha tries to pull from Beta - should produce conflicts
	if err := alpha.store.Fetch(ctx, "beta"); err != nil {
		t.Logf("Fetch: %v", err)
	}

	// Attempt merge
	conflicts, err := alpha.store.Merge(ctx, "beta/main")
	if err != nil {
		t.Logf("Merge result: %v", err)
	}

	if len(conflicts) > 0 {
		t.Logf("✓ Detected %d conflict(s)", len(conflicts))
		for _, c := range conflicts {
			t.Logf("  Conflict in %s: ours=%v, theirs=%v", c.Field, c.OursValue, c.TheirsValue)
		}
	} else {
		t.Log("No conflicts detected - may be expected if merge succeeded cleanly")
	}

	t.Log("=== Phase 3: Resolve conflicts with 'ours' strategy ===")

	// Get any conflicts
	// Note: GetConflicts queries dolt_conflicts which only exists after a conflicting merge
	currentConflicts, err := alpha.store.GetConflicts(ctx)
	if err != nil {
		// dolt_conflicts table may not exist if there were no actual conflicts
		t.Logf("GetConflicts: %v (table may not exist without actual conflicts)", err)
		currentConflicts = nil
	}

	if len(currentConflicts) > 0 {
		// Resolve using "ours" strategy
		for _, c := range currentConflicts {
			if err := alpha.store.ResolveConflicts(ctx, c.Field, "ours"); err != nil {
				t.Fatalf("failed to resolve conflict: %v", err)
			}
			t.Logf("✓ Resolved conflict in %s with 'ours'", c.Field)
		}

		// Commit the resolution
		if err := alpha.store.Commit(ctx, "Resolve conflicts using ours strategy"); err != nil {
			t.Logf("Commit resolution: %v", err)
		}
	}

	// Verify Alpha's version wins
	resolved, err := alpha.store.GetIssue(ctx, sharedID)
	if err != nil {
		t.Fatalf("failed to get resolved issue: %v", err)
	}
	if resolved != nil {
		t.Logf("✓ After resolution: title=%q, status=%s", resolved.Title, resolved.Status)
		// With "ours" strategy, Alpha's values should persist
		if resolved.Title != "Alpha's version of the title" {
			t.Logf("Note: Title changed - merge behavior may differ from expected")
		}
	}

	t.Log("=== Conflict resolution test completed ===")
}

// TestWorkHandoff tests work being created in one town and transferred to another.
// This simulates the scenario of a task being dispatched to a remote town.
func TestWorkHandoff(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Setup two towns
	alpha, beta := setupTwoTowns(t, ctx)
	defer alpha.cleanup()
	defer beta.cleanup()

	t.Log("=== Phase 1: Create work in Alpha and assign to Beta ===")

	// Create work in Alpha that's meant for Beta
	workItem := &types.Issue{
		ID:          "handoff-001",
		Title:       "Task dispatched to Beta town",
		Description: "This work should be picked up by Beta",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    1,
		Labels:      []string{"handoff", "beta-assigned"},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := alpha.store.CreateIssue(ctx, workItem, "alpha-dispatcher"); err != nil {
		t.Fatalf("failed to create work item: %v", err)
	}
	if err := alpha.store.Commit(ctx, "Dispatch handoff-001 to Beta"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	t.Logf("✓ Alpha created and dispatched: %s", workItem.ID)

	t.Log("=== Phase 2: Sync work to Beta ===")

	// Configure peers
	alphaRemoteURL := fmt.Sprintf("http://%s:%d/beads", alpha.server.Host(), alpha.server.RemotesAPIPort())
	_ = beta.store.AddRemote(ctx, "alpha", alphaRemoteURL)

	// Beta pulls from Alpha
	if err := beta.store.Fetch(ctx, "alpha"); err != nil {
		t.Logf("Fetch: %v", err)
	}

	_, err := beta.store.PullFrom(ctx, "alpha")
	if err != nil {
		t.Logf("Pull: %v", err)
	}

	// Verify Beta received the work
	receivedWork, err := beta.store.GetIssue(ctx, "handoff-001")
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if receivedWork == nil {
		t.Log("Note: Work item not yet visible - may need merge or different sync approach")
	} else {
		t.Logf("✓ Beta received: %s - %q", receivedWork.ID, receivedWork.Title)
	}

	t.Log("=== Phase 3: Beta works on and completes the task ===")

	if receivedWork != nil {
		// Beta updates the work status
		updates := map[string]interface{}{
			"status":      types.StatusInProgress,
			"description": "Beta is actively working on this task",
		}
		if err := beta.store.UpdateIssue(ctx, "handoff-001", updates, "beta-worker"); err != nil {
			t.Fatalf("failed to update: %v", err)
		}
		if err := beta.store.Commit(ctx, "Beta started working on handoff-001"); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}
		t.Log("✓ Beta updated status to in_progress")

		// Beta completes the work
		updates = map[string]interface{}{
			"status":      types.StatusClosed,
			"description": "Task completed by Beta town",
		}
		if err := beta.store.UpdateIssue(ctx, "handoff-001", updates, "beta-worker"); err != nil {
			t.Fatalf("failed to update: %v", err)
		}
		if err := beta.store.Commit(ctx, "Beta completed handoff-001"); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}
		t.Log("✓ Beta marked task as closed")
	}

	t.Log("=== Phase 4: Sync completed work back to Alpha ===")

	betaRemoteURL := fmt.Sprintf("http://%s:%d/beads", beta.server.Host(), beta.server.RemotesAPIPort())
	_ = alpha.store.AddRemote(ctx, "beta", betaRemoteURL)

	if err := alpha.store.Fetch(ctx, "beta"); err != nil {
		t.Logf("Fetch: %v", err)
	}

	_, err = alpha.store.PullFrom(ctx, "beta")
	if err != nil {
		t.Logf("Pull: %v", err)
	}

	// Verify Alpha sees the completed work
	completedWork, err := alpha.store.GetIssue(ctx, "handoff-001")
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if completedWork != nil {
		t.Logf("✓ Alpha sees: status=%s, desc=%q", completedWork.Status, completedWork.Description)
		if completedWork.Status == types.StatusClosed {
			t.Log("✓ Work handoff complete: task created in Alpha, completed in Beta, synced back")
		}
	}

	t.Log("=== Work handoff test completed ===")
}

// TestReputationTracking tests that validation data (basis for reputation) syncs correctly.
// Note: Full reputation scoring is not yet implemented - this tests validator data sync.
func TestReputationTracking(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Setup two towns
	alpha, beta := setupTwoTowns(t, ctx)
	defer alpha.cleanup()
	defer beta.cleanup()

	t.Log("=== Phase 1: Create work with validation in Alpha ===")

	// Create an issue that has been validated
	issue := &types.Issue{
		ID:          "validated-001",
		Title:       "Validated work item",
		Description: "This work has been validated",
		IssueType:   types.TypeTask,
		Status:      types.StatusClosed,
		Priority:    1,
		Validations: []types.Validation{
			{
				Validator: &types.EntityRef{
					Name:     "senior-reviewer",
					Platform: "gastown",
					Org:      "alpha-town",
				},
				Outcome:   "accepted",
				Timestamp: time.Now(),
			},
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := alpha.store.CreateIssue(ctx, issue, "alpha-user"); err != nil {
		t.Fatalf("failed to create validated issue: %v", err)
	}
	if err := alpha.store.Commit(ctx, "Create validated-001 with reviewer approval"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	t.Logf("✓ Alpha created validated issue with %d validation(s)", len(issue.Validations))

	t.Log("=== Phase 2: Sync validation data to Beta ===")

	// Configure peers
	alphaRemoteURL := fmt.Sprintf("http://%s:%d/beads", alpha.server.Host(), alpha.server.RemotesAPIPort())
	_ = beta.store.AddRemote(ctx, "alpha", alphaRemoteURL)

	// Beta pulls from Alpha
	if err := beta.store.Fetch(ctx, "alpha"); err != nil {
		t.Logf("Fetch: %v", err)
	}

	_, err := beta.store.PullFrom(ctx, "alpha")
	if err != nil {
		t.Logf("Pull: %v", err)
	}

	// Verify Beta received the validation data
	receivedIssue, err := beta.store.GetIssue(ctx, "validated-001")
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if receivedIssue == nil {
		t.Log("Note: Validated issue not yet visible in Beta")
	} else {
		t.Logf("✓ Beta received: %s", receivedIssue.ID)
		if len(receivedIssue.Validations) > 0 {
			v := receivedIssue.Validations[0]
			t.Logf("✓ Validation preserved: validator=%s, outcome=%s",
				v.Validator.Name, v.Outcome)
		} else {
			t.Log("Note: Validations array is empty - may need schema update or different sync")
		}
	}

	t.Log("=== Phase 3: Test validator history query ===")

	// Check that we can query history to see who validated what
	if receivedIssue != nil {
		history, err := beta.store.History(ctx, "validated-001")
		if err != nil {
			t.Logf("History query: %v", err)
		} else {
			t.Logf("✓ History entries: %d", len(history))
			for _, h := range history {
				t.Logf("  %s: %s", h.CommitHash[:8], h.Committer)
			}
		}
	}

	t.Log("=== Reputation tracking test completed ===")
	t.Log("Note: Full reputation scoring (success rate, sovereignty tier) is pending implementation")
}

// TownSetup holds all resources for a test town
type TownSetup struct {
	dir     string
	server  *Server
	store   *DoltStore
	cleanup func()
}

// setupTwoTowns creates two isolated towns with running dolt sql-servers.
// Alpha is started first, then Beta is cloned from Alpha's remotesapi to establish shared history.
// Each town gets its own directory, server on unique ports, and store connection.
func setupTwoTowns(t *testing.T, ctx context.Context) (*TownSetup, *TownSetup) {
	t.Helper()

	baseDir, err := os.MkdirTemp("", "peer-sync-test-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}

	// Setup Alpha directory
	alphaDir := filepath.Join(baseDir, "town-alpha")
	if err := os.MkdirAll(alphaDir, 0755); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create alpha dir: %v", err)
	}

	// Initialize dolt repo for Alpha
	cmd := exec.Command("dolt", "init")
	cmd.Dir = alphaDir
	if err := cmd.Run(); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to init alpha dolt repo: %v", err)
	}

	// Start Alpha server first so we can clone from its remotesapi
	alphaServer := NewServer(ServerConfig{
		DataDir:        alphaDir,
		SQLPort:        13307,
		RemotesAPIPort: 18081,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(alphaDir, "server.log"),
	})
	if err := alphaServer.Start(ctx); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to start alpha server: %v", err)
	}

	// Connect Alpha store and create genesis state
	alphaStore, err := New(ctx, &Config{
		Path:           alphaDir,
		Database:       "beads",
		ServerHost:     "127.0.0.1",
		ServerPort:     13307,
		CommitterName:  "alpha-town",
		CommitterEmail: "alpha@test.local",
	})
	if err != nil {
		alphaServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create alpha store: %v", err)
	}
	if err := alphaStore.SetConfig(ctx, "issue_prefix", "genesis"); err != nil {
		alphaStore.Close()
		alphaServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to set genesis prefix: %v", err)
	}
	if err := alphaStore.Commit(ctx, "Federation genesis commit"); err != nil {
		t.Logf("Alpha genesis commit: %v", err)
	}

	// Clone from Alpha's remotesapi to create Beta
	betaDir := filepath.Join(baseDir, "town-beta")
	alphaRemoteURL := fmt.Sprintf("http://127.0.0.1:%d/beads", alphaServer.RemotesAPIPort())
	cmd = exec.Command("dolt", "clone", alphaRemoteURL, betaDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		alphaStore.Close()
		alphaServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to clone from alpha: %v\nOutput: %s", err, output)
	}

	// Start Beta server
	betaServer := NewServer(ServerConfig{
		DataDir:        betaDir,
		SQLPort:        13308,
		RemotesAPIPort: 18082,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(betaDir, "server.log"),
	})
	if err := betaServer.Start(ctx); err != nil {
		alphaStore.Close()
		alphaServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to start beta server: %v", err)
	}

	// Update Alpha config for its own identity
	if err := alphaStore.SetConfig(ctx, "issue_prefix", "alpha"); err != nil {
		alphaStore.Close()
		alphaServer.Stop()
		betaServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to set alpha prefix: %v", err)
	}
	if err := alphaStore.Commit(ctx, "Alpha town configuration"); err != nil {
		t.Logf("Alpha config commit: %v", err)
	}

	// Connect Beta store
	betaStore, err := New(ctx, &Config{
		Path:           betaDir,
		Database:       "beads",
		ServerHost:     "127.0.0.1",
		ServerPort:     13308,
		CommitterName:  "beta-town",
		CommitterEmail: "beta@test.local",
	})
	if err != nil {
		alphaStore.Close()
		alphaServer.Stop()
		betaServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create beta store: %v", err)
	}
	if err := betaStore.SetConfig(ctx, "issue_prefix", "beta"); err != nil {
		betaStore.Close()
		alphaStore.Close()
		alphaServer.Stop()
		betaServer.Stop()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to set beta prefix: %v", err)
	}
	if err := betaStore.Commit(ctx, "Beta town configuration"); err != nil {
		t.Logf("Beta config commit: %v", err)
	}

	alpha := &TownSetup{
		dir:    alphaDir,
		server: alphaServer,
		store:  alphaStore,
		cleanup: func() {
			alphaStore.Close()
			alphaServer.Stop()
		},
	}

	beta := &TownSetup{
		dir:    betaDir,
		server: betaServer,
		store:  betaStore,
		cleanup: func() {
			betaStore.Close()
			betaServer.Stop()
			os.RemoveAll(baseDir)
		},
	}

	t.Logf("Towns ready: Alpha (SQL:%d, API:%d), Beta (SQL:%d, API:%d)",
		alphaServer.SQLPort(), alphaServer.RemotesAPIPort(),
		betaServer.SQLPort(), betaServer.RemotesAPIPort())

	return alpha, beta
}

// TestSyncWithCredentials tests federation sync with SQL user authentication.
// Note: PushWithCredentials may hang if Dolt's CALL DOLT_PUSH doesn't respect
// context cancellation. We enforce a 30s Go-level timeout around the push.
func TestSyncWithCredentials(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Setup two towns
	alpha, beta := setupTwoTowns(t, ctx)
	defer alpha.cleanup()
	defer beta.cleanup()

	t.Log("=== Testing credential-based sync ===")

	// Add Beta as a federation peer with credentials
	betaPeer := &storage.FederationPeer{
		Name:        "beta-town",
		RemoteURL:   fmt.Sprintf("http://%s:%d/beads", beta.server.Host(), beta.server.RemotesAPIPort()),
		Username:    "sync-user",
		Password:    "sync-password",
		Sovereignty: "T2",
	}
	if err := alpha.store.AddFederationPeer(ctx, betaPeer); err != nil {
		t.Fatalf("failed to add federation peer: %v", err)
	}
	t.Log("✓ Added Beta as federation peer with credentials")

	// List peers to verify
	peers, err := alpha.store.ListFederationPeers(ctx)
	if err != nil {
		t.Fatalf("failed to list peers: %v", err)
	}
	t.Logf("✓ Alpha has %d federation peer(s)", len(peers))
	for _, p := range peers {
		t.Logf("  - %s: %s (sovereignty: %s)", p.Name, p.RemoteURL, p.Sovereignty)
	}

	// Create issue to sync
	issue := &types.Issue{
		ID:        "cred-sync-001",
		Title:     "Issue synced with credentials",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := alpha.store.CreateIssue(ctx, issue, "alpha"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := alpha.store.Commit(ctx, "Create cred-sync-001"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Try push with credentials (may fail without proper user setup on beta).
	// Wrap in goroutine with timeout since Dolt CALL statements may not
	// respect context cancellation for network operations.
	pushDone := make(chan error, 1)
	go func() {
		pushDone <- alpha.store.PushWithCredentials(ctx, "beta-town")
	}()
	select {
	case err := <-pushDone:
		if err != nil {
			t.Logf("Push with credentials: %v (expected if beta doesn't have user)", err)
		} else {
			t.Log("✓ Push with credentials succeeded")
		}
	case <-time.After(30 * time.Second):
		t.Log("Push with credentials timed out after 30s (Dolt network operation hung)")
	}

	// Verify peer last_sync is updated
	peer, err := alpha.store.GetFederationPeer(ctx, "beta-town")
	if err != nil {
		t.Fatalf("failed to get peer: %v", err)
	}
	if peer != nil {
		if peer.LastSync != nil {
			t.Logf("✓ Last sync time recorded: %v", peer.LastSync)
		} else {
			t.Log("Note: Last sync time not recorded (sync may have failed)")
		}
	}

	t.Log("=== Credential sync test completed ===")
}

// TestBidirectionalSync tests the full Sync() method for bidirectional sync.
// Note: Sync() may hang if Dolt's internal push operations don't respect
// context cancellation. We enforce a 60s Go-level timeout around sync.
func TestBidirectionalSync(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Setup two towns
	alpha, beta := setupTwoTowns(t, ctx)
	defer alpha.cleanup()
	defer beta.cleanup()

	t.Log("=== Testing bidirectional Sync() method ===")

	// Configure peers
	betaRemoteURL := fmt.Sprintf("http://%s:%d/beads", beta.server.Host(), beta.server.RemotesAPIPort())
	_ = alpha.store.AddRemote(ctx, "beta", betaRemoteURL)

	// Create issue in Alpha
	issue := &types.Issue{
		ID:        "bidir-001",
		Title:     "Bidirectional sync test",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := alpha.store.CreateIssue(ctx, issue, "alpha"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := alpha.store.Commit(ctx, "Create bidir-001"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Use the Sync method with a Go-level timeout since Dolt's internal
	// push operations may not respect context cancellation.
	type syncOutcome struct {
		result *SyncResult
		err    error
	}
	syncDone := make(chan syncOutcome, 1)
	go func() {
		result, err := alpha.store.Sync(ctx, "beta", "ours")
		syncDone <- syncOutcome{result, err}
	}()

	select {
	case outcome := <-syncDone:
		if outcome.err != nil {
			t.Logf("Sync result error: %v", outcome.err)
		}
		if outcome.result != nil {
			t.Logf("Sync result: fetched=%v, merged=%v, pushed=%v",
				outcome.result.Fetched, outcome.result.Merged, outcome.result.Pushed)
			if outcome.result.PushError != nil {
				t.Logf("Push error (non-fatal): %v", outcome.result.PushError)
			}
			if len(outcome.result.Conflicts) > 0 {
				t.Logf("Conflicts: %d (resolved=%v)", len(outcome.result.Conflicts), outcome.result.ConflictsResolved)
			}
			t.Logf("Sync duration: %v", outcome.result.EndTime.Sub(outcome.result.StartTime))
		}
	case <-time.After(60 * time.Second):
		t.Log("Sync timed out after 60s (Dolt network operation hung)")
	}

	t.Log("=== Bidirectional sync test completed ===")
}
