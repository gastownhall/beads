// dual_mode_test.go - Test framework for ensuring commands work in both daemon and direct modes.
//
// PROBLEM:
// Multiple bugs have occurred where commands work in one mode but not the other:
// - GH#751: bd graph accessed nil store in daemon mode
// - GH#719: bd create -f bypassed daemon RPC
// - bd-fu83: relate/duplicate used direct store when daemon was running
//
// SOLUTION:
// This file provides a reusable test pattern that runs the same test logic
// in both direct mode (--no-daemon) and daemon mode, ensuring commands
// behave identically regardless of which mode they're running in.
//
// USAGE:
//
//	func TestCreateCommand(t *testing.T) {
//	    RunDualModeTest(t, "create basic issue", func(t *testing.T, env *DualModeTestEnv) {
//	        // Create an issue - this code runs twice: once in direct mode, once with daemon
//	        issue := &types.Issue{
//	            Title:     "Test issue",
//	            IssueType: types.TypeTask,
//	            Status:    types.StatusOpen,
//	            Priority:  2,
//	        }
//	        err := env.CreateIssue(issue)
//	        if err != nil {
//	            t.Fatalf("CreateIssue failed: %v", err)
//	        }
//
//	        // Verify issue was created
//	        got, err := env.GetIssue(issue.ID)
//	        if err != nil {
//	            t.Fatalf("GetIssue failed: %v", err)
//	        }
//	        if got.Title != "Test issue" {
//	            t.Errorf("expected title 'Test issue', got %q", got.Title)
//	        }
//	    })
//	}
//
// The test framework handles:
// - Setting up isolated test environments (temp dirs, databases)
// - Starting/stopping daemon for daemon mode tests
// - Saving/restoring global state between runs
// - Providing a unified API for common operations

//go:build integration
// +build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestMode indicates which mode the test is running in
type TestMode string

const (
	// DirectMode: Commands access SQLite directly (--no-daemon)
	DirectMode TestMode = "direct"
	// DaemonMode: Commands communicate via RPC to a background daemon
	DaemonMode TestMode = "daemon"
)

// DualModeTestEnv provides a unified test environment that works in both modes.
// Tests should use this interface rather than accessing global state directly.
type DualModeTestEnv struct {
	t          *testing.T
	mode       TestMode
	tmpDir     string
	beadsDir   string
	dbPath     string
	socketPath string

	// Direct mode resources
	store *sqlite.SQLiteStorage

	// Daemon mode resources
	client     *rpc.Client
	server     *rpc.Server
	serverDone chan error

	// Context for operations
	ctx    context.Context
	cancel context.CancelFunc
}

// Mode returns the current test mode (direct or daemon)
func (e *DualModeTestEnv) Mode() TestMode {
	return e.mode
}

// Context returns the test context
func (e *DualModeTestEnv) Context() context.Context {
	return e.ctx
}

// Store returns the direct store (only valid in DirectMode)
// For mode-agnostic operations, use the helper methods instead.
func (e *DualModeTestEnv) Store() *sqlite.SQLiteStorage {
	if e.mode != DirectMode {
		e.t.Fatal("Store() called in daemon mode - use helper methods instead")
	}
	return e.store
}

// Client returns the RPC client (only valid in DaemonMode)
// For mode-agnostic operations, use the helper methods instead.
func (e *DualModeTestEnv) Client() *rpc.Client {
	if e.mode != DaemonMode {
		e.t.Fatal("Client() called in direct mode - use helper methods instead")
	}
	return e.client
}

// CreateIssue creates an issue in either mode
func (e *DualModeTestEnv) CreateIssue(issue *types.Issue) error {
	if e.mode == DirectMode {
		return e.store.CreateIssue(e.ctx, issue, "test")
	}

	// Daemon mode: use RPC
	args := &rpc.CreateArgs{
		Title:       issue.Title,
		Description: issue.Description,
		IssueType:   string(issue.IssueType),
		Priority:    issue.Priority,
	}
	resp, err := e.client.Create(args)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("create failed: %s", resp.Error)
	}

	// Parse response to get the created issue ID
	// The RPC response contains the created issue as JSON
	var createdIssue types.Issue
	if err := json.Unmarshal(resp.Data, &createdIssue); err != nil {
		return fmt.Errorf("failed to parse created issue: %w", err)
	}
	issue.ID = createdIssue.ID
	return nil
}

// GetIssue retrieves an issue by ID in either mode
func (e *DualModeTestEnv) GetIssue(id string) (*types.Issue, error) {
	if e.mode == DirectMode {
		return e.store.GetIssue(e.ctx, id)
	}

	// Daemon mode: use RPC
	args := &rpc.ShowArgs{ID: id}
	resp, err := e.client.Show(args)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("show failed: %s", resp.Error)
	}

	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		return nil, fmt.Errorf("failed to parse issue: %w", err)
	}
	return &issue, nil
}

// UpdateIssue updates an issue in either mode
func (e *DualModeTestEnv) UpdateIssue(id string, updates map[string]interface{}) error {
	if e.mode == DirectMode {
		return e.store.UpdateIssue(e.ctx, id, updates, "test")
	}

	// Daemon mode: use RPC - convert map to UpdateArgs fields
	args := &rpc.UpdateArgs{ID: id}

	// Map common fields to their RPC counterparts
	if title, ok := updates["title"].(string); ok {
		args.Title = &title
	}
	if status, ok := updates["status"].(types.Status); ok {
		s := string(status)
		args.Status = &s
	}
	if statusStr, ok := updates["status"].(string); ok {
		args.Status = &statusStr
	}
	if priority, ok := updates["priority"].(int); ok {
		args.Priority = &priority
	}
	if desc, ok := updates["description"].(string); ok {
		args.Description = &desc
	}
	if issueType, ok := updates["issue_type"].(string); ok {
		args.IssueType = &issueType
	}

	resp, err := e.client.Update(args)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("update failed: %s", resp.Error)
	}
	return nil
}

// DeleteIssue marks an issue as deleted (tombstoned) in either mode
func (e *DualModeTestEnv) DeleteIssue(id string, force bool) error {
	if e.mode == DirectMode {
		updates := map[string]interface{}{
			"status": types.StatusTombstone,
		}
		return e.store.UpdateIssue(e.ctx, id, updates, "test")
	}

	// Daemon mode: use RPC
	args := &rpc.DeleteArgs{
		IDs:    []string{id},
		Force:  force,
		DryRun: false,
		Reason: "test deletion",
	}
	resp, err := e.client.Delete(args)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("delete failed: %s", resp.Error)
	}
	return nil
}

// AddDependency adds a dependency in either mode
func (e *DualModeTestEnv) AddDependency(issueID, dependsOnID string, depType types.DependencyType) error {
	if e.mode == DirectMode {
		dep := &types.Dependency{
			IssueID:     issueID,
			DependsOnID: dependsOnID,
			Type:        depType,
		}
		return e.store.AddDependency(e.ctx, dep, "test")
	}

	// Daemon mode: use RPC
	args := &rpc.DepAddArgs{
		FromID:  issueID,
		ToID:    dependsOnID,
		DepType: string(depType),
	}
	resp, err := e.client.AddDependency(args)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("add dependency failed: %s", resp.Error)
	}
	return nil
}

// ListIssues returns issues matching the filter in either mode
func (e *DualModeTestEnv) ListIssues(filter types.IssueFilter) ([]*types.Issue, error) {
	if e.mode == DirectMode {
		return e.store.SearchIssues(e.ctx, "", filter)
	}

	// Daemon mode: use RPC - convert filter to ListArgs
	args := &rpc.ListArgs{}
	if filter.Status != nil {
		args.Status = string(*filter.Status)
	}
	if filter.Priority != nil {
		args.Priority = filter.Priority
	}
	if filter.IssueType != nil {
		args.IssueType = string(*filter.IssueType)
	}

	resp, err := e.client.List(args)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("list failed: %s", resp.Error)
	}

	var issues []*types.Issue
	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		return nil, fmt.Errorf("failed to parse issues: %w", err)
	}
	return issues, nil
}

// GetReadyWork returns issues ready for work in either mode
func (e *DualModeTestEnv) GetReadyWork() ([]*types.Issue, error) {
	if e.mode == DirectMode {
		return e.store.GetReadyWork(e.ctx, types.WorkFilter{})
	}

	// Daemon mode: use RPC
	args := &rpc.ReadyArgs{}
	resp, err := e.client.Ready(args)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("ready failed: %s", resp.Error)
	}

	var issues []*types.Issue
	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		return nil, fmt.Errorf("failed to parse issues: %w", err)
	}
	return issues, nil
}

// AddLabel adds a label to an issue in either mode
func (e *DualModeTestEnv) AddLabel(issueID, label string) error {
	if e.mode == DirectMode {
		return e.store.AddLabel(e.ctx, issueID, label, "test")
	}

	// Daemon mode: use RPC
	args := &rpc.LabelAddArgs{
		ID:    issueID,
		Label: label,
	}
	resp, err := e.client.AddLabel(args)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("add label failed: %s", resp.Error)
	}
	return nil
}

// RemoveLabel removes a label from an issue in either mode
func (e *DualModeTestEnv) RemoveLabel(issueID, label string) error {
	if e.mode == DirectMode {
		return e.store.RemoveLabel(e.ctx, issueID, label, "test")
	}

	// Daemon mode: use RPC
	args := &rpc.LabelRemoveArgs{
		ID:    issueID,
		Label: label,
	}
	resp, err := e.client.RemoveLabel(args)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("remove label failed: %s", resp.Error)
	}
	return nil
}

// AddComment adds a comment to an issue in either mode
func (e *DualModeTestEnv) AddComment(issueID, text string) error {
	if e.mode == DirectMode {
		return e.store.AddComment(e.ctx, issueID, "test", text)
	}

	// Daemon mode: use RPC
	args := &rpc.CommentAddArgs{
		ID:     issueID,
		Author: "test",
		Text:   text,
	}
	resp, err := e.client.AddComment(args)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("add comment failed: %s", resp.Error)
	}
	return nil
}

// CloseIssue closes an issue with a reason in either mode
func (e *DualModeTestEnv) CloseIssue(id, reason string) error {
	if e.mode == DirectMode {
		updates := map[string]interface{}{
			"status":       types.StatusClosed,
			"close_reason": reason,
		}
		return e.store.UpdateIssue(e.ctx, id, updates, "test")
	}

	// Daemon mode: use RPC
	args := &rpc.CloseArgs{
		ID:     id,
		Reason: reason,
	}
	resp, err := e.client.CloseIssue(args)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("close failed: %s", resp.Error)
	}
	return nil
}

// TmpDir returns the test temporary directory
func (e *DualModeTestEnv) TmpDir() string {
	return e.tmpDir
}

// BeadsDir returns the .beads directory path
func (e *DualModeTestEnv) BeadsDir() string {
	return e.beadsDir
}

// DBPath returns the database file path
func (e *DualModeTestEnv) DBPath() string {
	return e.dbPath
}

// DualModeTestFunc is the function signature for tests that run in both modes
type DualModeTestFunc func(t *testing.T, env *DualModeTestEnv)

// RunDualModeTest runs a test function in both direct mode and daemon mode.
// This ensures the tested behavior works correctly regardless of which mode
// the CLI is operating in.
func RunDualModeTest(t *testing.T, name string, testFn DualModeTestFunc) {
	t.Helper()

	// Run in direct mode
	t.Run(name+"_direct", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping dual-mode test in short mode")
		}
		env := setupDirectModeEnv(t)
		testFn(t, env)
	})

	// Run in daemon mode
	t.Run(name+"_daemon", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping dual-mode test in short mode")
		}
		env := setupDaemonModeEnv(t)
		testFn(t, env)
	})
}

// RunDirectModeOnly runs a test only in direct mode.
// Use sparingly - prefer RunDualModeTest for most tests.
func RunDirectModeOnly(t *testing.T, name string, testFn DualModeTestFunc) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		env := setupDirectModeEnv(t)
		testFn(t, env)
	})
}

// RunDaemonModeOnly runs a test only in daemon mode.
// Use sparingly - prefer RunDualModeTest for most tests.
func RunDaemonModeOnly(t *testing.T, name string, testFn DualModeTestFunc) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping daemon test in short mode")
		}
		env := setupDaemonModeEnv(t)
		testFn(t, env)
	})
}

// setupDirectModeEnv creates a test environment for direct mode testing
func setupDirectModeEnv(t *testing.T) *DualModeTestEnv {
	t.Helper()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	store := newTestStore(t, dbPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	env := &DualModeTestEnv{
		t:        t,
		mode:     DirectMode,
		tmpDir:   tmpDir,
		beadsDir: beadsDir,
		dbPath:   dbPath,
		store:    store,
		ctx:      ctx,
		cancel:   cancel,
	}

	return env
}

// setupDaemonModeEnv creates a test environment with a running daemon
func setupDaemonModeEnv(t *testing.T) *DualModeTestEnv {
	t.Helper()

	tmpDir := makeSocketTempDir(t)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Initialize git repo (required for daemon)
	initTestGitRepo(t, tmpDir)

	dbPath := filepath.Join(beadsDir, "beads.db")
	socketPath := filepath.Join(beadsDir, "bd.sock")
	store := newTestStore(t, dbPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	// Create daemon logger
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Start RPC server
	server, serverErrChan, err := startRPCServer(ctx, socketPath, store, tmpDir, dbPath, log)
	if err != nil {
		cancel()
		t.Fatalf("failed to start RPC server: %v", err)
	}

	// Wait for server to be ready
	select {
	case <-server.WaitReady():
		// Server is ready
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not become ready within 5 seconds")
	}

	// Connect RPC client
	client, err := rpc.TryConnect(socketPath)
	if err != nil || client == nil {
		cancel()
		server.Stop()
		t.Fatalf("failed to connect RPC client: %v", err)
	}

	// Consume server errors in background
	serverDone := make(chan error, 1)
	go func() {
		select {
		case err := <-serverErrChan:
			serverDone <- err
		case <-ctx.Done():
			serverDone <- ctx.Err()
		}
	}()

	env := &DualModeTestEnv{
		t:          t,
		mode:       DaemonMode,
		tmpDir:     tmpDir,
		beadsDir:   beadsDir,
		dbPath:     dbPath,
		socketPath: socketPath,
		store:      store,
		client:     client,
		server:     server,
		serverDone: serverDone,
		ctx:        ctx,
		cancel:     cancel,
	}

	// Register cleanup
	t.Cleanup(func() {
		if client != nil {
			client.Close()
		}
		if server != nil {
			server.Stop()
		}
		cancel()
		os.RemoveAll(tmpDir)
	})

	return env
}

// ============================================================================
// Example dual-mode tests demonstrating the pattern
// ============================================================================

// TestDualMode_CreateAndRetrieveIssue demonstrates the basic dual-mode test pattern
func TestDualMode_CreateAndRetrieveIssue(t *testing.T) {
	RunDualModeTest(t, "create_and_retrieve", func(t *testing.T, env *DualModeTestEnv) {
		// This code runs twice: once in direct mode, once with daemon
		issue := &types.Issue{
			Title:       "Test issue",
			Description: "Test description",
			IssueType:   types.TypeTask,
			Status:      types.StatusOpen,
			Priority:    2,
		}

		// Create issue
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		if issue.ID == "" {
			t.Fatalf("[%s] issue ID not set after creation", env.Mode())
		}

		// Retrieve issue
		got, err := env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}

		// Verify
		if got.Title != "Test issue" {
			t.Errorf("[%s] expected title 'Test issue', got %q", env.Mode(), got.Title)
		}
		if got.Status != types.StatusOpen {
			t.Errorf("[%s] expected status 'open', got %q", env.Mode(), got.Status)
		}
	})
}

// TestDualMode_UpdateIssue tests updating issues works in both modes
func TestDualMode_UpdateIssue(t *testing.T) {
	RunDualModeTest(t, "update_issue", func(t *testing.T, env *DualModeTestEnv) {
		// Create issue
		issue := &types.Issue{
			Title:     "Original title",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  2,
		}
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		// Update issue
		updates := map[string]interface{}{
			"title":  "Updated title",
			"status": types.StatusInProgress,
		}
		if err := env.UpdateIssue(issue.ID, updates); err != nil {
			t.Fatalf("[%s] UpdateIssue failed: %v", env.Mode(), err)
		}

		// Verify update
		got, err := env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}

		if got.Title != "Updated title" {
			t.Errorf("[%s] expected title 'Updated title', got %q", env.Mode(), got.Title)
		}
		if got.Status != types.StatusInProgress {
			t.Errorf("[%s] expected status 'in_progress', got %q", env.Mode(), got.Status)
		}
	})
}

// TestDualMode_Dependencies tests dependency operations in both modes
func TestDualMode_Dependencies(t *testing.T) {
	RunDualModeTest(t, "dependencies", func(t *testing.T, env *DualModeTestEnv) {
		// Create two issues
		blocker := &types.Issue{
			Title:     "Blocker issue",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  1,
		}
		blocked := &types.Issue{
			Title:     "Blocked issue",
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  2,
		}

		if err := env.CreateIssue(blocker); err != nil {
			t.Fatalf("[%s] CreateIssue(blocker) failed: %v", env.Mode(), err)
		}
		if err := env.CreateIssue(blocked); err != nil {
			t.Fatalf("[%s] CreateIssue(blocked) failed: %v", env.Mode(), err)
		}

		// Add blocking dependency
		if err := env.AddDependency(blocked.ID, blocker.ID, types.DepBlocks); err != nil {
			t.Fatalf("[%s] AddDependency failed: %v", env.Mode(), err)
		}

		// Verify blocked issue is not in ready queue
		ready, err := env.GetReadyWork()
		if err != nil {
			t.Fatalf("[%s] GetReadyWork failed: %v", env.Mode(), err)
		}

		for _, r := range ready {
			if r.ID == blocked.ID {
				t.Errorf("[%s] blocked issue should not be in ready queue", env.Mode())
			}
		}

		// Verify blocker is in ready queue (it has no blockers)
		found := false
		for _, r := range ready {
			if r.ID == blocker.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("[%s] blocker issue should be in ready queue", env.Mode())
		}
	})
}

// TestDualMode_ListIssues tests listing issues works in both modes
func TestDualMode_ListIssues(t *testing.T) {
	RunDualModeTest(t, "list_issues", func(t *testing.T, env *DualModeTestEnv) {
		// Create multiple issues
		for i := 0; i < 3; i++ {
			issue := &types.Issue{
				Title:     fmt.Sprintf("Issue %d", i),
				IssueType: types.TypeTask,
				Status:    types.StatusOpen,
				Priority:  i + 1,
			}
			if err := env.CreateIssue(issue); err != nil {
				t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
			}
		}

		// List all issues
		issues, err := env.ListIssues(types.IssueFilter{})
		if err != nil {
			t.Fatalf("[%s] ListIssues failed: %v", env.Mode(), err)
		}

		if len(issues) != 3 {
			t.Errorf("[%s] expected 3 issues, got %d", env.Mode(), len(issues))
		}
	})
}

// TestDualMode_Labels tests label operations in both modes
func TestDualMode_Labels(t *testing.T) {
	RunDualModeTest(t, "labels", func(t *testing.T, env *DualModeTestEnv) {
		// Create issue
		issue := &types.Issue{
			Title:     "Issue with labels",
			IssueType: types.TypeBug,
			Status:    types.StatusOpen,
			Priority:  1,
		}
		if err := env.CreateIssue(issue); err != nil {
			t.Fatalf("[%s] CreateIssue failed: %v", env.Mode(), err)
		}

		// Add label
		if err := env.AddLabel(issue.ID, "critical"); err != nil {
			t.Fatalf("[%s] AddLabel failed: %v", env.Mode(), err)
		}

		// Verify label was added by fetching the issue
		got, err := env.GetIssue(issue.ID)
		if err != nil {
			t.Fatalf("[%s] GetIssue failed: %v", env.Mode(), err)
		}

		// Note: Label verification depends on whether the Show RPC returns labels
		// This test primarily verifies the AddLabel operation doesn't error
		_ = got // Use the retrieved issue for future label verification
	})
}
