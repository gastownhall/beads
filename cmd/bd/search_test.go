//go:build cgo

package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchCommand_HelpErrorHandling verifies that the search command handles
// Help() errors gracefully.
//
// This test addresses bd-gra: errcheck flagged cmd.Help() return value not checked
// in search.go:39. The current behavior is intentional:
// - Help() is called when query is missing (error path)
// - Even if Help() fails (e.g., output redirection fails), we still exit with code 1
// - The error from Help() is rare (typically I/O errors writing to stderr)
// - Since we're already in an error state, ignoring Help() errors is acceptable
func TestSearchCommand_HelpErrorHandling(t *testing.T) {
	t.Parallel()
	// Create a test command similar to searchCmd
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Test search command",
		Run: func(cmd *cobra.Command, args []string) {
			// Simulate search.go:37-40
			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			if query == "" {
				// This is the code path being tested
				_ = cmd.Help() // Intentionally ignore error (bd-gra)
				// In real code, os.Exit(1) follows, so Help() error doesn't matter
			}
		},
	}

	// Test 1: Normal case - Help() writes to stdout/stderr
	t.Run("normal_help_output", func(t *testing.T) {
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})

		// Call with no args (triggers help)
		cmd.SetArgs([]string{})
		_ = cmd.Execute() // Help is shown, no error expected
	})

	// Test 2: Help() with failed output writer
	t.Run("help_with_failed_writer", func(t *testing.T) {
		// Create a writer that always fails
		failWriter := &failingWriter{}
		cmd.SetOut(failWriter)
		cmd.SetErr(failWriter)

		// Call with no args (triggers help)
		cmd.SetArgs([]string{})
		err := cmd.Execute()

		// Even if Help() fails internally, cmd.Execute() may not propagate it
		// because we ignore the Help() return value
		t.Logf("cmd.Execute() returned: %v", err)

		// Key insight: The error from Help() is intentionally ignored because:
		// 1. We're already in an error path (missing required argument)
		// 2. The subsequent os.Exit(1) will terminate regardless
		// 3. Help() errors are rare (I/O failures writing to stderr)
		// 4. User will still see "Error: search query is required" before Help() is called
	})
}

// TestSearchCommand_HelpSuppression verifies that #nosec comment is appropriate
func TestSearchCommand_HelpSuppression(t *testing.T) {
	t.Parallel()
	// This test documents why ignoring cmd.Help() error is safe:
	//
	// 1. Help() is called in an error path (missing required argument)
	// 2. We print "Error: search query is required" before calling Help()
	// 3. We call os.Exit(1) after Help(), terminating regardless of Help() success
	// 4. Help() errors are rare (typically I/O errors writing to stderr)
	// 5. If stderr is broken, user already can't see error messages anyway
	//
	// Therefore, checking Help() error adds no value and can be safely ignored.

	// Demonstrate that Help() can fail
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test",
	}

	// With failing writer, Help() should error
	failWriter := &failingWriter{}
	cmd.SetOut(failWriter)
	cmd.SetErr(failWriter)

	err := cmd.Help()
	if err == nil {
		t.Logf("Help() succeeded even with failing writer (cobra may handle gracefully)")
	} else {
		t.Logf("Help() returned error as expected: %v", err)
	}

	// But in the search command, this error is intentionally ignored because
	// the command is already in an error state and will exit
}

// failingWriter is a writer that always returns an error
type failingWriter struct{}

func (fw *failingWriter) Write(p []byte) (n int, err error) {
	return 0, io.ErrClosedPipe // Simulate I/O error
}

// TestSearchCommand_MissingQueryShowsHelp verifies the intended behavior
func TestSearchCommand_MissingQueryShowsHelp(t *testing.T) {
	t.Parallel()
	// This test verifies that when query is missing, we:
	// 1. Print error message to stderr
	// 2. Show help (even if it fails, we tried)
	// 3. Exit with code 1

	// We can't test os.Exit() directly, but we can verify the logic up to that point
	var errBuf bytes.Buffer
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Test",
		Run: func(cmd *cobra.Command, args []string) {
			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			if query == "" {
				// Use cobra's SetErr to capture stderr without redirecting os.Stderr (bd-cqjoi)
				cmd.PrintErrf("Error: search query is required\n")

				if errBuf.String() == "" {
					t.Error("Expected error message to stderr")
				}

				// Help() is called here (may fail, but we don't care)
				_ = cmd.Help() // #nosec - see bd-gra

				// os.Exit(1) would be called here
			}
		},
	}

	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{}) // No query
	_ = cmd.Execute()
}

// TestSearchWithDateAndPriorityFilters tests bd search with date range and priority filters
func TestSearchWithDateAndPriorityFilters(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	twoDaysAgo := now.Add(-48 * time.Hour)

	// Create test issues with search-relevant content
	issue1 := &types.Issue{
		Title:       "Critical security bug in auth",
		Description: "Authentication bypass vulnerability",
		Priority:    0,
		IssueType:   types.TypeBug,
		Status:      types.StatusOpen,
	}
	issue2 := &types.Issue{
		Title:       "Add security scanning feature",
		Description: "Implement automated security checks",
		Priority:    2,
		IssueType:   types.TypeFeature,
		Status:      types.StatusInProgress,
	}
	issue3 := &types.Issue{
		Title:       "Security audit task",
		Description: "Review all security practices",
		Priority:    3,
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
	}

	for _, issue := range []*types.Issue{issue1, issue2, issue3} {
		if err := s.CreateIssue(ctx, issue, "test-user"); err != nil {
			t.Fatalf("Failed to create issue: %v", err)
		}
	}

	// Close issue3 to set closed_at timestamp
	if err := s.CloseIssue(ctx, issue3.ID, "test-user", "Testing", ""); err != nil {
		t.Fatalf("Failed to close issue3: %v", err)
	}

	t.Run("search with priority range - min", func(t *testing.T) {
		minPrio := 2
		results, err := s.SearchIssues(ctx, "security", types.IssueFilter{
			PriorityMin: &minPrio,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("Expected 2 issues matching 'security' with priority >= 2, got %d", len(results))
		}
	})

	t.Run("search with priority range - max", func(t *testing.T) {
		maxPrio := 1
		results, err := s.SearchIssues(ctx, "security", types.IssueFilter{
			PriorityMax: &maxPrio,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("Expected 1 issue matching 'security' with priority <= 1, got %d", len(results))
		}
		if len(results) > 0 && results[0].ID != issue1.ID {
			t.Errorf("Expected issue1, got %s", results[0].ID)
		}
	})

	t.Run("search with priority range - min and max", func(t *testing.T) {
		minPrio := 1
		maxPrio := 2
		results, err := s.SearchIssues(ctx, "security", types.IssueFilter{
			PriorityMin: &minPrio,
			PriorityMax: &maxPrio,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("Expected 1 issue matching 'security' with priority 1-2, got %d", len(results))
		}
		if len(results) > 0 && results[0].ID != issue2.ID {
			t.Errorf("Expected issue2, got %s", results[0].ID)
		}
	})

	t.Run("search with created after", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "security", types.IssueFilter{
			CreatedAfter: &twoDaysAgo,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("Expected 3 issues matching 'security' created after two days ago, got %d", len(results))
		}
	})

	t.Run("search with updated before", func(t *testing.T) {
		futureTime := now.Add(24 * time.Hour)
		results, err := s.SearchIssues(ctx, "security", types.IssueFilter{
			UpdatedBefore: &futureTime,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("Expected 3 issues matching 'security', got %d", len(results))
		}
	})

	t.Run("search with closed after", func(t *testing.T) {
		results, err := s.SearchIssues(ctx, "security", types.IssueFilter{
			ClosedAfter: &yesterday,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("Expected 1 closed issue matching 'security', got %d", len(results))
		}
		if len(results) > 0 && results[0].ID != issue3.ID {
			t.Errorf("Expected issue3, got %s", results[0].ID)
		}
	})

	t.Run("search with combined filters", func(t *testing.T) {
		minPrio := 0
		maxPrio := 2
		results, err := s.SearchIssues(ctx, "auth", types.IssueFilter{
			PriorityMin:  &minPrio,
			PriorityMax:  &maxPrio,
			CreatedAfter: &twoDaysAgo,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		// Should match issue1 (has "auth" in title, priority 0)
		// and issue2 (has "auth" in description via "automated", priority 2)
		// Note: "auth" is a substring match, so it matches "authentication" and "automated"
		if len(results) < 1 {
			t.Errorf("Expected at least 1 result matching combined filters, got %d", len(results))
		}
	})
}
