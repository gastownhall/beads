package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestShowCommand(t *testing.T) {
	// Save original global state
	origStore := store
	origDBPath := dbPath
	origDaemonClient := daemonClient
	defer func() {
		store = origStore
		dbPath = origDBPath
		daemonClient = origDaemonClient
	}()

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")

	// Create test store and set it globally
	testStore := newTestStore(t, testDB)
	defer testStore.Close()

	store = testStore
	dbPath = testDB
	daemonClient = nil // Force direct mode

	// Ensure BEADS_NO_DAEMON is set
	os.Setenv("BEADS_NO_DAEMON", "1")
	defer os.Unsetenv("BEADS_NO_DAEMON")

	ctx := context.Background()

	// Create test issues
	issue1 := &types.Issue{
		Title:       "First Test Issue",
		Description: "This is a test description",
		Priority:    1,
		IssueType:   types.TypeBug,
		Status:      types.StatusOpen,
	}
	if err := testStore.CreateIssue(ctx, issue1, "test-user"); err != nil {
		t.Fatalf("Failed to create issue1: %v", err)
	}

	issue2 := &types.Issue{
		Title:       "Second Test Issue",
		Description: "Another description",
		Design:      "Design notes here",
		Notes:       "Some notes",
		Priority:    2,
		IssueType:   types.TypeFeature,
		Status:      types.StatusInProgress,
		Assignee:    "alice",
	}
	if err := testStore.CreateIssue(ctx, issue2, "test-user"); err != nil {
		t.Fatalf("Failed to create issue2: %v", err)
	}

	// Add label to issue1
	if err := testStore.AddLabel(ctx, issue1.ID, "critical", "test-user"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}

	// Add dependency: issue2 depends on issue1
	dep := &types.Dependency{
		IssueID:     issue2.ID,
		DependsOnID: issue1.ID,
		Type:        types.DepBlocks,
	}
	if err := testStore.AddDependency(ctx, dep, "test-user"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	t.Run("show single issue", func(t *testing.T) {
		// Capture output
		var buf bytes.Buffer
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Reset command state
		rootCmd.SetArgs([]string{"show", issue1.ID})
		showCmd.Flags().Set("json", "false")

		err := rootCmd.Execute()

		// Restore stdout and read output
		w.Close()
		buf.ReadFrom(r)
		os.Stdout = oldStdout
		output := buf.String()

		if err != nil {
			t.Fatalf("show command failed: %v", err)
		}

		// Verify output contains issue details
		if !strings.Contains(output, issue1.ID) {
			t.Errorf("Output should contain issue ID %s", issue1.ID)
		}
		if !strings.Contains(output, issue1.Title) {
			t.Errorf("Output should contain issue title %s", issue1.Title)
		}
		if !strings.Contains(output, "critical") {
			t.Error("Output should contain label 'critical'")
		}
	})

	t.Run("show single issue with JSON output", func(t *testing.T) {
		// Capture output
		var buf bytes.Buffer
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Reset command state
		jsonOutput = true
		defer func() { jsonOutput = false }()
		rootCmd.SetArgs([]string{"show", issue1.ID, "--json"})

		err := rootCmd.Execute()

		// Restore stdout and read output
		w.Close()
		buf.ReadFrom(r)
		os.Stdout = oldStdout
		output := buf.String()

		if err != nil {
			t.Fatalf("show command failed: %v", err)
		}

		// Parse JSON output
		var result []map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
		}

		if len(result) != 1 {
			t.Fatalf("Expected 1 issue in result, got %d", len(result))
		}

		if result[0]["id"] != issue1.ID {
			t.Errorf("Expected issue ID %s, got %v", issue1.ID, result[0]["id"])
		}
		if result[0]["title"] != issue1.Title {
			t.Errorf("Expected title %s, got %v", issue1.Title, result[0]["title"])
		}

		// Verify labels are included
		labels, ok := result[0]["labels"].([]interface{})
		if !ok || len(labels) == 0 {
			t.Error("Expected labels in JSON output")
		}
	})

	t.Run("show multiple issues", func(t *testing.T) {
		// Capture output
		var buf bytes.Buffer
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Reset command state
		jsonOutput = true
		defer func() { jsonOutput = false }()
		rootCmd.SetArgs([]string{"show", issue1.ID, issue2.ID, "--json"})

		err := rootCmd.Execute()

		// Restore stdout and read output
		w.Close()
		buf.ReadFrom(r)
		os.Stdout = oldStdout
		output := buf.String()

		if err != nil {
			t.Fatalf("show command failed: %v", err)
		}

		// Parse JSON output
		var result []map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v", err)
		}

		if len(result) != 2 {
			t.Fatalf("Expected 2 issues in result, got %d", len(result))
		}
	})

	t.Run("show with dependencies", func(t *testing.T) {
		// Capture output
		var buf bytes.Buffer
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Reset command state
		jsonOutput = true
		defer func() { jsonOutput = false }()
		rootCmd.SetArgs([]string{"show", issue2.ID, "--json"})

		err := rootCmd.Execute()

		// Restore stdout and read output
		w.Close()
		buf.ReadFrom(r)
		os.Stdout = oldStdout
		output := buf.String()

		if err != nil {
			t.Fatalf("show command failed: %v", err)
		}

		// Parse JSON output
		var result []map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v", err)
		}

		// Verify dependencies are included
		deps, ok := result[0]["dependencies"].([]interface{})
		if !ok || len(deps) == 0 {
			t.Error("Expected dependencies in JSON output")
		}
	})

	t.Run("show with compaction", func(t *testing.T) {
		// Create a compacted issue
		now := time.Now()
		compactedIssue := &types.Issue{
			Title:           "Compacted Issue",
			Description:     "Original long description",
			Priority:        1,
			IssueType:       types.TypeTask,
			Status:          types.StatusClosed,
			ClosedAt:        &now,
			CompactionLevel: 1,
			OriginalSize:    100,
			CompactedAt:     &now,
		}
		if err := testStore.CreateIssue(ctx, compactedIssue, "test-user"); err != nil {
			t.Fatalf("Failed to create compacted issue: %v", err)
		}

		// Capture output
		var buf bytes.Buffer
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Reset command state
		rootCmd.SetArgs([]string{"show", compactedIssue.ID})
		showCmd.Flags().Set("json", "false")

		err := rootCmd.Execute()

		// Restore stdout and read output
		w.Close()
		buf.ReadFrom(r)
		os.Stdout = oldStdout
		output := buf.String()

		if err != nil {
			t.Fatalf("show command failed: %v", err)
		}

		// Verify compaction indicators are shown
		// Note: Case-insensitive check since output might have "Compacted" (capitalized)
		outputLower := strings.ToLower(output)
		if !strings.Contains(outputLower, "compacted") {
			t.Errorf("Output should indicate issue is compacted, got: %s", output)
		}
	})
}

func TestUpdateCommand(t *testing.T) {
	// Save original global state
	origStore := store
	origDBPath := dbPath
	origDaemonClient := daemonClient
	defer func() {
		store = origStore
		dbPath = origDBPath
		daemonClient = origDaemonClient
	}()

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")

	// Create test store and set it globally
	testStore := newTestStore(t, testDB)
	defer testStore.Close()

	store = testStore
	dbPath = testDB
	daemonClient = nil // Force direct mode

	// Ensure BEADS_NO_DAEMON is set
	os.Setenv("BEADS_NO_DAEMON", "1")
	defer os.Unsetenv("BEADS_NO_DAEMON")

	ctx := context.Background()

	// Create test issue
	issue := &types.Issue{
		Title:       "Test Issue",
		Description: "Original description",
		Priority:    2,
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
	}
	if err := testStore.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	t.Run("update status", func(t *testing.T) {
		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, "--status", "in_progress"})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.Status != types.StatusInProgress {
			t.Errorf("Expected status %s, got %s", types.StatusInProgress, updated.Status)
		}
	})

	t.Run("update priority", func(t *testing.T) {
		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, "--priority", "0"})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.Priority != 0 {
			t.Errorf("Expected priority 0, got %d", updated.Priority)
		}
	})

	t.Run("update title", func(t *testing.T) {
		newTitle := "Updated Test Issue"

		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, "--title", newTitle})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.Title != newTitle {
			t.Errorf("Expected title %s, got %s", newTitle, updated.Title)
		}
	})

	t.Run("update assignee", func(t *testing.T) {
		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, "--assignee", "bob"})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.Assignee != "bob" {
			t.Errorf("Expected assignee bob, got %s", updated.Assignee)
		}
	})

	t.Run("update description", func(t *testing.T) {
		newDesc := "New description text"

		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, "--description", newDesc})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.Description != newDesc {
			t.Errorf("Expected description %s, got %s", newDesc, updated.Description)
		}
	})

	t.Run("update multiple fields", func(t *testing.T) {
		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID,
			"--status", "closed",
			"--priority", "1",
			"--assignee", "charlie"})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.Status != types.StatusClosed {
			t.Errorf("Expected status %s, got %s", types.StatusClosed, updated.Status)
		}
		if updated.Priority != 1 {
			t.Errorf("Expected priority 1, got %d", updated.Priority)
		}
		if updated.Assignee != "charlie" {
			t.Errorf("Expected assignee charlie, got %s", updated.Assignee)
		}
	})

	t.Run("update multiple issues", func(t *testing.T) {
		// Create second test issue
		issue2 := &types.Issue{
			Title:       "Second Test Issue",
			Priority:    2,
			IssueType:   types.TypeBug,
			Status:      types.StatusOpen,
		}
		if err := testStore.CreateIssue(ctx, issue2, "test-user"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}

		// Reset both issues to open
		testStore.UpdateIssue(ctx, issue.ID, map[string]interface{}{"status": types.StatusOpen}, "test-user")

		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, issue2.ID, "--status", "in_progress"})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify both issues were updated
		updated1, _ := testStore.GetIssue(ctx, issue.ID)
		updated2, _ := testStore.GetIssue(ctx, issue2.ID)

		if updated1.Status != types.StatusInProgress {
			t.Errorf("Expected issue1 status %s, got %s", types.StatusInProgress, updated1.Status)
		}
		if updated2.Status != types.StatusInProgress {
			t.Errorf("Expected issue2 status %s, got %s", types.StatusInProgress, updated2.Status)
		}
	})

	t.Run("update with JSON output", func(t *testing.T) {
		// Capture output
		var buf bytes.Buffer
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Reset command state
		jsonOutput = true
		defer func() { jsonOutput = false }()
		rootCmd.SetArgs([]string{"update", issue.ID, "--priority", "3", "--json"})

		err := rootCmd.Execute()

		// Restore stdout and read output
		w.Close()
		buf.ReadFrom(r)
		os.Stdout = oldStdout
		output := buf.String()

		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Parse JSON output
		var result []map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v", err)
		}

		if len(result) != 1 {
			t.Fatalf("Expected 1 issue in result, got %d", len(result))
		}

		// Verify priority was updated
		priority := int(result[0]["priority"].(float64))
		if priority != 3 {
			t.Errorf("Expected priority 3, got %d", priority)
		}
	})

	t.Run("update design notes", func(t *testing.T) {
		designNotes := "New design approach"

		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, "--design", designNotes})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.Design != designNotes {
			t.Errorf("Expected design %s, got %s", designNotes, updated.Design)
		}
	})

	t.Run("update notes", func(t *testing.T) {
		notes := "Additional notes here"

		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, "--notes", notes})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.Notes != notes {
			t.Errorf("Expected notes %s, got %s", notes, updated.Notes)
		}
	})

	t.Run("update acceptance criteria", func(t *testing.T) {
		acceptance := "Must pass all tests"

		// Reset command state
		rootCmd.SetArgs([]string{"update", issue.ID, "--acceptance", acceptance})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("update command failed: %v", err)
		}

		// Verify issue was updated
		updated, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get updated issue: %v", err)
		}
		if updated.AcceptanceCriteria != acceptance {
			t.Errorf("Expected acceptance criteria %s, got %s", acceptance, updated.AcceptanceCriteria)
		}
	})
}

func TestEditCommand(t *testing.T) {
	// Note: The edit command opens an interactive editor and is difficult to test
	// in an automated fashion without complex mocking. We test what we can:
	// - That the command exists and can be invoked
	// - That it properly validates input (issue ID required)

	// Save original global state
	origStore := store
	origDBPath := dbPath
	origDaemonClient := daemonClient
	defer func() {
		store = origStore
		dbPath = origDBPath
		daemonClient = origDaemonClient
	}()

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")

	// Create test store and set it globally
	testStore := newTestStore(t, testDB)
	defer testStore.Close()

	store = testStore
	dbPath = testDB
	daemonClient = nil // Force direct mode

	// Ensure BEADS_NO_DAEMON is set
	os.Setenv("BEADS_NO_DAEMON", "1")
	defer os.Unsetenv("BEADS_NO_DAEMON")

	ctx := context.Background()

	// Create test issue
	issue := &types.Issue{
		Title:       "Test Issue",
		Description: "Original description",
		Priority:    1,
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
	}
	if err := testStore.CreateIssue(ctx, issue, "test-user"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	t.Run("edit command validation", func(t *testing.T) {
		// Test that edit command requires an issue ID argument
		rootCmd.SetArgs([]string{"edit"})

		// Capture stderr
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		err := rootCmd.Execute()

		// Restore stderr
		w.Close()
		var buf bytes.Buffer
		buf.ReadFrom(r)
		os.Stderr = oldStderr

		// Should fail with argument validation error
		if err == nil {
			t.Error("Expected error when no issue ID provided to edit command")
		}
	})

	// Testing the actual interactive editor flow would require mocking the editor
	// process, which is complex and fragile. Manual testing is more appropriate.
}

func TestCloseCommand(t *testing.T) {
	// Save original global state
	origStore := store
	origDBPath := dbPath
	origDaemonClient := daemonClient
	defer func() {
		store = origStore
		dbPath = origDBPath
		daemonClient = origDaemonClient
	}()

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")

	// Create test store and set it globally
	testStore := newTestStore(t, testDB)
	defer testStore.Close()

	store = testStore
	dbPath = testDB
	daemonClient = nil // Force direct mode

	// Ensure BEADS_NO_DAEMON is set
	os.Setenv("BEADS_NO_DAEMON", "1")
	defer os.Unsetenv("BEADS_NO_DAEMON")

	ctx := context.Background()

	t.Run("close single issue", func(t *testing.T) {
		// Create test issue
		issue := &types.Issue{
			Title:     "Test Issue",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := testStore.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Reset command state
		rootCmd.SetArgs([]string{"close", issue.ID, "--reason", "Completed"})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("close command failed: %v", err)
		}

		// Verify issue was closed
		closed, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get closed issue: %v", err)
		}
		if closed.Status != types.StatusClosed {
			t.Errorf("Expected status %s, got %s", types.StatusClosed, closed.Status)
		}
		if closed.ClosedAt == nil {
			t.Error("Expected ClosedAt to be set")
		}
	})

	t.Run("close multiple issues", func(t *testing.T) {
		// Create test issues
		issue1 := &types.Issue{
			Title:     "First Issue",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := testStore.CreateIssue(ctx, issue1, "test-user"); err != nil {
			t.Fatalf("Failed to create issue1: %v", err)
		}

		issue2 := &types.Issue{
			Title:     "Second Issue",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := testStore.CreateIssue(ctx, issue2, "test-user"); err != nil {
			t.Fatalf("Failed to create issue2: %v", err)
		}

		// Reset command state
		rootCmd.SetArgs([]string{"close", issue1.ID, issue2.ID, "--reason", "Done"})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("close command failed: %v", err)
		}

		// Verify both issues were closed
		closed1, _ := testStore.GetIssue(ctx, issue1.ID)
		closed2, _ := testStore.GetIssue(ctx, issue2.ID)

		if closed1.Status != types.StatusClosed {
			t.Errorf("Expected issue1 status %s, got %s", types.StatusClosed, closed1.Status)
		}
		if closed2.Status != types.StatusClosed {
			t.Errorf("Expected issue2 status %s, got %s", types.StatusClosed, closed2.Status)
		}
	})

	t.Run("close with JSON output", func(t *testing.T) {
		// Create test issue
		issue := &types.Issue{
			Title:     "JSON Test Issue",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := testStore.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Capture output
		var buf bytes.Buffer
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		// Reset command state
		jsonOutput = true
		defer func() { jsonOutput = false }()
		rootCmd.SetArgs([]string{"close", issue.ID, "--reason", "Fixed", "--json"})

		err := rootCmd.Execute()

		// Restore stdout and read output
		w.Close()
		buf.ReadFrom(r)
		os.Stdout = oldStdout
		output := buf.String()

		if err != nil {
			t.Fatalf("close command failed: %v", err)
		}

		// Parse JSON output
		var result []map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v", err)
		}

		if len(result) != 1 {
			t.Fatalf("Expected 1 issue in result, got %d", len(result))
		}

		// Verify issue is closed
		if result[0]["status"] != string(types.StatusClosed) {
			t.Errorf("Expected status %s, got %v", types.StatusClosed, result[0]["status"])
		}
	})

	t.Run("close without reason", func(t *testing.T) {
		// Create test issue
		issue := &types.Issue{
			Title:     "No Reason Issue",
			Priority:  1,
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
		}
		if err := testStore.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}

		// Reset command state (no --reason flag)
		rootCmd.SetArgs([]string{"close", issue.ID})

		err := rootCmd.Execute()
		if err != nil {
			t.Fatalf("close command failed: %v", err)
		}

		// Verify issue was closed (should use default reason "Closed")
		closed, err := testStore.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("Failed to get closed issue: %v", err)
		}
		if closed.Status != types.StatusClosed {
			t.Errorf("Expected status %s, got %s", types.StatusClosed, closed.Status)
		}
	})
}
