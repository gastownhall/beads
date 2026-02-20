//go:build cgo

package dolt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestServerMode_AddDependencyPersistsAcrossConnections verifies that AddDependency
// commits data that survives connection close when the server uses --no-auto-commit
// (@@autocommit = 0).
func TestServerMode_AddDependencyPersistsAcrossConnections(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server mode test")
	}

	tmpDir, err := os.MkdirTemp("", "dolt-dep-persist-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init dolt repo: %v", err)
	}

	// Start server (passes --no-auto-commit, setting @@autocommit = 0)
	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13311,
		RemotesAPIPort: 18085,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(tmpDir, "server.log"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		if err := server.Stop(); err != nil {
			t.Logf("warning: failed to stop server: %v", err)
		}
	}()

	storeCfg := &Config{
		Path:       tmpDir,
		Database:   "beads",
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: 13311,
	}

	// --- Connection 1: create issues and add dependency ---
	store1, err := New(ctx, storeCfg)
	if err != nil {
		t.Fatalf("failed to create store (conn 1): %v", err)
	}

	if err := store1.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	issueA := &types.Issue{
		Title:     "Blocker issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store1.CreateIssue(ctx, issueA, "test"); err != nil {
		t.Fatalf("failed to create issue A: %v", err)
	}

	issueB := &types.Issue{
		Title:     "Blocked issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store1.CreateIssue(ctx, issueB, "test"); err != nil {
		t.Fatalf("failed to create issue B: %v", err)
	}

	dep := &types.Dependency{
		IssueID:     issueB.ID,
		DependsOnID: issueA.ID,
		Type:        types.DepBlocks,
	}
	if err := store1.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	// Close the first connection entirely
	store1.Close()

	// --- Connection 2: verify dependency persisted ---
	store2, err := New(ctx, storeCfg)
	if err != nil {
		t.Fatalf("failed to create store (conn 2): %v", err)
	}
	defer store2.Close()

	deps, err := store2.GetDependencies(ctx, issueB.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed on new connection: %v", err)
	}

	if len(deps) != 1 {
		t.Fatalf("expected 1 dependency after reconnect, got %d (dependency was lost)", len(deps))
	}
	if deps[0].ID != issueA.ID {
		t.Errorf("expected dependency on %s, got %s", issueA.ID, deps[0].ID)
	}
}

// TestServerMode_RemoveDependencyPersistsAcrossConnections verifies that
// RemoveDependency commits data that survives connection close.
func TestServerMode_RemoveDependencyPersistsAcrossConnections(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping server mode test")
	}

	tmpDir, err := os.MkdirTemp("", "dolt-dep-remove-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("dolt", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init dolt repo: %v", err)
	}

	server := NewServer(ServerConfig{
		DataDir:        tmpDir,
		SQLPort:        13312,
		RemotesAPIPort: 18086,
		Host:           "127.0.0.1",
		LogFile:        filepath.Join(tmpDir, "server.log"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		if err := server.Stop(); err != nil {
			t.Logf("warning: failed to stop server: %v", err)
		}
	}()

	storeCfg := &Config{
		Path:       tmpDir,
		Database:   "beads",
		ServerMode: true,
		ServerHost: "127.0.0.1",
		ServerPort: 13312,
	}

	// --- Connection 1: create issues, add dependency, then remove it ---
	store1, err := New(ctx, storeCfg)
	if err != nil {
		t.Fatalf("failed to create store (conn 1): %v", err)
	}

	if err := store1.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	issueA := &types.Issue{
		Title:     "Issue A",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store1.CreateIssue(ctx, issueA, "test"); err != nil {
		t.Fatalf("failed to create issue A: %v", err)
	}

	issueB := &types.Issue{
		Title:     "Issue B",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store1.CreateIssue(ctx, issueB, "test"); err != nil {
		t.Fatalf("failed to create issue B: %v", err)
	}

	dep := &types.Dependency{
		IssueID:     issueB.ID,
		DependsOnID: issueA.ID,
		Type:        types.DepBlocks,
	}
	if err := store1.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("AddDependency failed: %v", err)
	}

	if err := store1.RemoveDependency(ctx, issueB.ID, issueA.ID, "test"); err != nil {
		t.Fatalf("RemoveDependency failed: %v", err)
	}

	store1.Close()

	// --- Connection 2: verify removal persisted ---
	store2, err := New(ctx, storeCfg)
	if err != nil {
		t.Fatalf("failed to create store (conn 2): %v", err)
	}
	defer store2.Close()

	deps, err := store2.GetDependencies(ctx, issueB.ID)
	if err != nil {
		t.Fatalf("GetDependencies failed on new connection: %v", err)
	}

	if len(deps) != 0 {
		t.Fatalf("expected 0 dependencies after reconnect (removal was lost), got %d", len(deps))
	}
}
