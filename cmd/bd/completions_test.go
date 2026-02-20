//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/types"
)

func TestIssueIDCompletion(t *testing.T) {
	// Save original store and restore after test
	originalStore := store
	originalRootCtx := rootCtx
	defer func() {
		store = originalStore
		rootCtx = originalRootCtx
	}()

	// Set up test context
	ctx := context.Background()
	rootCtx = ctx

	// Create dolt store for testing
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")
	testStore := newTestStoreWithPrefix(t, testDB, "bd")
	store = testStore

	// Create test issues
	now := time.Now()
	testIssues := []*types.Issue{
		{
			ID:        "bd-abc1",
			Title:     "Test issue 1",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		},
		{
			ID:        "bd-abc2",
			Title:     "Test issue 2",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeBug,
		},
		{
			ID:        "bd-xyz1",
			Title:     "Another test issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeFeature,
		},
		{
			ID:        "bd-xyz2",
			Title:     "Yet another test",
			Status:    types.StatusClosed,
			Priority:  3,
			IssueType: types.TypeTask,
			ClosedAt:  &now,
		},
	}

	for _, issue := range testIssues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create test issue: %v", err)
		}
	}

	tests := []struct {
		name             string
		toComplete       string
		expectedCount    int
		shouldContain    []string
		shouldNotContain []string
	}{
		{
			name:          "Empty prefix returns all issues",
			toComplete:    "",
			expectedCount: 4,
			shouldContain: []string{"bd-abc1", "bd-abc2", "bd-xyz1", "bd-xyz2"},
		},
		{
			name:             "Prefix 'bd-a' returns matching issues",
			toComplete:       "bd-a",
			expectedCount:    2,
			shouldContain:    []string{"bd-abc1", "bd-abc2"},
			shouldNotContain: []string{"bd-xyz1", "bd-xyz2"},
		},
		{
			name:             "Prefix 'bd-abc' returns matching issues",
			toComplete:       "bd-abc",
			expectedCount:    2,
			shouldContain:    []string{"bd-abc1", "bd-abc2"},
			shouldNotContain: []string{"bd-xyz1", "bd-xyz2"},
		},
		{
			name:             "Prefix 'bd-abc1' returns exact match",
			toComplete:       "bd-abc1",
			expectedCount:    1,
			shouldContain:    []string{"bd-abc1"},
			shouldNotContain: []string{"bd-abc2", "bd-xyz1", "bd-xyz2"},
		},
		{
			name:             "Prefix 'bd-xyz' returns matching issues",
			toComplete:       "bd-xyz",
			expectedCount:    2,
			shouldContain:    []string{"bd-xyz1", "bd-xyz2"},
			shouldNotContain: []string{"bd-abc1", "bd-abc2"},
		},
		{
			name:             "Non-matching prefix returns empty",
			toComplete:       "bd-zzz",
			expectedCount:    0,
			shouldContain:    []string{},
			shouldNotContain: []string{"bd-abc1", "bd-abc2", "bd-xyz1", "bd-xyz2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a dummy command (not actually used by the function)
			cmd := &cobra.Command{}
			args := []string{}

			// Call the completion function
			completions, directive := issueIDCompletion(cmd, args, tt.toComplete)

			// Check directive
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("Expected directive NoFileComp (4), got %d", directive)
			}

			// Check count
			if len(completions) != tt.expectedCount {
				t.Errorf("Expected %d completions, got %d", tt.expectedCount, len(completions))
			}

			// Check that expected IDs are present
			for _, expectedID := range tt.shouldContain {
				found := false
				for _, completion := range completions {
					// Completion format is "ID\tTitle"
					if len(completion) > 0 && completion[:len(expectedID)] == expectedID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected completion to contain '%s', but it was not found", expectedID)
				}
			}

			// Check that unexpected IDs are NOT present
			for _, unexpectedID := range tt.shouldNotContain {
				for _, completion := range completions {
					if len(completion) > 0 && completion[:len(unexpectedID)] == unexpectedID {
						t.Errorf("Did not expect completion to contain '%s', but it was found", unexpectedID)
					}
				}
			}

			// Verify format: each completion should be "ID\tTitle"
			for _, completion := range completions {
				if len(completion) == 0 {
					t.Errorf("Got empty completion string")
					continue
				}
				// Check that it contains a tab character
				foundTab := false
				for _, c := range completion {
					if c == '\t' {
						foundTab = true
						break
					}
				}
				if !foundTab {
					t.Errorf("Completion '%s' doesn't contain tab separator", completion)
				}
			}
		})
	}
}

func TestIssueIDCompletion_NoStore(t *testing.T) {
	// Save original store and restore after test
	originalStore := store
	originalDBPath := dbPath
	defer func() {
		store = originalStore
		dbPath = originalDBPath
	}()

	// Set store to nil and dbPath to non-existent path
	store = nil
	dbPath = "/nonexistent/path/to/database.db"

	cmd := &cobra.Command{}
	args := []string{}

	completions, directive := issueIDCompletion(cmd, args, "")

	// Should return empty completions when store is nil and dbPath is invalid
	if len(completions) != 0 {
		t.Errorf("Expected 0 completions when store is nil and dbPath is invalid, got %d", len(completions))
	}

	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("Expected directive NoFileComp (4), got %d", directive)
	}
}

func TestCompleteCommandWorksWithoutDatabase(t *testing.T) {
	// This test verifies that shell completions work even without a beads database.
	// The __complete command must be in noDbCommands list so that PersistentPreRun
	// doesn't exit with "no beads database found" error.
	//
	// This test will FAIL on versions before the fix was applied, where __complete
	// was not in the noDbCommands list.

	// Create a temp directory with no .beads database
	tmpDir := t.TempDir()

	// Save original state
	originalDBPath := dbPath
	originalStore := store
	defer func() {
		dbPath = originalDBPath
		store = originalStore
	}()

	// Reset state to simulate no database
	store = nil
	dbPath = ""

	// Reset caches so FindDatabasePath won't use stale git worktree info
	beads.ResetCaches()
	git.ResetCaches()
	defer func() {
		beads.ResetCaches()
		git.ResetCaches()
	}()

	// Change to temp directory (no database present)
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	defer func() { _ = os.Chdir(originalWd) }()

	// Test that issueIDCompletion returns gracefully (empty list, no error)
	// when there's no database, rather than panicking or hanging
	cmd := &cobra.Command{}
	completions, directive := issueIDCompletion(cmd, []string{}, "")

	// Should return empty completions, not panic
	if completions == nil {
		// nil is acceptable, convert to empty slice for consistent handling
		completions = []string{}
	}

	// Should return NoFileComp directive
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("Expected directive NoFileComp (%d), got %d",
			cobra.ShellCompDirectiveNoFileComp, directive)
	}

	// Completions should be empty (no database to query)
	if len(completions) != 0 {
		t.Errorf("Expected 0 completions without database, got %d", len(completions))
	}
}

func TestCompleteCommandInNoDbCommandsList(t *testing.T) {
	// This integration test verifies that running `bd __complete show ""`
	// does NOT fail with "no beads database found" error.
	//
	// The __complete command is Cobra's internal command for shell completions.
	// It must be in the noDbCommands list to skip database initialization.
	//
	// Before the fix: this test would fail because __complete wasn't in noDbCommands,
	// causing PersistentPreRun to exit with "no beads database found".
	// After the fix: this test passes because __complete is in noDbCommands.

	// Create a temp directory with no .beads database
	tmpDir := t.TempDir()

	// Change to temp directory (no database present)
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}
	defer func() { _ = os.Chdir(originalWd) }()

	// Save and reset global state
	originalDBPath := dbPath
	originalStore := store
	defer func() {
		dbPath = originalDBPath
		store = originalStore
	}()

	store = nil
	dbPath = ""

	// Capture stdout/stderr
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	_, w, _ := os.Pipe()
	os.Stdout = w
	os.Stderr = w
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	// Run __complete command (this is what shell completion scripts call)
	rootCmd.SetArgs([]string{"__complete", "show", ""})
	err = rootCmd.Execute()

	// Close pipe to get output
	_ = w.Close()

	// The command should NOT fail - if __complete is in noDbCommands,
	// PersistentPreRun will skip database initialization and the completion
	// will return empty results gracefully
	if err != nil {
		t.Errorf("__complete command failed without database: %v\n"+
			"This indicates __complete is not in noDbCommands list.\n"+
			"Shell completions should work without requiring a database.", err)
	}
}

func TestIssueIDCompletion_EmptyDatabase(t *testing.T) {
	// Save original store and restore after test
	originalStore := store
	originalRootCtx := rootCtx
	defer func() {
		store = originalStore
		rootCtx = originalRootCtx
	}()

	// Set up test context
	ctx := context.Background()
	rootCtx = ctx

	// Create empty dolt store
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")
	testStore := newTestStoreWithPrefix(t, testDB, "bd")
	store = testStore

	cmd := &cobra.Command{}
	args := []string{}

	completions, directive := issueIDCompletion(cmd, args, "")

	// Should return empty completions when database is empty
	if len(completions) != 0 {
		t.Errorf("Expected 0 completions when database is empty, got %d", len(completions))
	}

	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("Expected directive NoFileComp (4), got %d", directive)
	}
}
